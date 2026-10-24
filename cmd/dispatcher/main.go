// Command dispatcher is the scheduled job that polls Linear and dispatches runs.
//
// It reads the issues Linear has delegated to the configured agent, queues the
// ones it can build, and dispatches the queued runs admission lets start, in
// priority order, each into the repository its ticket names. Everything it
// holds is a reference to a value provisioned with the job: the Linear app's
// client credentials and the GitHub token are read from Secret Manager as the
// run starts, and the allowlist a ticket's repo: label must name is read from
// the owner database. Each poll mints its own Linear token and revokes it as
// the poll ends.
//
// The job runs under its own service account, holds the store, the Linear
// credentials, the GitHub token and the notice webhook, and holds nothing that
// reaches a repository.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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
	"github.com/alvintoh/forge-wingman/internal/webhook"
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
// harness's runs do not yet fill; TODO(FRG-47) tracks that. A per-model cap
// binds a run only on the model it names.
func budgetConfig(provider string) dispatcher.BudgetConfig {
	var modelCaps map[string]dispatcher.Window
	if caps := providers.ModelCaps(provider); len(caps) > 0 {
		modelCaps = make(map[string]dispatcher.Window, len(caps))
		for model, w := range caps {
			modelCaps[model] = dispatcher.Window{Name: w.Name, Period: w.Period, Limit: w.Limit}
		}
	}
	return dispatcher.BudgetConfig{
		ProviderWindows: dispatcher.ProviderWindows(provider),
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
// Linear agent it acts for, and the optional concurrency limits and tuning,
// where zero means the default. The tokens are not here because a poll reads
// them from Secret Manager.
type config struct {
	project  string
	delegate string
	limits   dispatcher.Limits
	tuning   dispatcher.Tuning
	twoStage bool
	// decisionTimeout is how long a run waits on an owner decision; zero takes
	// the code default.
	decisionTimeout time.Duration
	// decisionMode is whether a plan's decisions wait for the owner or take the recommended option.
	decisionMode runner.DecisionMode
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
	var err error
	if c.limits, c.tuning, err = dispatcher.ConcurrencySettings(getenv); err != nil {
		return config{}, err
	}
	c.twoStage = dispatcher.TwoStage(getenv)
	if raw := strings.TrimSpace(getenv("WINGMAN_DECISION_TIMEOUT")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return config{}, fmt.Errorf("WINGMAN_DECISION_TIMEOUT = %q is not a positive duration", raw)
		}
		c.decisionTimeout = d
	}
	if c.decisionMode, err = runner.ParseDecisionMode(getenv("WINGMAN_DECISIONS")); err != nil {
		return config{}, fmt.Errorf("WINGMAN_DECISIONS: %w", err)
	}
	return c, nil
}

// repositoryLister is the owner's project allowlist as the dispatcher reads it.
type repositoryLister interface {
	Repositories(ctx context.Context) ([]string, error)
}

// listedRepositories is the repositories the owner listed. An entry that is not
// owner/name fails the read rather than being skipped: a list that cannot be
// trusted whole admits nothing.
func listedRepositories(ctx context.Context, projects repositoryLister) ([]string, error) {
	repos, err := projects.Repositories(ctx)
	if err != nil {
		return nil, err
	}
	for _, repo := range repos {
		if !store.ValidRepository(repo) {
			return nil, fmt.Errorf("listed repository %q is not owner/name", repo)
		}
	}
	return repos, nil
}

// ownerRepositories reads the allowlist from the owner database once, for the
// poll that follows.
func ownerRepositories(ctx context.Context, owner *firestore.Client) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	return listedRepositories(ctx, store.NewProjects(owner))
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
	owner, err := firestore.NewClientWithDatabase(ctx, c.project, store.OwnerDatabase)
	if err != nil {
		return fmt.Errorf("owner database client: %w", err)
	}
	defer func() { _ = owner.Close() }()
	repos, reposErr := ownerRepositories(ctx, owner)
	dispatcher.LogPhase(logger, "setup", started, time.Now())
	gh := dispatcher.GitHub{Token: githubToken, Workflow: runWorkflow, Branch: runBranch, Client: client}
	queue := store.NewQueue(fsc).WithLogger(logger)
	plans := store.NewPlans(fsc)
	model := runner.DefaultModel()
	next, err := nextPlans(providers.PaidPlans(), model)
	if err != nil {
		return err
	}
	tickets := dispatcher.Linear{Tokens: tokens, Delegate: c.delegate, Client: client}
	if _, err := dispatcher.Poll(ctx, dispatcher.Deps{
		Source: tickets,
		// The same client moves a claimed ticket In Progress and puts it back
		// when a requeue or an abandon undoes the claim.
		TicketStates: tickets,
		Queue:        queue,
		Verdicts:     queue,
		Reach:        store.NewReaches(fsc),
		// The projects the owner may add are read as the agent the poll acts for,
		// and recorded where the surface reads them without a Linear credential.
		LinearProjects: tickets,
		ProjectRecords: store.NewProjectNames(fsc),
		ProjectPhases: dispatcher.ProjectPhaseDeps{
			Projects:  store.NewPhases(owner),
			Snapshots: store.NewPhaseSnapshots(fsc),
			Runs:      queue,
			Issues:    tickets,
		},
		Estimator:  store.NewEstimates(fsc),
		Visibility: gh,
		Providers:  store.NewProviders(fsc),
		Breaker:    store.NewBreaker(fsc),
		Phases:     store.NewPhases(owner),
		PeakModes:  store.NewPeakModes(fsc),
		Notices:    store.NewNotices(fsc),
		OpenPoster: slackPoster(secrets.Token, client),
		Plans:      plans,
		ModelPlans: plans,
		Overrides:  dispatcher.LabelOverrides{},
		Workflow:   gh,
		OpenPRs:    gh,
		// The rewrite rate is measured from the same GitHub the dispatch uses,
		// and recorded on the run rows the queue holds.
		PullRequests: gh,
		Rewrites:     store.NewRewrites(fsc),
		Decisions:    queue,
		Elicit:       webhook.Linear{Tokens: tokens},
		Logger:       logger,
		Now:          time.Now,
	}, dispatcher.Config{Repos: repos, ReposErr: reposErr, Budget: budgetConfig(runner.Provider(model)), Model: model, NextPlans: next, Limits: c.limits, Tuning: c.tuning, LastResortPlans: lastResortPlans(), TwoStage: c.twoStage, DecisionTimeout: c.decisionTimeout, DecisionMode: c.decisionMode}); err != nil {
		return err
	}
	return nil
}

// nextPlans is the paid plans a run moves to once the default model's plan
// binds: every plan after it in order, each with its own budget and the models
// a run moved onto it starts on. The first plan in order must be the default
// model's, since that is where every run starts.
func nextPlans(order []string, model string) ([]dispatcher.PaidPlan, error) {
	if len(order) == 0 {
		return nil, nil
	}
	if first := runner.Provider(model); order[0] != first {
		return nil, fmt.Errorf("the first paid plan is %s, not %s, the default model's", order[0], first)
	}
	var next []dispatcher.PaidPlan
	for _, plan := range order[1:] {
		m := providers.PlanModels(plan)
		next = append(next, dispatcher.PaidPlan{
			Plan: plan, Budget: budgetConfig(plan),
			Models: runner.ModelLabels{Build: m.Build, Review: m.Review, Plan: m.Plan},
		})
	}
	return next, nil
}

// lastResortPlans is the ordered free-tier plans the fallback walks: each
// configured plan's own free models, in the order the configuration names them.
func lastResortPlans() [][]string {
	var rungs [][]string
	for _, plan := range providers.LastResortPlans() {
		rungs = append(rungs, providers.LastResortModels(plan))
	}
	return rungs
}
