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
//	runner requeue         -run-id <id>              return a stopped run to the queue for another attempt
//	runner backfill-queued-at [-dry-run]             stamp queued_at on the queued and planned runs that lack it
//	runner project-add     -linear-project <name> -repo <owner/name>  list a project and its repository in the owner database
//	runner project-remove  -repo <owner/name>       unlist a project's repository
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
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/alvintoh/forge-wingman/internal/runner"
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

func run(ctx context.Context, logger *slog.Logger, args []string, getenv func(string) string) error {
	if len(args) == 0 {
		return errors.New("usage: runner ticket|build|plan-stage|record-plan-stage|pr-meta|record|enable-provider|reset-breaker|peak-mode|auto-merge|requeue|backfill-queued-at|project-add|project-remove|plan-define|plan-verdict|plan-reply|plan-list|plans-refresh|plan-smoke|review-smoke|pr-review-context|pr-review|pr-review-post|agent-probe|egress-proxy [flags]")
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
	case "project-add":
		return projectAdd(ctx, logger, e, args[1:])
	case "project-remove":
		return projectRemove(ctx, logger, e, args[1:])
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}
