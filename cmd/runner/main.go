// Command runner executes one run inside a repository's GitHub Actions workflow.
//
//	runner ticket          -run-id <id> -out <path> write the run record's ticket to a file for the model job
//	runner build           -model <provider/model>  fetch the projection, run the agent, bundle the branch, emit a summary
//	runner plan-stage      -ticket-file <path> -out <path>  run the plan phase alone and write its file list for the record job
//	runner record-plan-stage -run-id <id> -plan-file <path>  re-check the plan, record it, settle it and wake the dispatcher
//	runner pr-meta         -run-id <id> ...          render the PR's title and body from the run record
//	runner record          -run-id <id> -summary ... validate the build's summary and merge it into the run record
//	runner enable-provider -provider <name>          admit a halted provider back to dispatch (AC5)
//	runner reset-breaker                             clear the breaker a systemic stop tripped, admitting dispatch again
//	runner peak-mode       [-mode off-peak-only|always] print the peak mode, or set whether a run waits out its model's peak-price hours
//	runner auto-merge      [-mode on|off]            print whether auto-merge is on, or switch it for the next PR pr-meta handles
//	runner autonomy        [-level N]                print the autonomy level and what it allows and recompute the earned-level snapshot, or set the level for the next claim
//	runner requeue         -run-id <id>              return a stopped run to the queue for another attempt
//	runner backfill-queued-at [-dry-run]             stamp queued_at on the queued and planned runs that lack it
//	runner plan-define     -provider <name> ...      record a provider plan's price, billing, limit behaviour, pages and harnesses
//	runner plan-optin      -provider <name>          record the owner's consent to a per-token provider's spend, or with -private to private repos on its free tier, or with -harness to a harness that is not open-source
//	runner plan-verdict    -provider <name> ...      record the verdict on a plan's terms with its wording and source
//	runner plan-reply      -provider <name> ...      record a vendor reply and the verdict it leads to
//	runner plan-list                                 print every plan side by side with its harnesses, their licences and its trailing runs' cached share per phase, flagging the ones to look at
//	runner plans-refresh   [-provider <name>]        read each configured plan's own pages and model list, the leaderboard's scores and the free feed into a dated facts document
//	runner plan-smoke      -plan-models <list>       prove the plan agent refuses edit, a new file and bash on the list's first and last model
//	runner review-smoke    -review-models <list>     try the review agent on a fixture diff with each model, passing those whose findings parse at $0
//	runner pr-review-context -pr <n> -out <path>     write a pull request's diff and the ticket it names, read from Linear
//	runner pr-review       -context <path> ...       review the context on each model in turn and write the verdict
//	runner pr-review-post  -pr <n> -verdict <path>   post the review comment and status, and decide the auto-merge request
//	runner agent-probe     -agent-image <ref> [-egress] -- <command...>  run a command in the agent container, to prove what it cannot reach
//	runner egress-proxy    -allow <host,...>         serve the proxy that lets the agent container reach only those hosts
//
// build exits 0 when it stops short of a branch but reported why; ticket, pr-meta
// and record exit 1 for any run that cannot or did not succeed, so the workflow
// stays red.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/storage"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, logger, os.Args[1:], os.Getenv)
	stop()
	code := exitCode(err)
	var stopped *runner.StopError
	if code != 0 && !errors.As(err, &stopped) && !errors.Is(err, errRunFailed) {
		logger.Error("runnerFailed", "err", err)
	}
	os.Exit(code)
}

// errRunFailed is the result of ticket, plan-stage, record-plan-stage, pr-meta or
// record for a run that cannot or did not succeed.
var errRunFailed = errors.New("run did not succeed")

const maxRebaseConflictRetries = 1

// exitCode is 0 for a build stop whose summary was reported, which the record
// job then judges, and 1 for every other error. A stage that reports no summary
// wraps its stop in errRunFailed, and that wrapper decides the exit: there is
// nothing for the record job to judge, so the run failed and the job must say so.
func exitCode(err error) int {
	var stopped *runner.StopError
	if err == nil || (errors.As(err, &stopped) && !errors.Is(err, runner.ErrSummaryUnreported) && !errors.Is(err, errRunFailed)) {
		return 0
	}
	return 1
}

// env is the workflow context every subcommand reads.
type env struct {
	project   string
	runID     string
	attempt   uint64
	attemptID string
	tempDir   string
	output    string
	secrets   []string
	harnesses []runner.Harness
	identity  runner.Identity
}

