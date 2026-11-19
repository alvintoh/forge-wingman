// Command dispatcher is the scheduled job that polls Linear and dispatches runs.
//
// It reads the issues Linear has delegated to the configured agent, queues the
// ones it can build, and dispatches the queued runs admission lets start, in
// priority order, each into the repository its ticket names. Everything it
// holds is a reference to a value provisioned with the job: the Linear app's
// client credentials, the GitHub token and the OpenCode Go key admission reads
// the plan's live usage with are read from Secret Manager as the run starts,
// and the allowlist a ticket's repo: label must name is read from the owner
// database. Each poll mints its own Linear token and revokes it as the poll
// ends.
//
// The job runs under its own service account, holds the store, the Linear
// credentials, the GitHub token, the OpenCode Go key and the notice webhook,
// and holds nothing that reaches a repository.
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
	// goUsageKeySecret names the Secret Manager secret holding the OpenCode Go
	// key the live usage read authenticates with: the same key the runner's
	// model calls bill against, so the windows it reports are the runs' own.
	goUsageKeySecret = "opencode-api-key"
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
	return dispatcher.BudgetConfig{
		ProviderWindows: dispatcher.ProviderWindows(provider),
		ModelCaps:       capWindows(providers.ModelCaps(provider)),
		Cash:            dispatcher.Window{Name: dispatcher.CeilingCash, Calendar: true, Limit: providers.CashLimit()},
		Runner:          dispatcher.RunnerMinutes{FreeMinutes: providers.RunnerFreeMinutes()},
	}
}

