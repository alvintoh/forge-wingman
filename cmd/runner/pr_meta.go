package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// workRepo is where the workflow's own steps run, and the repository the pr-meta
// job checked out: the bundle is fetched into it.
const workRepo = "."

// mainRef is the ref the branch's changed files are measured against. It is the
// main the job checked out, whose history holds every commit a run is based on —
// which is why the checkout fetches it whole rather than its tip alone.
const mainRef = "origin/main"

// prMeta writes the PR's title, body, the branch segment the pushed branch must
// carry, whether it opens as a draft and whether to mark it eligible for
// auto-merge as outputs, from the run record's ticket, the build's summary and
// the branch's bundle.
//
// The write set is this job's own: the files the branch changes, read out of the
// bundle the model job uploaded, measured against the plan the run record
// carries. The model job's own out-of-plan list decides nothing here, since it
// is the claim under test.
func prMeta(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("pr-meta", flag.ContinueOnError)
	runID := fs.String("run-id", "", "run record to read the ticket from")
	attemptID := fs.String("attempt-id", e.attemptID, "workflow attempt the build ran in")
	summary := fs.String("summary", "", "the build's summary, as JSON")
	checkReport := fs.String("failed-gate", "", "the check job's failed_gate output")
	runURL := fs.String("run-url", "", "URL of the workflow run")
	loopDetail := fs.String("loop-detail", "", "the pre-PR loop's report of why the PR is a draft (FR-5)")
	template := fs.String("template", runner.DefaultPRTemplate, "pull request template to render")
	autoMergeSwitch := fs.String("auto-merge-switch", "", "the WINGMAN_AUTO_MERGE variable; auto-merge is off unless it is \"on\"")
	bundle := fs.String("bundle", "", "path to the bundle the model job uploaded")
	branch := fs.String("branch", "", "the branch the model job bundled")
	repository := fs.String("repository", "", "the repository the run builds in, as owner/name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := e.identity.CheckAccount(); err != nil {
		return err
	}
	tmpl, err := os.ReadFile(*template)
	if err != nil {
		return fmt.Errorf("reading the PR template: %w", err)
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
	plan, err := records.GetPlan(ctx, *runID)
	if err != nil {
		return err
	}
	planned, err := store.NewQueue(fsc).Planned(ctx)
	if err != nil {
		return err
	}
	sum := buildSummary(logger, *summary, buildAttempt(logger, *attemptID, e), rec.Ticket(), time.Now())
	if err := refuseWorkflowPush(logger, sum); err != nil {
		return err
	}
	changed, err := runner.Bundle{Path: *bundle, Repo: workRepo, Branch: *branch, Base: mainRef,
		RunID: e.runID}.ChangedFiles(ctx)
	if err != nil {
		logger.Warn("changedFilesUnread", "run", *runID, "err", err.Error())
	}
	check := prCheck{
		Changed:   changed,
		Unread:    err != nil,
		OutOfPlan: outOfPlan(plan, changed),
		Overlap:   planOverlap(rec.Ticket().Size, *repository, changed, planned),
	}
	title, body, err := renderPR(string(tmpl), rec, sum, runner.FailedGate(*checkReport), *runURL, *loopDetail, check.Overlap, check.OutOfPlan)
	if err != nil {
		return err
	}
	draft := draftPR(sum, check)
	merge := autoMerge(*autoMergeSwitch, *runID, rec.Ticket(), sum, runner.FailedGate(*checkReport), check)
	logger.Info("autoMergeDecided", "run", *runID, "autoMerge", merge, "sampled", runner.Sampled(*runID),
		"draft", draft, "changed", len(check.Changed), "outOfPlan", len(check.OutOfPlan), "overlap", check.Overlap)
	if err := writeOutputs(e.output, map[string]string{
		"title":          title,
		"branch_segment": rec.Ticket().BranchSegment(),
		"auto_merge":     strconv.FormatBool(merge),
		"draft":          strconv.FormatBool(draft),
	}); err != nil {
		return err
	}
	return writeMultilineOutput(e.output, "body", body)
}

// prCheck is what pr-meta establishes about a run's write set beyond what its
// summary claims. The model job's own out-of-plan list is not one of its
// inputs, since that is the claim under test.
type prCheck struct {
	// Changed is the write set the branch carries, read out of its bundle.
	Changed []string
	// OutOfPlan is what Changed holds that the run's recorded plan does not
	// name, empty for a run no plan stage recorded.
	OutOfPlan []string
	// Overlap names the in-flight run whose planned write set Changed touches,
	// or "" when none does.
	Overlap string
	// Unread is set when the branch's changes could not be read, which leaves
	// the write set unknown rather than empty.
	Unread bool
}

// clear reports whether nothing about the run's write set holds the PR back.
func (c prCheck) clear() bool {
	return !c.Unread && len(c.OutOfPlan) == 0 && c.Overlap == ""
}

// outOfPlan is what the branch's changed files hold that the plan the run
// record carries does not name. A run no plan stage recorded — an S ticket, and
// every run until the plan stage is switched on — has no trusted list to be
// outside of, so nothing is out of plan.
func outOfPlan(plan runner.PlanRecord, changed []string) []string {
	if len(plan.Files) == 0 {
		return nil
	}
	return runner.OutOfPlanFiles(changed, plan.Files)
}

// planOverlap names the in-flight run whose planned write set the branch's
// changed files touch, or "" when none does.
//
// Only an S run asks. An M or L run was admitted against every write set in
// flight, so its files cannot collide with another run's; an S run carries no
// write set to be admitted against, and a run that planned after it started was
// admitted against a repository that looked free — the one window admission
// cannot cover.
func planOverlap(size, repository string, changed []string, flight []dispatcher.InFlight) string {
	if size != "S" || len(changed) == 0 {
		return ""
	}
	for _, run := range flight {
		// The rule names a concurrent M or L run, and only a run with a
		// recorded plan has a write set it could be named for: an S ticket is
		// never planned, and an unplanned run's set is unknown rather than
		// empty.
		if run.Repo != repository || run.Size == "S" || !writeSetsTouch(run.Files, changed) {
			continue
		}
		return run.Ticket
	}
	return ""
}

// writeSetsTouch reports whether a planned write set and the changed files name
// a common path.
func writeSetsTouch(planned, changed []string) bool {
	if len(planned) == 0 {
		return false
	}
	return slices.ContainsFunc(planned, func(name string) bool { return slices.Contains(changed, name) })
}

// draftPR reports whether the PR opens as a draft (FR-5): the pre-PR loop found
// something open, or the run's write set is not clear — an edit outside the
// plan, a concurrent run's plan these files touch, or a write set that could not
// be read.
func draftPR(sum runner.Summary, check prCheck) bool {
	return !sum.Ready || !check.clear()
}

// ownerAttention reports whether the run's PR must wait for the owner rather
// than merge itself (FR-5, FR-17): a draft, sized L, in the review sample,
// touching a workflow, or failing a check gate.
func ownerAttention(runID string, t runner.Ticket, sum runner.Summary, failedGate string, check prCheck) bool {
	return draftPR(sum, check) || (t.Size != "S" && t.Size != "M") || runner.Sampled(runID) ||
		sum.StopReason == runner.StopWorkflowChange || failedGate != ""
}

// autoMergeOn is the only WINGMAN_AUTO_MERGE value that lets a run request auto-merge.
const autoMergeOn = "on"

// autoMerge reports whether the run's PR is marked eligible for auto-merge,
// which pr-review.yml requests once its review is clean: the switch is on and
// nothing in the run needs the owner.
func autoMerge(switchValue, runID string, t runner.Ticket, sum runner.Summary, failedGate string, check prCheck) bool {
	return switchValue == autoMergeOn && !ownerAttention(runID, t, sum, failedGate, check)
}

// refuseWorkflowPush fails a build that stopped for touching a workflow file,
// so the pr job, which could not push it, never runs.
func refuseWorkflowPush(logger *slog.Logger, sum runner.Summary) error {
	if sum.StopReason != runner.StopWorkflowChange {
		return nil
	}
	logger.Warn("pushRefused", "reason", string(sum.StopReason), "detail", sum.StopDetail)
	return errRunFailed
}

// buildSummary is raw parsed as attemptID's build of t, or the zero Summary when
// raw is empty or rejected, so the PR falls back to the ticket rather than
// failing to open.
func buildSummary(logger *slog.Logger, raw, attemptID string, t runner.Ticket, now time.Time) runner.Summary {
	if raw == "" {
		return runner.Summary{}
	}
	sum, err := runner.ParseSummary(raw, attemptID, t, now)
	if err != nil {
		logger.Warn("summaryRejected", "err", err.Error())
		return runner.Summary{}
	}
	return sum
}

// renderPR is the PR's title and body for rec's ticket, taken from the build's
// summary: the record gains the build's fields only after the PR opens.
// loopDetail is the pre-PR loop's report of why it is a draft, overlap names the
// in-flight run whose plan these files touch, and outOfPlan the edited files the
// run's plan did not name — both of which also keep it a draft.
func renderPR(tmpl string, rec runner.Record, sum runner.Summary, failedGate, runURL, loopDetail, overlap string, outOfPlan []string) (title, body string, err error) {
	t := rec.Ticket()
	body, err = runner.PRBody(tmpl, t, sum.PRSummary, failedGate, runURL, loopDetail, overlap, outOfPlan)
	if err != nil {
		return "", "", err
	}
	return prTitle(sum, t), body, nil
}

// prTitle is the subject the build committed with, or the ticket's Subject for
// a summary that carries none.
func prTitle(sum runner.Summary, t runner.Ticket) string {
	if sum.CommitSubject != "" {
		return sum.CommitSubject
	}
	return t.Subject()
}