// requiredEnv names the variables a subcommand cannot run without. Only the two
// that act on a workflow run itself need its identity and temp directory; the
// owner commands need the project alone, so they run from a terminal with no
// Actions environment.
func requiredEnv(command string) ([]string, bool) {
	switch command {
	case "ticket", "pr-meta", "enable-provider", "reset-breaker", "peak-mode", "auto-merge", "autonomy", "cd", "requeue", "backfill-queued-at",
		"plan-define", "plan-optin", "plan-verdict", "plan-reply", "plan-list", "plans-refresh":
		return []string{"GOOGLE_CLOUD_PROJECT"}, true
	case "record", "record-plan-stage":
		return []string{"GOOGLE_CLOUD_PROJECT", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT"}, true
	case "build", "plan-stage":
		return []string{"GOOGLE_CLOUD_PROJECT", "RUNNER_TEMP", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT"}, true
	}
	return nil, false
}

func loadEnv(getenv func(string) string, required ...string) (env, error) {
	e := env{
		project:  getenv("GOOGLE_CLOUD_PROJECT"),
		tempDir:  getenv("RUNNER_TEMP"),
		output:   getenv("GITHUB_OUTPUT"),
		identity: runner.IdentityFromEnv(getenv),
	}
	e.secrets = runner.HarnessSecrets(getenv)
	e.harnesses = runner.Harnesses(getenv)
	var missing []string
	for _, name := range required {
		if getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return env{}, fmt.Errorf("missing environment: %v", missing)
	}
	attempt := getenv("GITHUB_RUN_ATTEMPT")
	if attempt != "" {
		n, err := parseAttempt(attempt)
		if err != nil {
			return env{}, fmt.Errorf("GITHUB_RUN_ATTEMPT: %w", err)
		}
		e.attempt = n
	}
	e.runID = getenv("GITHUB_RUN_ID")
	e.attemptID = e.runID + "-" + attempt
	return e, nil
}

func run(ctx context.Context, logger *slog.Logger, args []string, getenv func(string) string) error {
	if len(args) == 0 {
		return errors.New("usage: runner ticket|build|plan-stage|record-plan-stage|pr-meta|record|enable-provider|reset-breaker|peak-mode|auto-merge|autonomy|cd|requeue|backfill-queued-at|plan-define|plan-verdict|plan-reply|plan-list|plans-refresh|plan-smoke|review-smoke|pr-review-context|pr-review|pr-review-post|agent-probe|egress-proxy [flags]")
	}
	if args[0] == "plan-smoke" {
		return planSmoke(ctx, logger, getenv, args[1:])
	}
	switch args[0] {
	case "review-smoke":
		return reviewSmoke(ctx, logger, getenv, args[1:])
	case "pr-review-context":
		return prReviewContext(ctx, logger, getenv, args[1:])
	case "pr-review":
		return prReview(ctx, logger, getenv, args[1:])
	case "pr-review-post":
		return prReviewPost(ctx, logger, getenv, args[1:])
	case "agent-probe":
		return agentProbe(ctx, os.Stdout, os.Stderr, args[1:])
	case "egress-proxy":
		return egressProxy(ctx, logger, args[1:])
	}
	required, known := requiredEnv(args[0])
	if !known {
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
	e, err := loadEnv(getenv, required...)
	if err != nil {
		if args[0] == "build" {
			return setupFailed(logger, getenv("GITHUB_OUTPUT"), err)
		}
		return err
	}
	switch args[0] {
	case "ticket":
		return ticket(ctx, logger, e, args[1:])
	case "build":
		return build(ctx, logger, e, args[1:])
	case "plan-stage":
		return planStage(ctx, logger, e, args[1:])
	case "record-plan-stage":
		return recordPlanStage(ctx, logger, e, args[1:])
	case "pr-meta":
		return prMeta(ctx, logger, e, args[1:])
	case "record":
		return record(ctx, logger, getenv, e, args[1:])
	case "enable-provider":
		return enableProvider(ctx, logger, e, args[1:])
	case "reset-breaker":
		return resetBreaker(ctx, logger, e, args[1:])
	case "peak-mode":
		return peakMode(ctx, logger, e, args[1:])
	case "auto-merge":
		return autoMergeSwitch(ctx, logger, e, args[1:])
	case "autonomy":
		return autonomy(ctx, logger, e, args[1:])
	case "cd":
		return cd(ctx, logger, e, args[1:])
	case "requeue":
		return requeue(ctx, logger, e, args[1:])
	case "backfill-queued-at":
		return backfillQueuedAt(ctx, logger, e, args[1:])
	case "plan-define":
		return planDefine(ctx, logger, e, args[1:])
	case "plan-optin":
		return planOptIn(ctx, logger, e, args[1:])
	case "plan-verdict":
		return planVerdict(ctx, logger, e, args[1:])
	case "plan-reply":
		return planReply(ctx, logger, e, args[1:])
	case "plan-list":
		return planList(ctx, logger, e, args[1:])
	case "plans-refresh":
		return plansRefresh(ctx, logger, e, args[1:])
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

// ticket writes the run record's ticket to the file -out names — carrying the
// plan stage's file list when one is recorded, so the build skips planning —
// and fails when the identity does not match, the record is missing or its
// ticket cannot be built.
func ticket(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("ticket", flag.ContinueOnError)
	runID := fs.String("run-id", "", "run record to read the ticket from")
	out := fs.String("out", "", "path to write the run's ticket to")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := e.identity.CheckAccount(); err != nil {
		return err
	}
	fsc, err := recordsClient(ctx, e.project, *runID)
	if err != nil {
		return err
	}
	defer func() { _ = fsc.Close() }()
	records := store.NewRecords(fsc)
	rec, err := runRecord(ctx, logger, records, *runID)
	if err != nil {
		return err
	}
	t := rec.Ticket()
	logger.Info("ticketRead", "run", *runID, "ticket", t.ID)
	if rec.LastResort && rec.LastResortModels.Build == "" {
		return fmt.Errorf("run %s was claimed on a free tier but names no free model", *runID)
	}
	plan, err := records.GetPlan(ctx, *runID)
	if err != nil {
		return err
	}
	t.PlanFiles = plan.Files
	answered, err := store.NewQueue(fsc).Decisions(ctx, *runID)
	if err != nil {
		return err
	}
	t.Decisions = answered
	return writeTicket(*out, e.output, t, rec.RunModels())
}

// writeTicket writes the ticket to path and the models it named to $GITHUB_OUTPUT.
// The ticket never travels as a job output: GitHub drops any output holding a
// value masked in this job, which is how a run reaches the model job empty, so
// only the model labels — never ticket text — are outputs.
func writeTicket(path, output string, t runner.Ticket, m runner.ModelLabels) error {
	if path == "" {
		return errors.New("no -out path to write the ticket to")
	}
	v, err := t.Encode()
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(v), 0o600); err != nil {
		return fmt.Errorf("writing the ticket: %w", err)
	}
	return writeOutputs(output, modelOutputs(m))
}

// modelOutputs are the models the ticket named, each empty when it named none,
// so a run.yml expression falls back to its own default.
func modelOutputs(m runner.ModelLabels) map[string]string {
	return map[string]string{
		"override_model":         m.Build,
		"override_review_models": m.Review,
		"override_plan_models":   strings.Join(m.Plan, ","),
	}
}

// errTicketNotDelivered reports no ticket file where the build expected one: the
// hand-off a masked job output silently dropped, or an artifact that never came.
var errTicketNotDelivered = errors.New("ticket not delivered")

// readTicket reads the ticket the ticket job wrote to path. An absent or empty
// file is errTicketNotDelivered, a hand-off failure distinct from a file that is
// present but does not decode, which is runner.ErrTicketInvalid.
func readTicket(path string) (runner.Ticket, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return runner.Ticket{}, fmt.Errorf("%w: %w", errTicketNotDelivered, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return runner.Ticket{}, fmt.Errorf("%w: %s is empty", errTicketNotDelivered, path)
	}
	return runner.ParseTicket(string(raw))
}

func build(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	model := fs.String("model", "", "model, as provider/model")
	planModels := fs.String("plan-models", runner.DefaultPlanModel(), "the plan phase's models in order, comma-separated provider/model; later ones are backups")
	reviewModels := fs.String("review-models", strings.Join(runner.DefaultReviewModels(), ","), "the review phase's models in order, comma-separated provider/model; later ones are backups (FR-14)")
	pointer := fs.String("pointer", runner.DefaultPointer, "object naming the current rule-stack sha")
	ticketFile := fs.String("ticket-file", "", "path to the run's ticket, as the ticket subcommand wrote it")
	checksImage := fs.String("checks-image", "", "Go image, pinned by digest, to run the pre-PR checks in; required")
	agentImage := fs.String("agent-image", "", agentImageRequiredUsage)
	agentEgress := fs.String("agent-egress", "", agentEgressUsage)
	if err := fs.Parse(args); err != nil {
		return setupFailed(logger, e.output, err)
	}
	if err := requireImages(map[string]string{"agent-image": *agentImage, "checks-image": *checksImage}); err != nil {
		return setupFailed(logger, e.output, err)
	}
	const repo = "."
	checks, packageTests, err := checkRunners(ctx, *checksImage, e.tempDir, repo)
	if err != nil {
		return setupFailed(logger, e.output, err)
	}
	plan := splitModels(*planModels, []string{runner.DefaultPlanModel()})
	review := splitModels(*reviewModels, runner.DefaultReviewModels())
	started, err := withAgents(ctx, logger, agentSettings{Image: *agentImage, Egress: *agentEgress, Model: *model, Plan: plan, Review: review},
		repo, e.harnesses, func(contained []runner.Harness, probe func(context.Context, string) error) error {
			e.harnesses = contained
			if err := writeOutputs(e.output, map[string]string{"attempt_id": e.attemptID}); err != nil {
				return setupFailed(logger, e.output, err)
			}
			t, err := readTicket(*ticketFile)
			switch {
			case errors.Is(err, errTicketNotDelivered):
				return ticketNotDelivered(logger, e.output, err)
			case err != nil:
				logger.Warn("ticketRejected", "err", err.Error())
			}

			gcs, err := storage.NewClient(ctx)
			if err != nil {
				return setupFailed(logger, e.output, fmt.Errorf("storage client: %w", err))
			}
			defer func() { _ = gcs.Close() }()

			logger = logger.With("attempt", e.attemptID, "ticket", t.ID)
			// These routers are given no harness opt-in: a run job holds no plan
			// record, so it runs the plan's next harness rather than one that is
			// not open-source, which only the owner's own commands admit.
			res, err := runner.Build(ctx, runner.BuildDeps{
				Projections:  store.NewBucket(gcs, e.project+"-projections"),
				Completions:  store.NewBucket(gcs, e.project+"-completions"),
				Agent:        runner.NewRouter(runner.ProfileBuild, e.harnesses...),
				PlanAgent:    runner.NewRouter(runner.ProfilePlan, e.harnesses...),
				ReviewAgent:  runner.NewRouter(runner.ProfileReview, e.harnesses...),
				Checks:       checks,
				PackageTests: packageTests,
				AgentProbe:   probe,
				Report:       func(s runner.Summary) error { return writeSummary(e.output, s) },
				Logger:       logger,
				Now:          time.Now,
			}, runner.BuildConfig{
				AttemptID:    e.attemptID,
				Repo:         repo,
				TempDir:      e.tempDir,
				Pointer:      *pointer,
				Model:        *model,
				PlanModels:   plan,
				ReviewModels: review,
				Secrets:      e.secrets,
				Identity:     e.identity,
				Ticket:       t,
			})
			if err != nil {
				return err
			}
			if err := writeOutputs(e.output, map[string]string{
				"branch":  res.Branch,
				"changed": strconv.FormatBool(res.Changed),
			}); err != nil {
				return err
			}
			return writeMultilineOutput(e.output, "loop_detail", res.LoopDetail)
		})
	if !started {
		return setupFailed(logger, e.output, err)
	}
	return err
}

// checkRunners are the pre-PR loop's checks: the repository's own gates, and one
// package's tests, which the guard probe breaks a changed guard against. Both
// run in a container of image when one is given, with the module cache under
// tempDir and repo's git common directory resolved now, before any agent runs;
// else on the host.
func checkRunners(ctx context.Context, image, tempDir, repo string) (runner.CheckRunner, runner.PackageTestRunner, error) {
	if image == "" {
		return runner.RunChecks, runner.RunPackageTest, nil
	}
	linter, err := exec.LookPath("golangci-lint")
	if err != nil {
		return nil, nil, fmt.Errorf("checks linter: %w", err)
	}
	if linter, err = filepath.Abs(linter); err != nil {
		return nil, nil, fmt.Errorf("checks linter: %w", err)
	}
	// The checks image pins a Go toolchain alone, so the web gates run on the
	// bun model.yml installs for its harnesses, mounted into their container.
	bun, err := exec.LookPath("bun")
	if err != nil {
		return nil, nil, fmt.Errorf("checks bun: %w", err)
	}
	if bun, err = filepath.Abs(bun); err != nil {
		return nil, nil, fmt.Errorf("checks bun: %w", err)
	}
	gitDir, err := runner.GitCommonDir(ctx, repo)
	if err != nil {
		return nil, nil, fmt.Errorf("checks git common dir: %w", err)
	}
	c := runner.ContainerChecks{Image: image, ModCache: filepath.Join(tempDir, "checks-modcache"), Linter: linter,
		Bun: bun, BunCache: filepath.Join(tempDir, "checks-bun-cache"), GitCommonDir: gitDir}
	if err := c.Validate(); err != nil {
		return nil, nil, err
	}
	return c.Run, c.RunPackageTest, nil
}

// agentImageUsage describes the -agent-image flag of the subcommands that may run an agent on the host;
// build and plan-stage use agentImageRequiredUsage.
const agentImageUsage = "agent image, pinned by digest, to run the agent CLIs in; empty runs them on the host"

// agentImageRequiredUsage describes the -agent-image flag of build and plan-stage, whose output the trusted jobs read.
const agentImageRequiredUsage = "agent image, pinned by digest, to run the agent CLIs in; required"

// requireImages refuses a stage whose agent would run on the runner, where it
// has a shell and could write the outputs the trusted jobs read. images maps
// each flag name to its value.
func requireImages(images map[string]string) error {
	var missing []string
	for name, image := range images {
		if image == "" {
			missing = append(missing, "-"+name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	slices.Sort(missing)
	return fmt.Errorf("%s required: the agent and its checks run in containers, never on the runner", strings.Join(missing, ", "))
}

// agentEgressUsage describes the -agent-egress flag of build and plan-stage.
const agentEgressUsage = "on confines the agent container to its models' API hosts through a proxy; empty or off leaves it on the default network"

// agentSettings is how a stage runs its agents: in a container of Image when it
// is not empty, and with Egress "on" reachable only to the API hosts of the
// plans Models name.
type agentSettings struct {
	Image  string
	Egress string
	// Model is the build model, empty for a stage that has none; Plan and Review
	// are the plan and review phases' model lists.
	Model  string
	Plan   []string
	Review []string
}

// models is every model the stage's agents may call.
func (s agentSettings) models() []string {
	var m []string
	if s.Model != "" {
		m = append(m, s.Model)
	}
	return slices.Concat(m, s.Plan, s.Review)
}

// agentRunner is the harnesses a stage runs agents through, in a container of
// s.Image when one is given, with repo's git common directory resolved now,
// before any agent runs, and the probe that proves the image runs every one of
// them; else the host harnesses unchanged and no probe. The returned stop
// removes the egress network and proxy, and is never nil.
func agentRunner(ctx context.Context, s agentSettings, repo string, harnesses []runner.Harness) ([]runner.Harness, func(context.Context, string) error, func(context.Context) error, error) {
	noStop := func(context.Context) error { return nil }
	if s.Egress != "" && s.Egress != "on" && s.Egress != "off" {
		return nil, nil, nil, fmt.Errorf("-agent-egress %q is not on or off", s.Egress)
	}
	if s.Image == "" {
		if s.Egress == "on" {
			return nil, nil, nil, errors.New("-agent-egress on needs -agent-image")
		}
		return harnesses, nil, noStop, nil
	}
	gitDir, err := runner.GitCommonDir(ctx, repo)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("agent git common dir: %w", err)
	}
	c := runner.AgentContainer{Image: s.Image, GitCommonDir: gitDir}
	if err := c.Validate(); err != nil {
		return nil, nil, nil, err
	}
	stop := noStop
	if s.Egress == "on" {
		if c, stop, err = startEgress(ctx, c, s.models(), os.Stderr); err != nil {
			return nil, nil, nil, err
		}
	}
	contained, probe, err := runner.WithAgentContainer(harnesses, c)
	if err != nil {
		return nil, nil, nil, errors.Join(err, stop(ctx))
	}
	return contained, probe, stop, nil
}

// startEgress confines c to the API hosts of the plans models name, through
// this binary run as the proxy.
func startEgress(ctx context.Context, c runner.AgentContainer, models []string, logs io.Writer) (runner.AgentContainer, func(context.Context) error, error) {
	hosts, err := runner.EgressHosts(models)
	if err != nil {
		return runner.AgentContainer{}, nil, err
	}
	self, err := os.Executable()
	if err != nil {
		return runner.AgentContainer{}, nil, fmt.Errorf("egress proxy binary: %w", err)
	}
	return runner.StartEgress(ctx, c, self, hosts, logs)
}

// withAgents runs stage with the harnesses contained as s says, and removes the
// egress network and proxy after it returns, however it ends. started is false
// when the agents could not be set up, so stage never ran.
func withAgents(ctx context.Context, logger *slog.Logger, s agentSettings, repo string, harnesses []runner.Harness,
	stage func(contained []runner.Harness, probe func(context.Context, string) error) error) (started bool, err error) {
	contained, probe, stop, err := agentRunner(ctx, s, repo, harnesses)
	if err != nil {
		return false, err
	}
	defer func() {
		if err := stop(ctx); err != nil {
			logger.Warn("agentEgressStopFailed", "err", err.Error())
		}
	}()
	return true, stage(contained, probe)
}

// splitModels reads a comma-separated model list, using a copy of fallback when
// the value is blank — what a cleared workflow input passes. A blank entry inside a list
// is kept so the validator reports it.
func splitModels(s string, fallback []string) []string {
	if strings.TrimSpace(s) == "" {
		return slices.Clone(fallback)
	}
	parts := strings.Split(s, ",")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}

// buildAttempt is id when it names an attempt of this run, and the current
// attempt otherwise.
func buildAttempt(logger *slog.Logger, id string, e env) string {
	if id == "" {
		return e.attemptID
	}
	if !ownAttemptID(id, e.runID, e.attempt) {
		logger.Warn("attemptIDRejected", "length", len(id))
		return e.attemptID
	}
	return id
}

// runRecord reads run runID's record, failing the run when the record is
// missing or its ticket cannot be built.
func runRecord(ctx context.Context, logger *slog.Logger, r runner.RecordReader, runID string) (runner.Record, error) {
	rec, err := runner.ReadRun(ctx, r, runID)
	var stopped *runner.StopError
	if errors.As(err, &stopped) {
		logger.Error("ticketUnavailable", "run", runID, "reason", string(stopped.Reason),
			"err", stopped.Err.Error())
		return runner.Record{}, fmt.Errorf("%w: %s", errRunFailed, stopped.Reason)
	}
	if err != nil {
		return runner.Record{}, err
	}
	return rec, nil
}

func record(ctx context.Context, logger *slog.Logger, getenv func(string) string, e env, args []string) error {
	fs := flag.NewFlagSet("record", flag.ContinueOnError)
	runID := fs.String("run-id", "", "run record to finalize")
	attemptID := fs.String("attempt-id", e.attemptID, "workflow attempt the build ran in")
	summary := fs.String("summary", "", "the build's summary, as JSON")
	unreadable := fs.Bool("summary-unreadable", false, "the summary could not be passed in")
	prURL := fs.String("pr-url", "", "URL of the opened PR")
	runResult := fs.String("run-result", "", "result of the run job")
	prResult := fs.String("pr-result", "", "result of the pr job")
	prStopReason := fs.String("pr-stop-reason", "", "the pr job's stop_reason output, empty when it pushed")
	prSeconds := fs.Int("pr-duration-s", 0, "wall-clock seconds the pr job took")
	checkReport := fs.String("failed-gate", "", "the check job's failed_gate output, empty when it did not run")
	runURL := fs.String("run-url", "", "URL of the workflow run")
	autoMerge := fs.Bool("auto-merge", false, "the pr job marked the PR eligible for auto-merge")
	claimAttempt := fs.String("attempt", "", "attempt the dispatcher claimed the run at, empty for a run dispatched without one")
	dispatcherURI := fs.String("dispatcher-uri", "", "the dispatcher job's run endpoint, woken once the record is written; empty skips the wake")
	if err := fs.Parse(args); err != nil {
		return err
	}
	attempt, err := parseClaimAttempt(*claimAttempt)
	if err != nil {
		return err
	}
	// started tells run.yml's fallback that the summary arrived, so a later failure
	// is not mistaken for one too large to pass in.
	if err := writeOutputs(e.output, map[string]string{"started": "true"}); err != nil {
		return err
	}
	*attemptID = buildAttempt(logger, *attemptID, e)

	fsc, err := recordsClient(ctx, e.project, *runID)
	if err != nil {
		return err
	}
	defer func() { _ = fsc.Close() }()

	records := store.NewRecords(fsc)
	queue := store.NewQueue(fsc)
	rec, err := finalize(ctx, logger, records, queue, records, store.NewNotices(fsc), store.NewBreaker(fsc), runner.FinalizeInput{
		RunID:             *runID,
		Identity:          e.identity,
		AttemptID:         *attemptID,
		Summary:           *summary,
		SummaryUnreadable: *unreadable,
		PRURL:             *prURL,
		RunResult:         *runResult,
		PRResult:          *prResult,
		PRStopReason:      *prStopReason,
		PRDuration:        time.Duration(*prSeconds) * time.Second,
		CheckReport:       *checkReport,
		FailedJobLog:      failedJobLogForRun(ctx, logger, getenv),
		RunURL:            *runURL,
		AutoMerge:         *autoMerge,
		Attempt:           attempt,
	}, time.Now())
	if err != nil {
		return err
	}
	defer wakeAfterRecord(ctx, logger, googleClient, *dispatcherURI, *runID)
	if rec.StopReason == runner.StopSummaryInvalid {
		logger.Warn("summaryRejected", "run", *runID, "err", rec.StopDetail)
	}
	logger.Info("recordFinalized", "run", *runID, "attempt", *attemptID, "outcome", string(rec.Outcome),
		"reason", string(rec.StopReason), "failedGate", rec.FailedGate,
		"timeOfUse", rec.TimeOfUse, "costDrift", rec.CostDrift)
	if rec.CostDriftUnread {
		logger.Warn("costDriftUnread", "run", *runID, "provider", rec.Plan)
	}
	if rec.Outcome == runner.OutcomeInfraFailure && rec.StopReason == runner.StopModelUnavailable && len(rec.Steps) > 0 {
		provider := runner.Provider(rec.Steps[len(rec.Steps)-1].Model)
		if perr := store.NewProviders(fsc).RecordInfraStop(ctx, provider, time.Now()); perr != nil {
			logger.Error("providerInfraStopNotRecorded", "run", *runID, "provider", provider, "err", perr.Error())
		}
	}
	if rec.Succeeded() && len(rec.Steps) > 0 {
		provider := runner.Provider(rec.Steps[len(rec.Steps)-1].Model)
		if perr := store.NewProviders(fsc).RecordSuccess(ctx, provider); perr != nil {
			logger.Error("providerSuccessNotRecorded", "run", *runID, "provider", provider, "err", perr.Error())
		}
	}
	if rec.StopReason == runner.StopRebaseConflict {
		res, err := queue.RequeueAfterConflict(ctx, *runID, time.Now(), maxRebaseConflictRetries)
		switch {
		case err == nil:
			logger.Info("rebaseConflictRequeued", "run", *runID, "attempt", res.Attempt)
			restoreTicketState(ctx, logger, e.project, *runID, res.TicketState)
			return nil
		case errors.Is(err, store.ErrRetriesExhausted):
			logger.Warn("rebaseConflictRetriesExhausted", "run", *runID, "retries", maxRebaseConflictRetries)
		case errors.Is(err, store.ErrRunAbandoned):
			logger.Warn("rebaseConflictOnAbandonedRun", "run", *runID)
		default:
			return err
		}
	}
	if !rec.Succeeded() {
		return errRunFailed
	}
	return nil
}

// enableProvider clears a halted provider's stop count, admitting it to
// dispatch again (AC5) — a manual operator action, never automatic.
func enableProvider(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("enable-provider", flag.ContinueOnError)
	provider := fs.String("provider", "", "provider to admit again")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := e.identity.CheckAccount(); err != nil {
		return err
	}
	if !providerPattern.MatchString(*provider) {
		return fmt.Errorf("provider %q is not a provider name", *provider)
	}
	if e.project == "" {
		return errors.New("GOOGLE_CLOUD_PROJECT is not set")
	}
	fsc, err := firestore.NewClient(ctx, e.project)
	if err != nil {
		return fmt.Errorf("firestore client: %w", err)
	}
	defer func() { _ = fsc.Close() }()
	if err := store.NewProviders(fsc).ClearHalt(ctx, *provider); err != nil {
		return err
	}
	logger.Info("providerEnabled", "provider", *provider)
	return nil
}

// noticeRaiser and breakerTripper are what record writes for a systemic stop.
type noticeRaiser interface {
	Raise(ctx context.Context, n dispatcher.Notice, at time.Time) error
}

type breakerTripper interface {
	Trip(ctx context.Context, runID, class string, at time.Time) error
}

// finalize writes the run's outcome into its record and raises its systemic
// notice, even when the record or ledger write then fails, since the stop is
// known by then.
func finalize(ctx context.Context, logger *slog.Logger, records runner.RecordStore, ledger runner.Ledger,
	reader runner.ProviderCostReader, notices noticeRaiser, breaker breakerTripper, in runner.FinalizeInput, at time.Time) (runner.Record, error) {
	rec, err := runner.Finalize(ctx, records, ledger, reader, in, at)
	raiseSystemic(ctx, logger, notices, breaker, rec, in.AttemptID, at)
	return rec, err
}

// attemptIDPattern is what a workflow attempt id looks like: run, then attempt.
var attemptIDPattern = regexp.MustCompile(`^[0-9]{1,20}-[0-9]{1,10}$`)

// raiseSystemic raises the notice for a systemic stop, linked to the run that
// recorded it, and trips the breaker when its class does. The notice is keyed
// by run and attempt, so a later attempt after a reset raises its own; an
// attempt id that cannot name a document falls back to the run alone. Both
// writes create only what is absent, so a record job run twice raises and
// trips once.
func raiseSystemic(ctx context.Context, logger *slog.Logger, notices noticeRaiser, breaker breakerTripper, rec runner.Record, attemptID string, at time.Time) {
	notify, trips := rec.Systemic()
	if !notify {
		return
	}
	class := string(rec.StopReason)
	id := rec.RunID + "-" + attemptID
	if !attemptIDPattern.MatchString(attemptID) {
		logger.Error("noticeAttemptInvalid", "run", rec.RunID, "length", len(attemptID))
		id = rec.RunID
	}
	if err := notices.Raise(ctx, dispatcher.Notice{ID: id, Class: class, Link: rec.RunURL}, at); err != nil {
		logger.Error("noticeNotRaised", "run", rec.RunID, "class", class, "err", err.Error())
	}
	if !trips {
		return
	}
	if err := breaker.Trip(ctx, rec.RunID, class, at); err != nil {
		logger.Error("breakerNotTripped", "run", rec.RunID, "class", class, "err", err.Error())
		return
	}
	logger.Warn("breakerTripped", "run", rec.RunID, "class", class)
}

// resetBreaker clears the breaker a systemic stop tripped, admitting dispatch
// again — a manual operator action, never automatic. Its account check only
// guards against a run on the wrong account by mistake: Firestore IAM decides
// who may clear the breaker.
func resetBreaker(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("reset-breaker", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := e.identity.CheckAccount(); err != nil {
		return err
	}
	if e.project == "" {
		return errors.New("GOOGLE_CLOUD_PROJECT is not set")
	}
	fsc, err := firestore.NewClient(ctx, e.project)
	if err != nil {
		return fmt.Errorf("firestore client: %w", err)
	}
	defer func() { _ = fsc.Close() }()
	if err := store.NewBreaker(fsc).Reset(ctx); err != nil {
		return err
	}
	logger.Info("breakerReset")
	return nil
}

// requeue returns a run that has ended to the queue, so its ticket is attempted
// again — a manual operator action, never automatic. Its account check only
// guards against a run on the wrong account by mistake: Firestore IAM decides
// who may requeue.
func requeue(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("requeue", flag.ContinueOnError)
	runID := fs.String("run-id", "", "run to return to the queue")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := e.identity.CheckAccount(); err != nil {
		return err
	}
	fsc, err := recordsClient(ctx, e.project, *runID)
	if err != nil {
		return err
	}
	defer func() { _ = fsc.Close() }()
	res, err := store.NewQueue(fsc).Requeue(ctx, *runID, time.Now())
	if err != nil {
		return err
	}
	logger.Info("runRequeued", "run", *runID, "attempt", res.Attempt, "state", res.State)
	restoreTicketState(ctx, logger, e.project, *runID, res.TicketState)
	return nil
}

// backfillQueuedAt stamps queued_at on the queued and planned runs written
// before the field existed — a manual operator action, safe to repeat. Its
// account check only guards against a run on the wrong account by mistake:
// Firestore IAM decides who may write.
func backfillQueuedAt(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("backfill-queued-at", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "count the runs it would stamp and write nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := e.identity.CheckAccount(); err != nil {
		return err
	}
	if e.project == "" {
		return errors.New("GOOGLE_CLOUD_PROJECT is not set")
	}
	fsc, err := firestore.NewClient(ctx, e.project)
	if err != nil {
		return fmt.Errorf("firestore client: %w", err)
	}
	defer func() { _ = fsc.Close() }()
	res, err := store.NewQueue(fsc).BackfillQueuedAt(ctx, *dryRun)
	if err != nil {
		return err
	}
	logger.Info("queuedAtBackfilled", "stamped", res.Stamped, "skipped", res.Skipped, "dryRun", *dryRun)
	return nil
}

// providerPattern is what runner.Provider extracts from a model string: the
// same character class modelPattern requires of a model's provider segment.
var providerPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// recordsClient opens Firestore for a subcommand that reads or writes record runID.
func recordsClient(ctx context.Context, project, runID string) (*firestore.Client, error) {
	if !runner.ValidRunID(runID) {
		return nil, fmt.Errorf("run id %q cannot name a record", runID)
	}
	if project == "" {
		return nil, errors.New("GOOGLE_CLOUD_PROJECT is not set")
	}
	fsc, err := firestore.NewClient(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("firestore client: %w", err)
	}
	return fsc, nil
}

// setupFailed reports a build that could not start, so the record says why.
func setupFailed(logger *slog.Logger, output string, err error) error {
	return reportStop(logger, output, "setupFailed", runner.SetupSummary(err, time.Now()), err)
}

// ticketNotDelivered reports a build the ticket job handed no ticket, so the
// record blames the hand-off rather than the ticket.
func ticketNotDelivered(logger *slog.Logger, output string, err error) error {
	return reportStop(logger, output, "ticketNotDelivered",
		runner.StoppedSummary(runner.StopTicketNotDelivered, err, time.Now()), err)
}

// reportStop writes sum to $GITHUB_OUTPUT and returns the StopError it reports.
// The returned StopError exits 0 only when that report was written.
func reportStop(logger *slog.Logger, output, event string, sum runner.Summary, err error) error {
	logger.Error(event, "err", err)
	stopped := &runner.StopError{Outcome: sum.Outcome, Reason: sum.StopReason, Err: err}
	if output == "" {
		return errors.Join(stopped, runner.ErrSummaryUnreported)
	}
	if werr := writeSummary(output, sum); werr != nil {
		return errors.Join(stopped, fmt.Errorf("%w: %w", runner.ErrSummaryUnreported, werr))
	}
	return stopped
}

// ownAttemptID reports whether id names an attempt of this workflow run up to the current one.
func ownAttemptID(id, runID string, current uint64) bool {
	attempt, ok := strings.CutPrefix(id, runID+"-")
	if !ok {
		return false
	}
	n, err := parseAttempt(attempt)
	return err == nil && n <= current
}

// parseAttempt reads a run attempt: a positive decimal with no sign or leading zero.
func parseAttempt(s string) (uint64, error) {
	if s == "" || s[0] < '1' || s[0] > '9' {
		return 0, fmt.Errorf("attempt %q is not a positive number", s)
	}
	return strconv.ParseUint(s, 10, 32)
}

// parseClaimAttempt reads the attempt a run was claimed at, which the workflow
// passes through empty for a run dispatched before it carried one: nil.
func parseClaimAttempt(s string) (*int, error) {
	if s == "" {
		return nil, nil
	}
	n, err := strconv.ParseUint(s, 10, 31)
	if err != nil {
		return nil, fmt.Errorf("-attempt %q is not a claim attempt: %w", s, err)
	}
	attempt := int(n)
	return &attempt, nil
}

// errNoOutput is a summary with nowhere to go: outside Actions nothing reports it.
var errNoOutput = errors.New("no GITHUB_OUTPUT to report the summary to")

func writeSummary(path string, s runner.Summary) error {
	if path == "" {
		return errNoOutput
	}
	v, err := s.Encode()
	if err != nil {
		return err
	}
	return writeOutputs(path, map[string]string{"summary": v})
}

// writeOutputs appends single-line step outputs to $GITHUB_OUTPUT, or does nothing
// outside Actions.
func writeOutputs(path string, kv map[string]string) error {
	for k, v := range kv {
		if strings.ContainsAny(k+v, "\r\n") {
			return fmt.Errorf("output %s spans lines", k)
		}
	}
	if path == "" {
		return nil
	}
	var out strings.Builder
	for k, v := range kv {
		fmt.Fprintf(&out, "%s=%s\n", k, v)
	}
	return appendFile(path, out.String())
}

// writeMultilineOutput appends one step output that may span lines to
// $GITHUB_OUTPUT, between random delimiters the value does not contain.
func writeMultilineOutput(path, key, value string) error {
	if path == "" {
		return nil
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	delim := "EOF_" + hex.EncodeToString(b)
	if strings.Contains(value, delim) {
		return fmt.Errorf("output %s contains its delimiter", key)
	}
	return appendFile(path, fmt.Sprintf("%s<<%s\n%s\n%s\n", key, delim, value, delim))
}

// appendFile appends content to the file at path, creating it owner-only if absent.
func appendFile(path, content string) (err error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if _, err := f.WriteString(content); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
