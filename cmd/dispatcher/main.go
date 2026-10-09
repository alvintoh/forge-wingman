// Command dispatcher is the scheduled job that polls Linear and dispatches runs.
//
// It reads the issues Linear has delegated to the configured agent, queues the
// ones it can build, and dispatches the queued runs admission lets start, in
// priority order, each into the repository its ticket names. Everything it
// holds is a reference to a value provisioned with the job: the Linear app's
// client credentials and the GitHub token are read from Secret Manager as the
// run starts, and WINGMAN_REPOS is the allowlist a ticket's repo: label must
// name. Each poll mints its own Linear token and revokes it as the poll ends.
//
// The job runs under its own service account, holds the store, the Linear
// credentials, the GitHub token and the notice webhook, and holds nothing that
// reaches a repository.
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
	"github.com/alvintoh/forge-wingman/internal/linear"
	"github.com/alvintoh/forge-wingman/internal/providers"
	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

const (
	// runWorkflow and runBranch are the workflow a dispatch starts and the ref
	// it starts it from: the runner's federation trusts main only, and run.yml's
	// jobs refuse any other ref.
	runWorkflow = "run.yml"
	runBranch   = "main"
	// githubTokenSecret names the Secret Manager secret the job reads its
	// GitHub token from, which infra/ creates without a value: the value is
	// added by hand, and a rotation is the next run's problem rather than a
	// redeploy's.
	githubTokenSecret = "github-token"
	// noticeWebhookSecret names the Slack webhook notices are posted to, read
	// only when a notice is waiting.
	noticeWebhookSecret = "notice-webhook-url"
	// requestTimeout bounds one call to Linear or GitHub, so a poll cannot sit
	// on a hung connection until Cloud Run's own deadline.
	requestTimeout = 30 * time.Second
)

// budgetConfig is FR-22's admission ceilings for the provider that serves the
// dispatched model: that provider's own rolling allowance windows and its
// per-model monthly caps, plus the cash ceiling and the GitHub Actions
// free-minutes allowance shared by every provider, all read from provider
// configuration rather than hardcoded. The cash ceiling is a calendar
// month (GitHub's own billing cycle); the runner-minutes ceiling is GitHub
// Actions' free 2,000 minutes/month on a private target repository,
// hard-stopped there by default (RatePerMinute zero) since no payment method is
// assumed configured. The windows meter SettledProviderCostMicros, which the
// harness's runs do not yet fill (FRG-47). A per-model cap binds a run only on
// the model it names (FRG-62).
func budgetConfig(provider string) dispatcher.BudgetConfig {
	var windows []dispatcher.Window
	for _, w := range providers.Windows(provider) {
		windows = append(windows, dispatcher.Window{Name: w.Name, Period: w.Period, Limit: w.Limit})
	}
	var modelCaps map[string]dispatcher.Window
	if caps := providers.ModelCaps(provider); len(caps) > 0 {
		modelCaps = make(map[string]dispatcher.Window, len(caps))
		for model, w := range caps {
			modelCaps[model] = dispatcher.Window{Name: w.Name, Period: w.Period, Limit: w.Limit}
		}
	}
	return dispatcher.BudgetConfig{
		ProviderWindows: windows,
		ModelCaps:       modelCaps,
		Cash:            dispatcher.Window{Name: dispatcher.CeilingCash, Calendar: true, Limit: providers.CashLimit()},
		Runner:          dispatcher.RunnerMinutes{FreeMinutes: providers.RunnerFreeMinutes()},
	}
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

// slackPoster opens the Slack poster from the webhook secret, trimmed of the
// newline a value added with echo carries, which no URL parses with.
func slackPoster(token func(context.Context, string) (string, error), client *http.Client) func(context.Context) (dispatcher.Poster, error) {
	return func(ctx context.Context) (dispatcher.Poster, error) {
		webhook, err := token(ctx, noticeWebhookSecret)
		if err != nil {
			return nil, err
		}
		return dispatcher.Slack{Webhook: strings.TrimSpace(webhook), Client: client}, nil
	}
}

// run polls once: the schedule starts the job, and one execution is one poll.
func run(ctx context.Context, logger *slog.Logger, getenv func(string) string) error {
	started := time.Now()
	logger.Info("dispatcherStarted")
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
	client := &http.Client{Timeout: requestTimeout}
	creds, err := linear.ReadCredentials(ctx, linear.DispatchApp, secrets.Token, client)
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
	tokens := linear.NewPollSource(creds)
	defer func() {
		start := time.Now()
		linear.RevokeAll(ctx, tokens, logger)
		dispatcher.LogPhase(logger, "revoke", start, time.Now())
	}()
	dispatcher.LogPhase(logger, "setup", started, time.Now())
	gh := dispatcher.GitHub{Token: githubToken, Workflow: runWorkflow, Branch: runBranch, Client: client}
	queue := store.NewQueue(fsc)
	plans := store.NewPlans(fsc)
	model := runner.DefaultModel()
	if _, err := dispatcher.Poll(ctx, dispatcher.Deps{
		Source:     dispatcher.Linear{Tokens: tokens, Delegate: c.delegate, Client: client},
		Queue:      queue,
		Verdicts:   queue,
		Estimator:  store.NewEstimates(fsc),
		Visibility: gh,
		Providers:  store.NewProviders(fsc),
		Breaker:    store.NewBreaker(fsc),
		Notices:    store.NewNotices(fsc),
		OpenPoster: slackPoster(secrets.Token, client),
		Plans:      plans,
		ModelPlans: plans,
		Overrides:  dispatcher.LabelOverrides{},
		Workflow:   gh,
		OpenPRs:    gh,
		Logger:     logger,
		Now:        time.Now,
	}, dispatcher.Config{Repos: c.repos, Budget: budgetConfig(runner.Provider(model)), Model: model, Limits: c.limits, Tuning: c.tuning, LastResort: providers.LastResortModels(runner.Provider(model))}); err != nil {
		return err
	}
	return nil
}
