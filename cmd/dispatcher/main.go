// Command dispatcher is the scheduled job that polls Linear and dispatches runs.
//
// It reads the issues Linear has delegated to the configured agent, queues the
// ones it can build, and dispatches the highest-priority queued run into the
// repository its ticket names. Everything it holds is a reference to a value
// provisioned with the job: the Linear and GitHub tokens are read from Secret
// Manager as the run starts, and WINGMAN_REPOS is the allowlist a ticket's
// repo: label must name.
//
// The job runs under its own service account, holds the store and the two API
// tokens, and holds nothing that reaches a repository.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"cloud.google.com/go/firestore"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
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
// Linear agent it acts for, and the repositories it may dispatch into. The
// tokens are not here because a poll reads them from Secret Manager.
type config struct {
	project  string
	delegate string
	repos    []string
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
	return c, nil
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
	if _, err := dispatcher.Poll(ctx, dispatcher.Deps{
		Source: dispatcher.Linear{Token: linear, Delegate: c.delegate, Client: client},
		Queue:  store.NewQueue(fsc),
		Workflow: dispatcher.GitHub{
			Token: githubToken, Workflow: runWorkflow, Branch: runBranch, Client: client,
		},
		Logger: logger,
		Now:    time.Now,
	}, dispatcher.Config{Repos: c.repos}); err != nil {
		return err
	}
	return nil
}