// capWindows is a plan's per-model caps as the dispatcher meters them, or nil
// when it caps none.
func capWindows(caps map[string]providers.Window) map[string]dispatcher.Window {
	if len(caps) == 0 {
		return nil
	}
	windows := make(map[string]dispatcher.Window, len(caps))
	for model, w := range caps {
		windows[model] = dispatcher.Window{Name: w.Name, Period: w.Period, Limit: w.Limit}
	}
	return windows
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

// settingReader is one of the owner's whole-number settings as a poll reads it.
type settingReader interface {
	Value(ctx context.Context) (int, error)
}

// ownerRunsPerRepo reads the cap once, for the poll that follows. A cap that
// cannot be read is the default, never no cap at all, and one read outside the
// setting's range is held to the nearest end of it, so the cap is never lifted
// past the maximum nor lowered past the minimum.
func ownerRunsPerRepo(ctx context.Context, logger *slog.Logger, reader settingReader) int {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	n, err := reader.Value(ctx)
	if err != nil {
		logger.Warn("runsPerRepoNotRead", "err", err.Error(), "runs_per_repo", dispatcher.DefaultRunsPerRepo)
		return dispatcher.DefaultRunsPerRepo
	}
	held := min(max(n, dispatcher.MinRunsPerRepo), dispatcher.MaxRunsPerRepo)
	if held != n {
		logger.Warn("runsPerRepoOutOfRange", "runs_per_repo", n, "held_to", held)
	}
	return held
}

// pollClock is the record of the last polls and the last wake request as an
// execution reads and stamps it.
type pollClock interface {
	Times(ctx context.Context) (dispatcher.PollTimes, error)
	MarkPolled(ctx context.Context, scheduled bool) error
}

// ownerPollInterval reads the owner's interval once. One that cannot be read,
// or is not an interval the owner may save, is the default 15 minutes, so the
// dispatcher never polls less often than it did before the setting existed.
func ownerPollInterval(ctx context.Context, logger *slog.Logger, reader settingReader) time.Duration {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	n, err := reader.Value(ctx)
	if err == nil && !dispatcher.ValidPollMinutes(n) {
		err = fmt.Errorf("%d is not an interval the owner may save", n)
	}
	if err != nil {
		logger.Warn("pollIntervalNotRead", "err", err.Error(), "poll_minutes", dispatcher.DefaultPollMinutes)
		n = dispatcher.DefaultPollMinutes
	}
	return time.Duration(n) * time.Minute
}

// decidePoll decides this execution, reading nothing but the owner's interval
// and the poll times. A wake always polls; a scheduled tick polls only once the
// interval has passed since the last scheduled poll. Poll times that cannot be
// read let the execution poll, as every one did before the setting existed, as
// a wake's poll, so it never delays the next tick.
func decidePoll(ctx context.Context, logger *slog.Logger, interval settingReader, clock pollClock, now time.Time) dispatcher.PollStart {
	every := ownerPollInterval(ctx, logger, interval)
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	times, err := clock.Times(ctx)
	start := dispatcher.PollDue(times, every, now)
	if err != nil {
		logger.Warn("pollTimesNotRead", "err", err.Error())
		start = dispatcher.PollStart{Due: true, Reason: "poll-times-unread"}
	}
	minutes := int(every / time.Minute)
	if !start.Due {
		logger.Info("pollSkipped", "reason", start.Reason, "last_scheduled_poll", times.LastScheduled, "poll_minutes", minutes)
		return start
	}
	logger.Info("pollStarting", "reason", start.Reason, "scheduled", start.Scheduled, "poll_minutes", minutes)
	return start
}

// pollSetup readies a poll after the decision to make one: the credentials and
// clients it needs, returning the poll itself.
type pollSetup func(ctx context.Context) (func(ctx context.Context) error, error)

// gatedPoll runs one execution: decide, set up, stamp, poll. The stamp comes
// after setup and just before the poll's first read, so a poll whose setup
// fails stamps nothing and leaves every wake recorded before it pending for the
// next execution, while a wake recorded before the stamp is one the poll's reads
// cover. A stamp that cannot be written is logged and the poll goes ahead.
func gatedPoll(ctx context.Context, logger *slog.Logger, interval settingReader, clock pollClock, now time.Time, setup pollSetup) error {
	start := decidePoll(ctx, logger, interval, clock, now)
	if !start.Due {
		return nil
	}
	poll, err := setup(ctx)
	if err != nil {
		return err
	}
	stampCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	err = clock.MarkPolled(stampCtx, start.Scheduled)
	cancel()
	if err != nil {
		logger.Warn("pollStartNotRecorded", "err", err.Error())
	}
	return poll(ctx)
}

// runsAtOnceAdopter makes the owner's N the one admission claims under.
type runsAtOnceAdopter interface {
	AdoptRunsAtOnce(ctx context.Context, n int) error
}

// adoptRunsAtOnce reads the owner's N once and makes it the N the poll that
// follows claims under, held to 1..RunsAtOnceMax(platformCap). An N that
// cannot be read, or cannot be adopted, leaves the N the last poll adopted
// rather than guess one, which could raise it past what the owner set.
func adoptRunsAtOnce(ctx context.Context, logger *slog.Logger, reader settingReader, adopter runsAtOnceAdopter, platformCap int) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	n, err := reader.Value(ctx)
	if err != nil {
		logger.Warn("runsAtOnceNotRead", "err", err.Error())
		return
	}
	held := min(max(n, dispatcher.MinRunsAtOnce), dispatcher.RunsAtOnceMax(platformCap))
	if held != n {
		logger.Warn("runsAtOnceOutOfRange", "runs_at_once", n, "held_to", held)
	}
	if err := adopter.AdoptRunsAtOnce(ctx, held); err != nil {
		logger.Warn("runsAtOnceNotAdopted", "err", err.Error(), "runs_at_once", held)
	}
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

// run polls once, unless a scheduled tick comes before the owner's interval has
// passed: the schedule and every wake start the job, and one execution is at
// most one poll.
func run(ctx context.Context, logger *slog.Logger, getenv func(string) string) error {
	started := time.Now()
	logger.Info("dispatcherStarted")
	c, err := loadConfig(getenv)
	if err != nil {
		return err
	}
	fsc, err := firestore.NewClient(ctx, c.project)
	if err != nil {
		return fmt.Errorf("firestore client: %w", err)
	}
	defer func() { _ = fsc.Close() }()
	owner, err := firestore.NewClientWithDatabase(ctx, c.project, store.OwnerDatabase)
	if err != nil {
		return fmt.Errorf("owner database client: %w", err)
	}
	defer func() { _ = owner.Close() }()
	// The setup's clients outlive it, into the poll, so they close as run returns.
	var secretsClient *secretmanager.Client
	defer func() {
		if secretsClient != nil {
			_ = secretsClient.Close()
		}
	}()
	var tokens *linear.PollSource
	defer func() {
		if tokens != nil {
			start := time.Now()
			linear.RevokeAll(ctx, tokens, logger)
			dispatcher.LogPhase(logger, "revoke", start, time.Now())
		}
	}()
	return gatedPoll(ctx, logger, store.NewPollInterval(owner), store.NewPollClock(fsc), started, func(ctx context.Context) (func(context.Context) error, error) {
		var err error
		if secretsClient, err = secretmanager.NewClient(ctx); err != nil {
			return nil, fmt.Errorf("secret manager client: %w", err)
		}
		secrets := dispatcher.NewSecrets(c.project, secretsClient)
		client := &http.Client{Timeout: requestTimeout}
		creds, err := linear.ReadCredentials(ctx, linear.DispatchApp, secrets.Token, client)
		if err != nil {
			return nil, err
		}
		githubToken, err := secrets.Token(ctx, githubTokenSecret)
		if err != nil {
			return nil, err
		}
		tokens = linear.NewPollSource(creds)
		repos, reposErr := ownerRepositories(ctx, owner)
		c.limits.RepoCap = ownerRunsPerRepo(ctx, logger, store.NewRunsPerRepo(owner))
		dispatcher.LogPhase(logger, "setup", started, time.Now())
		gh := dispatcher.GitHub{Token: githubToken, Workflow: runWorkflow, Branch: runBranch, Client: client}
		queue := store.NewQueue(fsc).WithLogger(logger)
		adoptRunsAtOnce(ctx, logger, store.NewRunsAtOnce(owner), queue, c.limits.OrDefault().PlatformCap)
		plans := store.NewPlans(fsc)
		model := runner.DefaultModel()
		others, err := planBudgets(providers.PaidPlans(), model)
		if err != nil {
			return nil, err
		}
		tickets := dispatcher.Linear{Tokens: tokens, Delegate: c.delegate, Client: client}
		return func(ctx context.Context) error {
			_, err := dispatcher.Poll(ctx, dispatcher.Deps{
				Source: tickets,
				// The same client moves a claimed ticket In Progress and puts it back
				// when a requeue or an abandon undoes the claim.
				TicketStates: tickets,
				Queue:        queue,
				Verdicts:     queue,
				Reach:        store.NewReaches(fsc),
				Holds:        queue,
				// The projects the owner may add are read as the agent the poll acts for,
				// and recorded where the surface reads them without a Linear credential.
				LinearProjects: tickets,
				ProjectRecords: store.NewProjectNames(fsc),
				ProjectPhases: dispatcher.ProjectPhaseDeps{
					Projects:  store.NewPhases(owner),
					Snapshots: store.NewPhaseSnapshots(fsc),
					Runs:      queue,
					Issues:    tickets,
					Closures:  queue,
				},
				Estimator:  store.NewEstimates(fsc),
				Visibility: gh,
				Providers:  store.NewProviders(fsc),
				// Admission reads the OpenCode Go plan's live windows with the key the
				// runs bill against, held in Secret Manager rather than the environment.
				GoUsage: dispatcher.GoUsageClient{
					Token:  func(ctx context.Context) (string, error) { return secrets.Token(ctx, goUsageKeySecret) },
					Client: client,
				},
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
				Escapes:      store.NewAutoMergeSwitches(fsc),
				Checks:       dispatcher.ChecksDeps{Runs: store.NewChecks(fsc), GitHub: gh, Branch: runBranch},
				RulesetMarks: store.NewRulesetMarks(fsc),
				Decisions:    queue,
				Elicit:       webhook.Linear{Tokens: tokens},
				Logger:       logger,
				Now:          time.Now,
			}, dispatcher.Config{Repos: repos, ReposErr: reposErr, Budget: budgetConfig(runner.Provider(model)), Model: model, Plans: providers.Plans(), PlanBudgets: others, Limits: c.limits, Tuning: c.tuning, LastResortPlans: lastResortPlans(), TwoStage: c.twoStage, DecisionTimeout: c.decisionTimeout, DecisionMode: c.decisionMode})
			return err
		}, nil
	})
}

// planBudgets is every paid plan after the default model's in order, each with
// its own budget and models, which judge a run already claimed on it. The first
// plan in order must be the default model's, since that is where every run starts.
func planBudgets(order []string, model string) ([]dispatcher.PaidPlan, error) {
	if len(order) == 0 {
		return nil, nil
	}
	if first := runner.Provider(model); order[0] != first {
		return nil, fmt.Errorf("the first paid plan is %s, not %s, the default model's", order[0], first)
	}
	var others []dispatcher.PaidPlan
	for _, plan := range order[1:] {
		m := providers.PlanModels(plan)
		others = append(others, dispatcher.PaidPlan{
			Plan: plan, Budget: budgetConfig(plan),
			Models: runner.ModelLabels{Build: m.Build, Review: m.Review, Plan: m.Plan},
		})
	}
	return others, nil
}

// lastResortPlans is each configured plan's own free models, in the order the
// configuration names them.
func lastResortPlans() [][]string {
	var rungs [][]string
	for _, plan := range providers.LastResortPlans() {
		rungs = append(rungs, providers.LastResortModels(plan))
	}
	return rungs
}
