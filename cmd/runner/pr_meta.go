package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
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
// carries.
func prMeta(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("pr-meta", flag.ContinueOnError)
	runID := fs.String("run-id", "", "run record to read the ticket from")
	attemptID := fs.String("attempt-id", e.attemptID, "workflow attempt the build ran in")
	summary := fs.String("summary", "", "the build's summary, as JSON")
	checkReport := fs.String("failed-gate", "", "the check job's failed_gate output")
	runURL := fs.String("run-url", "", "URL of the workflow run")
	loopDetail := fs.String("loop-detail", "", "the pre-PR loop's report of why the PR is a draft (FR-5)")
	template := fs.String("template", runner.DefaultPRTemplate, "pull request template to render")
	frontEndPaths := fs.String("front-end-paths", "", "the WINGMAN_FRONT_END_PATHS variable: a JSON object of repository to the path prefixes its front-end code lives under")
	bundle := fs.String("bundle", "", "path to the bundle the model job uploaded")
	branch := fs.String("branch", "", "the branch the model job bundled")
	repository := fs.String("repository", "", "the repository the run builds in, as owner/name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// unreadPlanned records that the set of concurrent runs could not be read,
	// which leaves the overlap unknown rather than absent.
	unreadPlanned := false
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
	if err != nil && !errors.Is(err, runner.ErrRecordNotFound) {
		return err
	}
	planned, err := store.NewQueue(fsc).Planned(ctx)
	if err != nil {
		// A read that fails is an unknown set of concurrent runs, not an empty
		// one: the overlap is left unread rather than the run stranded, the
		// way an unreadable bundle is.
		logger.Warn("plannedRunsUnread", "run", *runID, "err", err.Error())
		planned, unreadPlanned = nil, true
	}
	provisional, err := store.NewQueue(fsc).ProvisionalDecisions(ctx, *runID)
	unreadProvisional := err != nil
	if unreadProvisional {
		logger.Warn("provisionalDecisionsUnread", "run", *runID, "err", err.Error())
	}
	superseded, err := store.NewQueue(fsc).SupersededPR(ctx, *runID)
	if err != nil {
		logger.Warn("supersededPRUnread", "run", *runID, "err", err.Error())
	}
	retried, err := store.NewQueue(fsc).OperatorAssisted(ctx, *runID)
	if err != nil {
		logger.Warn("operatorAssistedUnread", "run", *runID, "err", err.Error())
	}
	sum := buildSummary(logger, *summary, buildAttempt(logger, *attemptID, e), rec.Ticket(), time.Now())
	changed, err := runner.Bundle{Path: *bundle, Repo: workRepo, Branch: *branch, Base: mainRef,
		RunID: e.runID}.ChangedFiles(ctx)
	if err != nil {
		logger.Warn("changedFilesUnread", "run", *runID, "err", err.Error())
	}
	check := prCheck{
		Planned:           planRecorded(plan),
		Changed:           changed,
		Unread:            err != nil || unreadPlanned,
		OutOfPlan:         outOfPlan(plan, changed),
		Overlap:           planOverlap(rec.Ticket().Size, *repository, changed, planned),
		FrontEnd:          frontEnd(logger, rec.Ticket().FrontEnd, *repository, *frontEndPaths, changed),
		Provisional:       provisional,
		ProvisionalUnread: unreadProvisional,
		Supersedes:        superseded,
		Retried:           retried,
	}
	// A workflow file in the write set is refused from the write set the
	// branch carries — the run's own App cannot push one — and falls back to
	// the build's stop only where that write set could not be read.
	if wf := runner.WorkflowFiles(check.Changed); len(wf) > 0 {
		return refuseWorkflowPush(logger, sum, wf)
	}
	if check.Unread {
		if err := refuseWorkflowPush(logger, sum, nil); err != nil {
			return err
		}
	}
	title, body, err := renderPR(string(tmpl), rec, sum, runner.FailedGate(*checkReport), *runURL, *loopDetail, check)
	if err != nil {
		return err
	}
	draft := draftPR(sum, check)
	switchOn := autoMergeSwitchOn(ctx, logger, *runID, store.NewAutoMergeSwitches(fsc))
	rule := autoMergeRule(ctx, logger, *runID, store.NewRewrites(fsc), switchOn)
	merge := autoMerge(rule.On, *runID, rec.Ticket(), sum, runner.FailedGate(*checkReport), check)
	if err := records.SetWaitReason(ctx, *runID, waitReason(*runID, rec.Ticket(), sum, check)); err != nil {
		logger.Warn("waitReasonNotRecorded", "run", *runID, "err", err.Error())
	}
	logger.Info("autoMergeDecided", append(ruleAttrs(rule), "run", *runID, "autoMerge", merge, "sampled", runner.Sampled(*runID),
		"draft", draft, "frontEnd", len(check.FrontEnd), "changed", len(check.Changed), "outOfPlan", len(check.OutOfPlan), "overlap", check.Overlap, "planned", check.Planned)...)
	if err := writeOutputs(e.output, map[string]string{
		"title":          title,
		"branch_segment": rec.Ticket().BranchSegment(),
		"auto_merge":     strconv.FormatBool(merge),
		"draft":          strconv.FormatBool(draft),
		"superseded_pr":  supersededOutput(check.Supersedes),
		// superseded_by_retry words the close comment, so it is set only when a PR is superseded.
		"superseded_by_retry": retriedOutput(check.Supersedes, check.Retried),
	}); err != nil {
		return err
	}
	return writeMultilineOutput(e.output, "body", body)
}

// supersededOutput is the step output naming the superseded PR, empty when none.
func supersededOutput(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// retriedOutput is the step output saying the superseded PR was replaced by an
// owner's retry, empty otherwise.
func retriedOutput(superseded int, retried bool) string {
	if superseded > 0 && retried {
		return "true"
	}
	return ""
}

// prCheck is what pr-meta establishes about a run's write set beyond what its
// summary claims.
type prCheck struct {
	// Planned is set when the run's plan recorded a write set; the zero value holds the PR.
	Planned bool
	// Changed is the write set the branch carries, read out of its bundle.
	Changed []string
	// OutOfPlan is what Changed holds that the run's recorded plan does not
	// name, empty for a run no plan stage recorded.
	OutOfPlan []string
	// FrontEnd names what marks the change as front-end work: the ticket's label
	// and each declared path prefix the write set falls under.
	FrontEnd []string
	// Overlap names the in-flight run whose planned write set Changed touches,
	// or "" when none does.
	Overlap string
	// Unread is set when the branch's changes could not be read, which leaves
	// the write set unknown rather than empty.
	Unread bool
	// Provisional are the decisions auto-pilot settled on the run, confirmed or not.
	Provisional []runner.Provisional
	// ProvisionalUnread is set when they could not be read, which leaves them unknown rather than none.
	ProvisionalUnread bool
	// Supersedes is the number of the PR the owner's choice replaced, zero when none.
	Supersedes int
	// Retried is set when the owner replaced it by retrying the run with a note.
	Retried bool
}

// unconfirmed reports whether auto-pilot took an option the owner has not confirmed,
// or the decisions could not be read.
func (c prCheck) unconfirmed() bool {
	return c.ProvisionalUnread || slices.ContainsFunc(c.Provisional, func(p runner.Provisional) bool { return !p.Confirmed })
}

// clear reports whether nothing about the run's write set holds the PR back.
func (c prCheck) clear() bool {
	return !c.Unread && len(c.OutOfPlan) == 0 && c.Overlap == ""
}

// planRecorded reports whether plan names a write set to check the branch against.
func planRecorded(plan runner.PlanRecord) bool { return len(plan.Files) > 0 }

// frontEnd names what marks a run as a front-end change: the ticket's label,
// then each of repository's declared prefixes that changed has a file under. A
// setting that cannot be read marks every change, so a typo holds a PR rather
// than releasing it.
func frontEnd(logger *slog.Logger, labelled bool, repository, setting string, changed []string) []string {
	var marks []string
	if labelled {
		marks = append(marks, runner.FrontEndLabel)
	}
	var byRepo map[string][]string
	if setting != "" {
		if err := json.Unmarshal([]byte(setting), &byRepo); err != nil {
			logger.Warn("frontEndPathsUnreadable", "err", err.Error())
			return append(marks, "WINGMAN_FRONT_END_PATHS")
		}
	}
	for repo, prefixes := range byRepo {
		if !strings.EqualFold(repo, repository) {
			continue
		}
		for _, prefix := range prefixes {
			if underPrefix(changed, prefix) {
				marks = append(marks, prefix)
			}
		}
	}
	return marks
}

// underPrefix reports whether a file in files is prefix or sits under it. A
// prefix that names the whole repository (empty, "." or "/") matches every file,
// and "./web" is "web", so a loosely written prefix holds rather than releases.
func underPrefix(files []string, prefix string) bool {
	dir := strings.Trim(path.Clean("/"+prefix), "/")
	return slices.ContainsFunc(files, func(file string) bool {
		c := strings.TrimPrefix(path.Clean("/"+file), "/")
		return dir == "" || c == dir || strings.HasPrefix(c, dir+"/")
	})
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
// Only an S run asks. An M or L run may share files with another run in flight,
// but its overlap is logged when it is claimed and a conflicting merge requeues
// it; an S run carries no write set for that claim to compare, and a run that
// planned after it started was admitted against a repository that looked free.
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
// touching a workflow, failing a check gate, changing front-end code, or having
// no recorded plan, or holding a provisional decision the owner has not
// confirmed, which hold the PR without making it a draft.
func ownerAttention(runID string, t runner.Ticket, sum runner.Summary, failedGate string, check prCheck) bool {
	return waitReason(runID, t, sum, check) != "" ||
		sum.StopReason == runner.StopWorkflowChange || failedGate != "" || !check.Planned
}

// waitReason is the first reason the run's PR waits for the owner, empty when
// none holds. The two that block a merge come first, so the merge route sees
// them whatever else holds the PR.
func waitReason(runID string, t runner.Ticket, sum runner.Summary, check prCheck) runner.WaitReason {
	switch {
	case check.unconfirmed():
		return runner.WaitUnconfirmedDecision
	case draftPR(sum, check):
		return runner.WaitDraft
	case len(check.FrontEnd) > 0:
		return runner.WaitFrontEnd
	case t.Size != "S" && t.Size != "M":
		return runner.WaitSizeL
	case runner.Sampled(runID):
		return runner.WaitSampled
	}
	return ""
}

// autoMergeSwitchOn reads the owner's switch once. A switch that cannot be read
// holds auto-merge off.
func autoMergeSwitchOn(ctx context.Context, logger *slog.Logger, runID string, s autoMergeSwitchStore) bool {
	on, err := s.Enabled(ctx)
	if err != nil {
		logger.Warn("autoMergeSwitchUnread", "run", runID, "err", err.Error())
		return false
	}
	return on
}

// recentRewrites is the store of measured PRs the auto-merge rule reads.
type recentRewrites interface {
	Recent(ctx context.Context, limit int) ([]dispatcher.Sample, error)
}

// autoMergeRule decides whether the rule is on. A switch that is off returns
// before the evidence is read; evidence that cannot be read holds auto-merge off.
func autoMergeRule(ctx context.Context, logger *slog.Logger, runID string, r recentRewrites, switchOn bool) dispatcher.AutoMergeDecision {
	if !switchOn {
		return dispatcher.AutoMergeDecision{Reason: dispatcher.AutoMergeReasonSwitchOff}
	}
	trailing, err := r.Recent(ctx, dispatcher.TrailingPRs)
	if err != nil {
		logger.Warn("autoMergeEvidenceUnread", "run", runID, "err", err.Error())
		return dispatcher.AutoMergeDecision{Reason: dispatcher.AutoMergeReasonEvidenceUnread}
	}
	return dispatcher.AutoMergeOn(dispatcher.Evidence{Trailing: trailing})
}

// ruleAttrs are the decision's log attributes: the reason always, and the
// numbers only where the decision was made on evidence.
func ruleAttrs(d dispatcher.AutoMergeDecision) []any {
	attrs := []any{"reason", d.Reason}
	if d.Evaluated() {
		attrs = append(attrs, "rate", d.RateText(), "measured", d.PRs, "reverted", d.Reverted)
	}
	return attrs
}

// autoMerge reports whether the run's PR is marked eligible for auto-merge,
// which pr-review.yml requests once its review is clean: the auto-merge rule is
// on and nothing in the run needs the owner.
func autoMerge(ruleOn bool, runID string, t runner.Ticket, sum runner.Summary, failedGate string, check prCheck) bool {
	return ruleOn && !ownerAttention(runID, t, sum, failedGate, check)
}

// refuseWorkflowPush fails a build whose write set touches a workflow file, so
// the pr job, which could not push it, never runs. The files are the branch's
// own; with none given — the write set could not be read — the build's own stop
// is what is left to go on.
func refuseWorkflowPush(logger *slog.Logger, sum runner.Summary, workflowFiles []string) error {
	if len(workflowFiles) == 0 && sum.StopReason != runner.StopWorkflowChange {
		return nil
	}
	logger.Warn("pushRefused", "reason", string(runner.StopWorkflowChange), "detail", sum.StopDetail,
		"files", workflowFiles)
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
// loopDetail is the pre-PR loop's report of why it is a draft, and check names
// what pr-meta established about the write set — each of which also keeps it
// a draft. The summary's own findings travel with it: the pre-open review's
// report is part of the body whether or not they kept it a draft.
func renderPR(tmpl string, rec runner.Record, sum runner.Summary, failedGate, runURL, loopDetail string, check prCheck) (title, body string, err error) {
	t := rec.Ticket()
	body, err = runner.PRBody(tmpl, t, sum.PRSummary, failedGate, runURL, loopDetail, check.Overlap, check.OutOfPlan, check.Unread, check.FrontEnd, !check.Planned, sum.ReviewFindings, check.Provisional, check.Supersedes, check.Retried)
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
