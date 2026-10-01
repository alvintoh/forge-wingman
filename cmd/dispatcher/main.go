// Command dispatcher is the scheduled job that polls Linear and dispatches runs.
//
// It reads the issues Linear has delegated to the configured agent, queues the
// ones it can build, and dispatches the queued runs admission lets start, in
// priority order, each into the repository its ticket names. Everything it
// holds is a reference to a value provisioned with the job: the Linear and
// GitHub tokens are read from Secret Manager as the run starts, and
// WINGMAN_REPOS is the allowlist a ticket's repo: label must name.
//
// The job runs under its own service account, holds the store and the two API
// tokens, and holds nothing that reaches a repository.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"cloud.google.com/go/firestore"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

const (
	// runWorkflow and runBranch are the workflow a dispatch starts and the ref
	// it starts it from: the runner's federation trusts main only, and run.yml's
	// jobs refuse any other ref.
	runWorkflow = "run.yml"
	runBranch   = "main"
	// linearTokenSecret and githubTokenSecret name the Secret Manager secrets
	// the job reads its API tokens from, which infra/ creates without a value:
	// the tokens themselves are added by hand, and a rotation is the next run's
	// problem rather than a redeploy's.
	linearTokenSecret = "linear-token"
	githubTokenSecret = "github-token"
	// requestTimeout bounds one call to Linear or GitHub, so a poll cannot sit
	// on a hung connection until Cloud Run's own deadline.
	requestTimeout = 30 * time.Second
)

// budgetConfig is FR-22's admission ceilings, assumed against OpenCode Go per
// the PRD's Constraints table: its own rolling allowance windows — $12/5h,
// $30/week, $60/month — checked INSTEAD OF the cash ceiling for provider
// cost, since its $10/month subscription already satisfies NFR-1's $20 cash
// cap; the cash ceiling itself, a calendar month (GitHub's own billing
// cycle); and GitHub Actions' free 2,000 minutes/month on a private target
// repository, hard-stopped there by default (RatePerMinute zero) since no
// payment method is assumed configured.
//
// Hardcoded rather than read from the environment: NFR-3 calls every one of
// these a configuration value, and making a nested structure like this
// env-configurable is a deliberate scope cut for FRG-20 — see the PR's Known
// Limitations.
var budgetConfig = dispatcher.BudgetConfig{
	ProviderWindows: []dispatcher.Window{
		{Name: "opencode-go-5h", Period: 5 * time.Hour, Limit: 12 * money.Dollar},
		{Name: "opencode-go-week", Period: 7 * 24 * time.Hour, Limit: 30 * money.Dollar},
		{Name: "opencode-go-month", Period: 30 * 24 * time.Hour, Limit: 60 * money.Dollar},
	},
	Cash:   dispatcher.Window{Name: dispatcher.CeilingCash, Calendar: true, Limit: 20 * money.Dollar},
	Runner: dispatcher.RunnerMinutes{FreeMinutes: 2000},
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, logger, os.Getenv)
	stop()
	if err != nil {
		logger.Error("dispatcherFailed", "err", err)
		os.Exit(1)
	}
}

// config is what one poll needs: the project whose store is the queue, the
// Linear agent it acts for, the repositories it may dispatch into, and the
// optional concurrency limits and tuning, where zero means the default. The
// tokens are not here because a poll reads them from Secret Manager.
type config struct {
	project  string
	delegate string
	repos    []string
	limits   dispatcher.Limits
	tuning   dispatcher.Tuning
}

func loadConfig(getenv func(string) string) (config, error) {
	c := config{
		project:  getenv("GOOGLE_CLOUD_PROJECT"),
		delegate: getenv("LINEAR_DELEGATE"),
	}
	var missing []string
	for _, kv := range [][2]string{
		{"GOOGLE_CLOUD_PROJECT", c.project},
		{"LINEAR_DELEGATE", c.delegate},
	} {
		if kv[1] == "" {
			missing = append(missing, kv[0])
		}
	}
	if len(missing) > 0 {
		return config{}, fmt.Errorf("missing environment: %v", missing)
	}
	repos, err := allowlist(getenv("WINGMAN_REPOS"))
	if err != nil {
		return config{}, err
	}
	c.repos = repos
	if c.limits, c.tuning, err = concurrencySettings(getenv); err != nil {
		return config{}, err
	}
	return c, nil
}

// concurrencySettings reads the optional limits and tuning the WINGMAN_*
// variables name. An unset one is zero, so its default applies; one that is set
// must be positive, and the fall band must lie above the rise band. The names
// are the keys infra/variables.tf allows in dispatcher_settings.
func concurrencySettings(getenv func(string) string) (dispatcher.Limits, dispatcher.Tuning, error) {
	var l dispatcher.Limits
	var t dispatcher.Tuning
	for _, s := range []struct {
		key  string
		into *int
	}{
		{"WINGMAN_PLATFORM_CAP", &l.PlatformCap},
		{"WINGMAN_LARGE_CAP", &l.LargeCap},
		{"WINGMAN_REVIEW_WIP", &l.ReviewWIP},
		{"WINGMAN_STABLE_RUNS", &t.StableRuns},
	} {
		raw := strings.TrimSpace(getenv(s.key))
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return l, t, fmt.Errorf("%s = %q is not a positive whole number", s.key, raw)
		}
		*s.into = n
	}
	for _, s := range []struct {
		key  string
		into *float64
	}{
		{"WINGMAN_RISE_WITHIN", &t.RiseWithin},
		{"WINGMAN_HALVE_BEYOND", &t.HalveBeyond},
	} {
		raw := strings.TrimSpace(getenv(s.key))
		if raw == "" {
			continue
		}
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil || !(f > 0) || math.IsInf(f, 0) {
			return l, t, fmt.Errorf("%s = %q is not a positive number", s.key, raw)
		}
		*s.into = f
	}
	if eff := t.OrDefault(); eff.HalveBeyond <= eff.RiseWithin {
		return l, t, errors.New("WINGMAN_HALVE_BEYOND must exceed WINGMAN_RISE_WITHIN")
	}
	return l, t, nil
}

// repoPattern is what GitHub calls a repository: an owner and a name.
var repoPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

// allowlist is the repositories WINGMAN_REPOS names, comma separated. An empty
// allowlist is refused rather than admitted: a dispatcher that was not
// configured must dispatch into nothing.
func allowlist(raw string) ([]string, error) {
	var repos []string
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !repoPattern.MatchString(entry) {
			return nil, fmt.Errorf("WINGMAN_REPOS entry %q is not owner/name", entry)
		}
		repos = append(repos, entry)
	}
	if len(repos) == 0 {
		return nil, errors.New("WINGMAN_REPOS names no repository")
	}
	return repos, nil
}

// run polls once: the schedule starts the job, and one execution is one poll.
func run(ctx context.Context, logger *slog.Logger, getenv func(string) string) error {
	c, err := loadConfig(getenv)
	if err != nil {
		return err
	}
	secretsClient, err := secretmanager.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("secret manager client: %w", err)
	}
	defer func() { _ = secretsClient.Close() }()
	secrets := dispatcher.NewSecrets(c.project, secretsClient)
	linear, err := secrets.Token(ctx, linearTokenSecret)
	if err != nil {
		return err
	}
	githubToken, err := secrets.Token(ctx, githubTokenSecret)
	if err != nil {
		return err
	}
	fsc, err := firestore.NewClient(ctx, c.project)
	if err != nil {
		return fmt.Errorf("firestore client: %w", err)
	}
	defer func() { _ = fsc.Close() }()
	client := &http.Client{Timeout: requestTimeout}
	gh := dispatcher.GitHub{Token: githubToken, Workflow: runWorkflow, Branch: runBranch, Client: client}
	if _, err := dispatcher.Poll(ctx, dispatcher.Deps{
		Source:     dispatcher.Linear{Token: linear, Delegate: c.delegate, Client: client},
		Queue:      store.NewQueue(fsc),
		Estimator:  store.NewEstimates(fsc),
		Visibility: gh,
		Providers:  store.NewProviders(fsc),
		Workflow:   gh,
		OpenPRs:    gh,
		Logger:     logger,
		Now:        time.Now,
	}, dispatcher.Config{Repos: c.repos, Budget: budgetConfig, Model: runner.DefaultModel, Limits: c.limits, Tuning: c.tuning}); err != nil {
		return err
	}
	return nil
}
