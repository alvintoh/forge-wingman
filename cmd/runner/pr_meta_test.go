package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// prRecord is a run record as pr-meta reads it: the ticket, and none of the
// build's fields, which only the record job writes after the PR opens.
var prRecord = runner.Record{TicketID: "ABC-1", TicketTitle: "[BE] Add the widget", Size: "M", SizedBy: "test", TicketBody: "Add it."}

func prTemplate(t *testing.T) string {
	b, err := os.ReadFile(filepath.Join("..", "..", runner.DefaultPRTemplate))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func encodedSummary(t *testing.T, attemptID string, now time.Time, edits ...func(*runner.Summary)) string {
	tk := prRecord.Ticket()
	sum := runner.Summary{
		Outcome: runner.OutcomeBuilt,
		Phase:   runner.PhaseCommit,
		Steps: []runner.Step{{Phase: runner.PhaseBuild, Round: 1, Model: "opencode-go/x",
			CompletionsObject: "completions/" + attemptID + "-build-1.jsonl", At: now.Add(-30 * time.Minute)}},
		EditedFiles:   []string{"widget.go", "extra.go"},
		Branch:        runner.BranchName(tk.BranchSegment(), attemptID),
		CommitSubject: "feat(runner): ABC-1 add the widget",
		PRSummary:     "Adds the widget the runner needs.",
		StartedAt:     now.Add(-time.Hour),
	}
	for _, edit := range edits {
		edit(&sum)
	}
	raw, err := sum.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestAutoMerge(t *testing.T) {
	// run-0 falls in the review sample and run-abc-12 does not.
	const unsampled, sampled = "run-abc-12", "run-0"
	hold := autoMergeHold{Reason: runner.WaitEscapedPR, Detail: "octo/scratch#341 was reverted"}
	for name, tt := range map[string]struct {
		runID, size, failedGate string
		edit                    func(*runner.Summary)
		check                   prCheck
		hold                    autoMergeHold
		want                    bool
	}{
		"a ready M run requests it":                    {unsampled, "M", "", nil, prCheck{Planned: true}, autoMergeHold{}, true},
		"an unplanned S run requests it":               {unsampled, "S", "", nil, prCheck{}, autoMergeHold{}, true},
		"a run with no recorded plan does not":         {unsampled, "M", "", nil, prCheck{}, autoMergeHold{}, false},
		"an unplanned L run does not":                  {unsampled, "L", "", nil, prCheck{}, autoMergeHold{}, false},
		"an L run does not":                            {unsampled, "L", "", nil, prCheck{Planned: true}, autoMergeHold{}, false},
		"a sampled run does not":                       {sampled, "M", "", nil, prCheck{Planned: true}, autoMergeHold{}, false},
		"a draft does not":                             {unsampled, "M", "", func(s *runner.Summary) { s.Ready = false }, prCheck{Planned: true}, autoMergeHold{}, false},
		"an out-of-plan edit does not":                 {unsampled, "M", "", nil, prCheck{Planned: true, OutOfPlan: []string{"x.go"}}, autoMergeHold{}, false},
		"a concurrent run's plan does not":             {unsampled, "S", "", nil, prCheck{Planned: true, Overlap: "M-2"}, autoMergeHold{}, false},
		"an unread bundle does not":                    {unsampled, "M", "", nil, prCheck{Planned: true, Unread: true}, autoMergeHold{}, false},
		"front-end paths do not":                       {unsampled, "M", "", nil, prCheck{Planned: true, FrontEnd: []string{"web/"}}, autoMergeHold{}, false},
		"the front-end label does not":                 {unsampled, "S", "", nil, prCheck{Planned: true, FrontEnd: []string{"area:fe"}}, autoMergeHold{}, false},
		"a workflow change does not":                   {unsampled, "M", "", func(s *runner.Summary) { s.StopReason = runner.StopWorkflowChange }, prCheck{Planned: true}, autoMergeHold{}, false},
		"a failed check gate does not":                 {unsampled, "M", "test", nil, prCheck{Planned: true}, autoMergeHold{}, false},
		"an unconfirmed provisional decision does not": {unsampled, "M", "", nil, prCheck{Planned: true, Provisional: []runner.Provisional{{Question: "Q?", Choice: "A"}}}, autoMergeHold{}, false},
		"unreadable provisional decisions do not":      {unsampled, "M", "", nil, prCheck{Planned: true, ProvisionalUnread: true}, autoMergeHold{}, false},
		"a confirmed provisional decision does":        {unsampled, "M", "", nil, prCheck{Planned: true, Provisional: []runner.Provisional{{Question: "Q?", Choice: "A", Confirmed: true}}}, autoMergeHold{}, true},
		"an auto-merge hold does not":                  {unsampled, "S", "", nil, prCheck{Planned: true}, hold, false},
	} {
		t.Run(name, func(t *testing.T) {
			sum := runner.Summary{Outcome: runner.OutcomeBuilt, Ready: true}
			if tt.edit != nil {
				tt.edit(&sum)
			}
			tk := prRecord.Ticket()
			tk.Size = tt.size
			if got := autoMerge(true, tt.runID, tk, sum, tt.failedGate, tt.check, tt.hold); got != tt.want {
				t.Fatalf("autoMerge = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAutoMergeNeedsTheSwitchOn(t *testing.T) {
	sum := runner.Summary{Outcome: runner.OutcomeBuilt, Ready: true}
	clear := prRecord.Ticket()
	if autoMerge(false, "run-abc-12", clear, sum, "", prCheck{Planned: true}, autoMergeHold{}) {
		t.Error("switch off: autoMerge = true, want false for a run nothing else holds back")
	}
	if !autoMerge(true, "run-abc-12", clear, sum, "", prCheck{Planned: true}, autoMergeHold{}) {
		t.Error("switch on: autoMerge = false, want true")
	}
}

func TestWaitReasonPutsTheReasonsThatBlockAMergeFirst(t *testing.T) {
	const unsampled, sampled = "run-abc-12", "run-0"
	unconfirmed := []runner.Provisional{{Question: "Q?", Choice: "A"}}
	for name, tt := range map[string]struct {
		runID, size string
		ready       bool
		check       prCheck
		hold        runner.WaitReason
		want        runner.WaitReason
	}{
		"nothing holds it":                      {unsampled, "M", true, prCheck{Planned: true}, "", ""},
		"a decision hides every other reason":   {sampled, "L", false, prCheck{Planned: true, FrontEnd: []string{"web/"}, Provisional: unconfirmed}, "", runner.WaitUnconfirmedDecision},
		"unread decisions count as unsettled":   {unsampled, "M", true, prCheck{Planned: true, ProvisionalUnread: true}, "", runner.WaitUnconfirmedDecision},
		"a draft hides front-end":               {unsampled, "L", false, prCheck{Planned: true, FrontEnd: []string{"web/"}}, "", runner.WaitDraft},
		"an unread bundle makes a draft":        {unsampled, "M", true, prCheck{Planned: true, Unread: true}, "", runner.WaitDraft},
		"front-end comes before size L":         {sampled, "L", true, prCheck{Planned: true, FrontEnd: []string{"web/"}}, "", runner.WaitFrontEnd},
		"front-end hides the hold":              {unsampled, "M", true, prCheck{Planned: true, FrontEnd: []string{"web/"}}, runner.WaitEscapedPR, runner.WaitFrontEnd},
		"size L comes before the sample":        {sampled, "L", true, prCheck{Planned: true}, "", runner.WaitSizeL},
		"the sample alone":                      {sampled, "M", true, prCheck{Planned: true}, "", runner.WaitSampled},
		"the hold names what nothing else does": {unsampled, "S", true, prCheck{}, runner.WaitEscapedPR, runner.WaitEscapedPR},
		"an unplanned run has no named reason":  {unsampled, "M", true, prCheck{}, "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			tk := prRecord.Ticket()
			tk.Size = tt.size
			sum := runner.Summary{Outcome: runner.OutcomeBuilt, Ready: tt.ready}
			if got := waitReason(tt.runID, tk, sum, tt.check, tt.hold); got != tt.want {
				t.Fatalf("waitReason = %q, want %q", got, tt.want)
			}
		})
	}
}

// fakeRecentRewrites returns the samples configured, or fails to read them.
type fakeRecentRewrites struct {
	samples []dispatcher.Sample
	err     error
	limit   *int
	reads   *int
}

func (f fakeRecentRewrites) RecentHumanMerged(_ context.Context, limit int) ([]dispatcher.Sample, error) {
	if f.limit != nil {
		*f.limit, *f.reads = limit, *f.reads+1
	}
	return f.samples, f.err
}

func TestAutoMergeRate(t *testing.T) {
	clean := make([]dispatcher.Sample, dispatcher.TrailingPRs)
	for i := range clean {
		clean[i] = dispatcher.Sample{Rewrite: dispatcher.Rewrite{AgentLines: 100}, MergedBy: "alvintoh"}
	}
	over := make([]dispatcher.Sample, dispatcher.TrailingPRs)
	for i := range over {
		over[i] = dispatcher.Sample{Rewrite: dispatcher.Rewrite{AgentLines: 100, OwnerLines: 30}, MergedBy: "alvintoh"}
	}
	atGuideline := make([]dispatcher.Sample, dispatcher.TrailingPRs)
	for i := range atGuideline {
		atGuideline[i] = dispatcher.Sample{Rewrite: dispatcher.Rewrite{AgentLines: 100, OwnerLines: 20}, MergedBy: "alvintoh"}
	}
	for name, tt := range map[string]struct {
		store     fakeRecentRewrites
		want      bool
		reason    string
		wantWarn  bool
		wantReads int
	}{
		"ten measured PRs under the guideline": {fakeRecentRewrites{samples: clean}, true, "on", false, 1},
		"exactly at the guideline":             {fakeRecentRewrites{samples: atGuideline}, true, "on", false, 1},
		"a rate above the guideline":           {fakeRecentRewrites{samples: over}, false, "above-guideline", false, 1},
		"too few measured PRs":                 {fakeRecentRewrites{samples: clean[:3]}, false, "too-few-measured", false, 1},
		"an unreadable read":                   {fakeRecentRewrites{samples: clean, err: errors.New("firestore is unreachable")}, false, "evidence-unread", true, 1},
	} {
		t.Run(name, func(t *testing.T) {
			var logs strings.Builder
			var reads, limit int
			tt.store.reads, tt.store.limit = &reads, &limit
			got := autoMergeRate(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), "run-abc-12", tt.store)
			if got.WithinGuideline != tt.want || got.Reason != tt.reason || strings.Contains(logs.String(), "msg=autoMergeEvidenceUnread") != tt.wantWarn || reads != tt.wantReads {
				t.Fatalf("decision = %+v, log %q, reads %d, want within the guideline = %v, reason %q, warned = %v, reads %d", got, logs.String(), reads, tt.want, tt.reason, tt.wantWarn, tt.wantReads)
			}
			if reads > 0 && limit != dispatcher.TrailingPRs {
				t.Fatalf("RecentHumanMerged limit = %d, want %d", limit, dispatcher.TrailingPRs)
			}
		})
	}
}

// TestAutoMergeIsDecidedByTheSwitchAndTheLatch asserts what has to hold for an
// eligible PR to be marked for auto-merge: the switch on and readable, and no
// escaped PR. The rewrite rate is read beside them and holds nothing, so every
// rate the window can report leaves the PR eligible.
func TestAutoMergeIsDecidedByTheSwitchAndTheLatch(t *testing.T) {
	rated := func(ownerPerPR int64) []dispatcher.Sample {
		window := make([]dispatcher.Sample, 0, dispatcher.TrailingPRs)
		for range dispatcher.TrailingPRs {
			window = append(window, dispatcher.Sample{Rewrite: dispatcher.Rewrite{AgentLines: 1_000, OwnerLines: ownerPerPR}, MergedBy: "alvintoh"})
		}
		return window
	}
	noAgentLines := make([]dispatcher.Sample, dispatcher.TrailingPRs)
	for i := range noAgentLines {
		noAgentLines[i] = dispatcher.Sample{MergedBy: "alvintoh"}
	}
	unreadable := errors.New("firestore is unreachable")
	on := &fakeAutoMergeSwitchStore{setting: store.AutoMergeSetting{Enabled: true}}
	off := &fakeAutoMergeSwitchStore{setting: store.AutoMergeSetting{Enabled: false}}
	escaped := &fakeAutoMergeSwitchStore{setting: store.AutoMergeSetting{Enabled: true}}
	escaped.setting.Escaped = dispatcher.MergeEscape{Repo: "octo/scratch", PR: 341, Reason: dispatcher.EscapeReverted}
	for name, tt := range map[string]struct {
		store    *fakeAutoMergeSwitchStore
		rewrites fakeRecentRewrites
		merge    bool
		switchOn bool
		reason   string
		hold     runner.WaitReason
		warn     bool
	}{
		"the switch on under the guideline":          {on, fakeRecentRewrites{samples: rated(100)}, true, true, "on", "", false},
		"the switch on exactly at the guideline":     {on, fakeRecentRewrites{samples: rated(200)}, true, true, "on", "", false},
		"the switch on above the guideline":          {on, fakeRecentRewrites{samples: rated(201)}, true, true, "above-guideline", "", false},
		"the switch on with fewer than ten measured": {on, fakeRecentRewrites{samples: rated(0)[:9]}, true, true, "too-few-measured", "", false},
		"the switch on with no agent lines":          {on, fakeRecentRewrites{samples: noAgentLines}, true, true, "no-agent-lines", "", false},
		"the switch on with unreadable evidence":     {on, fakeRecentRewrites{err: unreadable}, true, true, "evidence-unread", "", false},
		"the switch on with an escaped PR":           {escaped, fakeRecentRewrites{samples: rated(0)}, false, true, "on", runner.WaitEscapedPR, false},
		"an escaped PR above the guideline":          {escaped, fakeRecentRewrites{samples: rated(250)}, false, true, "above-guideline", runner.WaitEscapedPR, false},
		"the switch off above the guideline":         {off, fakeRecentRewrites{samples: rated(250)}, false, false, "above-guideline", "", false},
		"a settings read that fails":                 {&fakeAutoMergeSwitchStore{readErr: unreadable}, fakeRecentRewrites{samples: rated(0)}, false, false, "on", runner.WaitAutoMergeUnread, true},
	} {
		t.Run(name, func(t *testing.T) {
			var logs strings.Builder
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			sum := runner.Summary{Outcome: runner.OutcomeBuilt, Ready: true}
			got := decideAutoMerge(context.Background(), logger, "run-abc-12", tt.store, tt.rewrites, prRecord.Ticket(), sum, "", prCheck{Planned: true})
			if got.Merge != tt.merge || got.SwitchOn != tt.switchOn || got.Rate.Reason != tt.reason || got.Hold.Reason != tt.hold {
				t.Fatalf("verdict = %+v, want merge %v, switch %v, the rate's reason %q and the hold %q", got, tt.merge, tt.switchOn, tt.reason, tt.hold)
			}
			if got.WaitReason != tt.hold {
				t.Fatalf("wait reason = %q, want the hold %q", got.WaitReason, tt.hold)
			}
			if logged := strings.Contains(logs.String(), "msg=autoMergeSettingsUnread"); logged != tt.warn {
				t.Fatalf("log %q, want the unread settings logged = %v", logs.String(), tt.warn)
			}
		})
	}
}

// The latch holds auto-merge off and the rate is still worded beside it, so the
// PR body and the run's log carry both facts without the rate holding anything.
func TestDecideAutoMergeHoldsForTheLatchAndStillWordsTheRate(t *testing.T) {
	escaped := &fakeAutoMergeSwitchStore{setting: store.AutoMergeSetting{Enabled: true,
		Escaped: dispatcher.MergeEscape{Repo: "octo/scratch", PR: 341, Reason: dispatcher.EscapeReverted}}}
	rated := make([]dispatcher.Sample, 0, dispatcher.TrailingPRs)
	for range dispatcher.TrailingPRs {
		rated = append(rated, dispatcher.Sample{Rewrite: dispatcher.Rewrite{AgentLines: 1_000, OwnerLines: 250}, MergedBy: "alvintoh"})
	}
	got := decideAutoMerge(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), "run-abc-12",
		escaped, fakeRecentRewrites{samples: rated}, prRecord.Ticket(), runner.Summary{Outcome: runner.OutcomeBuilt, Ready: true}, "", prCheck{Planned: true})
	if got.Merge || got.Hold.Reason != runner.WaitEscapedPR {
		t.Fatalf("verdict = %+v, want the latch holding auto-merge off", got)
	}
	if !strings.Contains(got.Hold.Detail, "until you switch it on again") {
		t.Fatalf("detail = %q, want the latch named", got.Hold.Detail)
	}
	if !strings.Contains(got.RateWords, "25.0%") || !strings.Contains(got.RateWords, "above the 20% guideline") {
		t.Fatalf("rate words = %q, want the rate against the guideline beside the hold", got.RateWords)
	}
}

// TestRateWordsNamesTheRateAgainstTheGuideline asserts what the PR body says
// about the rate: where it sits against the 20% guideline, or which condition
// stopped it being computed. None of it says the rate holds the merge.
func TestRateWordsNamesTheRateAgainstTheGuideline(t *testing.T) {
	for name, tt := range map[string]struct {
		decision dispatcher.AutoMergeDecision
		detail   string
	}{
		"at or under the guideline": {
			dispatcher.AutoMergeDecision{Reason: dispatcher.AutoMergeReasonOn, WithinGuideline: true, PRs: 10, Rate: 0.1},
			"at or under the 20% guideline",
		},
		"above the guideline": {
			dispatcher.AutoMergeDecision{Reason: dispatcher.AutoMergeReasonAboveGuideline, PRs: 10, Rate: 0.234},
			"above the 20% guideline",
		},
		"too few measured": {
			dispatcher.AutoMergeDecision{Reason: dispatcher.AutoMergeReasonTooFewMeasured, PRs: 4},
			"4 of 10",
		},
		"no agent lines": {
			dispatcher.AutoMergeDecision{Reason: dispatcher.AutoMergeReasonNoAgentLines, PRs: 10},
			"no agent lines",
		},
		"evidence unread": {
			dispatcher.AutoMergeDecision{Reason: dispatcher.AutoMergeReasonEvidenceUnread},
			"could not be read",
		},
		"a reason this build does not know": {
			dispatcher.AutoMergeDecision{Reason: "reason-from-a-later-build", PRs: 10, Rate: 0.1},
			"could not be read",
		},
	} {
		t.Run(name, func(t *testing.T) {
			words := rateWords(tt.decision)
			if !strings.Contains(words, tt.detail) {
				t.Fatalf("rate words = %q, want them to name %q", words, tt.detail)
			}
			for _, banned := range []string{"hold", "off", "bar"} {
				if strings.Contains(strings.ToLower(words), banned) {
					t.Fatalf("rate words = %q, want them not to say %q", words, banned)
				}
			}
		})
	}
}

func TestRateAttrsEncodeAsJSONAndOmitWhatWasNotMeasured(t *testing.T) {
	for name, tt := range map[string]struct {
		d        dispatcher.AutoMergeDecision
		measured bool
		rate     string
	}{
		"a rate":         {dispatcher.AutoMergeDecision{Reason: dispatcher.AutoMergeReasonAboveGuideline, PRs: 10, Rate: 0.12}, true, "12.0%"},
		"no agent lines": {dispatcher.AutoMergeDecision{Reason: dispatcher.AutoMergeReasonNoAgentLines, PRs: 10, Rate: math.Inf(1)}, true, "no agent lines"},
		"the switch off": {dispatcher.AutoMergeDecision{Reason: dispatcher.AutoMergeReasonSwitchOff}, false, ""},
		"unreadable":     {dispatcher.AutoMergeDecision{Reason: dispatcher.AutoMergeReasonEvidenceUnread}, false, ""},
	} {
		t.Run(name, func(t *testing.T) {
			var buf strings.Builder
			slog.New(slog.NewJSONHandler(&buf, nil)).Info("autoMergeDecided", rateAttrs(tt.d)...)
			var line map[string]any
			if err := json.Unmarshal([]byte(buf.String()), &line); err != nil {
				t.Fatalf("log %q is not JSON: %v", buf.String(), err)
			}
			if strings.Contains(buf.String(), "!ERROR") {
				t.Fatalf("log %q holds an unencodable value", buf.String())
			}
			_, hasRate := line["rate"]
			_, hasMeasured := line["measured"]
			if hasRate != tt.measured || hasMeasured != tt.measured || line["reason"] != tt.d.Reason || (tt.measured && line["rate"] != tt.rate) {
				t.Fatalf("log %q, want measured = %v, rate %q", buf.String(), tt.measured, tt.rate)
			}
		})
	}
}

func TestAutoMergeVerdictLogsTheGuidelineTheSwitchAndTheHold(t *testing.T) {
	v := autoMergeVerdict{Merge: true, SwitchOn: true,
		Rate: dispatcher.AutoMergeDecision{Reason: dispatcher.AutoMergeReasonAboveGuideline, PRs: 10, Rate: 0.153}}
	var buf strings.Builder
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("autoMergeDecided", v.attrs()...)
	var line map[string]any
	if err := json.Unmarshal([]byte(buf.String()), &line); err != nil {
		t.Fatalf("log %q is not JSON: %v", buf.String(), err)
	}
	if line["reason"] != "above-guideline" || line["autoMerge"] != true || line["switchOn"] != true ||
		line["rateGuideline"] != "20%" {
		t.Fatalf("log %q, want the guideline and the rate's reason beside autoMerge true and switchOn true", buf.String())
	}
	if _, held := line["hold"]; held {
		t.Fatalf("log %q, want no hold for a rate above the guideline", buf.String())
	}
	held := v
	held.Hold = autoMergeHold{Reason: runner.WaitEscapedPR, Detail: "octo/scratch#341 was reverted"}
	buf.Reset()
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("autoMergeDecided", held.attrs()...)
	if !strings.Contains(buf.String(), `"hold":"escaped-pr"`) {
		t.Fatalf("log %q, want the escaped PR named as the hold", buf.String())
	}
}

func TestPlanRecorded(t *testing.T) {
	for name, tt := range map[string]struct {
		plan runner.PlanRecord
		want bool
	}{
		"no plan":           {runner.PlanRecord{}, false},
		"an empty plan":     {runner.PlanRecord{Files: []string{}}, false},
		"a plan with files": {runner.PlanRecord{Files: []string{"a.go"}}, true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := planRecorded(tt.plan); got != tt.want {
				t.Fatalf("planRecorded = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRefuseWorkflowPush(t *testing.T) {
	now := time.Now()
	stopped := func(s *runner.Summary) {
		s.Outcome, s.StopReason = runner.OutcomeStopped, runner.StopWorkflowChange
		s.StopDetail = "touches files under .github/workflows/, which the run cannot push: .github/workflows/ci.yml"
	}
	wf := []string{".github/workflows/ci.yml"}
	for name, tt := range map[string]struct {
		raw     string
		files   []string
		refused bool
	}{
		"a workflow file in the write set is refused":        {encodedSummary(t, "42-1", now), wf, true},
		"a workflow change the build stopped for is refused": {encodedSummary(t, "42-1", now, stopped), nil, true},
		"a built summary is not":                             {encodedSummary(t, "42-1", now), nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			var log strings.Builder
			logger := slog.New(slog.NewTextHandler(&log, nil))
			err := refuseWorkflowPush(logger, buildSummary(logger, tt.raw, "42-1", prRecord.Ticket(), now), tt.files)
			if tt.refused != errors.Is(err, errRunFailed) {
				t.Fatalf("err = %v, want refused %v", err, tt.refused)
			}
			if tt.refused && !strings.Contains(log.String(), "msg=pushRefused") {
				t.Fatalf("log = %q, want the refusal logged", log.String())
			}
		})
	}
}

func TestRenderPRFallsBackToTheTicketWithoutAUsableSummary(t *testing.T) {
	now := time.Now()
	for name, tt := range map[string]struct {
		raw     string
		wantLog bool
	}{
		"empty":           {"", false},
		"another attempt": {encodedSummary(t, "42-2", now), true},
		"malformed":       {"{", true},
	} {
		var log strings.Builder
		logger := slog.New(slog.NewTextHandler(&log, nil))
		sum := buildSummary(logger, tt.raw, "42-1", prRecord.Ticket(), now)
		title, body, err := renderPR(prTemplate(t), prRecord, sum, "", "https://example.test/run", "", prCheck{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if title != "ABC-1 [BE] Add the widget" || !strings.Contains(body, "`ABC-1`: [BE] Add the widget") ||
			strings.Contains(body, "outside the plan") {
			t.Errorf("%s: title %q, want the ticket's subject and summary and no out-of-plan line:\n%s", name, title, body)
		}
		if got := strings.Contains(log.String(), "summaryRejected"); got != tt.wantLog {
			t.Errorf("%s: logged summaryRejected = %v, want %v: %q", name, got, tt.wantLog, log.String())
		}
	}
}

// TestRenderPRNamesWhatPRMetaComputedNotWhatTheModelClaimed is the AC11
// regression: the summary above claims `extra.go` is out of plan, and the branch
// read out of its bundle says nothing is, so the body names what pr-meta
// computed. The model job's own list is the claim under test.
func TestRenderPRNamesWhatPRMetaComputedNotWhatTheModelClaimed(t *testing.T) {
	now := time.Now()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sum := buildSummary(logger, encodedSummary(t, "42-1", now), "42-1", prRecord.Ticket(), now)
	planned := []string{"widget.go", "extra.go"}
	check := prCheck{
		Changed:   planned,
		OutOfPlan: outOfPlan(runner.PlanRecord{Files: planned}, planned),
		Overlap:   "M-2",
	}
	title, body, err := renderPR(prTemplate(t), prRecord, sum, "", "https://example.test/run", "", check)
	if err != nil {
		t.Fatal(err)
	}
	if title != "feat(runner): ABC-1 add the widget" {
		t.Fatalf("title = %q, want the build's committed subject", title)
	}
	if !strings.Contains(body, "\nAdds the widget the runner needs\\.\n") {
		t.Fatalf("body lacks the build's summary:\n%s", body)
	}
	if !strings.Contains(body, "**Concurrent run:** kept this a draft — `M-2`") {
		t.Fatalf("body does not name the run whose plan these files touch:\n%s", body)
	}
}

// The body names what holds auto-merge, so a reader sees why a PR that could
// have merged itself did not (AC2, AC8). The words themselves are the hold's;
// prbody escapes punctuation, so this checks the note and its prefix.
func TestRenderPRNamesWhatHoldsAutoMerge(t *testing.T) {
	now := time.Now()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sum := buildSummary(logger, encodedSummary(t, "42-1", now), "42-1", prRecord.Ticket(), now)
	hold := autoMergeHold{Reason: runner.WaitEscapedPR, Detail: "the latch holds it until you switch it on again"}
	_, body, err := renderPR(prTemplate(t), prRecord, sum, "", "https://example.test/run", "", prCheck{Planned: true, Hold: hold})
	if err != nil {
		t.Fatal(err)
	}
	if want := "**Auto-merge held:** the latch holds it until you switch it on again"; !strings.Contains(body, want) {
		t.Fatalf("body does not name what holds auto-merge:\n%s", body)
	}
}

// A size-S run has no plan stage, so its body does not claim a missing plan
// keeps it from auto-merge, while a size-M run's does (AC1).
func TestRenderPRSaysAMissingPlanHoldsOnlyAnMOrLRun(t *testing.T) {
	now := time.Now()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sum := buildSummary(logger, encodedSummary(t, "42-1", now), "42-1", prRecord.Ticket(), now)
	s := prRecord
	s.Size = "S"
	_, body, err := renderPR(prTemplate(t), s, sum, "", "https://example.test/run", "", prCheck{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "No recorded plan") {
		t.Fatalf("a size-S body claims a missing plan holds it:\n%s", body)
	}
	_, bodyM, err := renderPR(prTemplate(t), prRecord, sum, "", "https://example.test/run", "", prCheck{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bodyM, "No recorded plan") {
		t.Fatalf("a size-M body does not name the missing plan:\n%s", bodyM)
	}
}

func TestPRTitlePrefersTheCommittedSubject(t *testing.T) {
	tk := runner.Ticket{ID: "ABC-1", Title: "[BE] Add the widget"}
	if got := prTitle(runner.Summary{}, tk); got != "ABC-1 [BE] Add the widget" {
		t.Fatalf("title without a committed subject = %q, want the ticket's subject", got)
	}
	sum := runner.Summary{CommitSubject: "feat(runner): ABC-1 add the widget"}
	if got := prTitle(sum, tk); got != "feat(runner): ABC-1 add the widget" {
		t.Fatalf("title = %q, want the committed subject", got)
	}
}

func TestPlanOverlapNamesTheRunWhosePlanTheseFilesTouch(t *testing.T) {
	flight := []dispatcher.InFlight{
		{Ticket: "M-1", Repo: "octo/repo", Size: "M", Stage: dispatcher.StageBuild,
			Files: []string{"internal/runner/build.go", "cmd/runner/main.go"}},
		{Ticket: "L-1", Repo: "octo/other", Size: "L", Files: []string{"web/src/app.go"}},
		// A run whose plan has not settled writes an unknown set, not an empty
		// one, so it can touch nothing it could be named for.
		{Ticket: "M-2", Repo: "octo/repo", Size: "M", Stage: dispatcher.StagePlan},
		{Ticket: "M-3", Repo: "octo/repo", Size: "M", Files: []string{"web/src/app.go"}},
	}
	for name, tt := range map[string]struct {
		size, repository string
		changed          []string
		want             string
	}{
		"a planned file":                {"S", "octo/repo", []string{"cmd/runner/main.go"}, "M-1"},
		"only the first match is named": {"S", "octo/repo", []string{"internal/runner/build.go"}, "M-1"},
		"nothing planned is touched":    {"S", "octo/repo", []string{"docs/readme.md"}, ""},
		"another repository's plan":     {"S", "octo/repo", []string{"web/src/app.go"}, "M-3"},
		"no changed files":              {"S", "octo/repo", nil, ""},
		"an unplanned run is not named": {"S", "octo/repo", []string{"a.go"}, ""},
		"an M run asks nothing":         {"M", "octo/repo", []string{"cmd/runner/main.go"}, ""},
		"an L run asks nothing":         {"L", "octo/repo", []string{"cmd/runner/main.go"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := planOverlap(tt.size, tt.repository, tt.changed, flight); got != tt.want {
				t.Fatalf("planOverlap = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestOutOfPlanNeedsARecordedPlan holds that a run no plan stage recorded has no
// trusted list to be outside of; autoMerge refuses such a run on its own.
func TestOutOfPlanNeedsARecordedPlan(t *testing.T) {
	changed := []string{"a.go", "b.go"}
	if got := outOfPlan(runner.PlanRecord{}, changed); got != nil {
		t.Fatalf("outOfPlan with no recorded plan = %q, want nothing", got)
	}
	if got := outOfPlan(runner.PlanRecord{Files: []string{"a.go"}}, changed); !slices.Equal(got, []string{"b.go"}) {
		t.Fatalf("outOfPlan = %q, want the unplanned file", got)
	}
}

func TestDraftPR(t *testing.T) {
	for name, tt := range map[string]struct {
		sum   runner.Summary
		check prCheck
		want  bool
	}{
		"a ready run in plan":     {runner.Summary{Ready: true}, prCheck{Changed: []string{"a.go"}}, false},
		"no recorded plan":        {runner.Summary{Ready: true}, prCheck{}, false},
		"an unready run":          {runner.Summary{}, prCheck{}, true},
		"an out-of-plan edit":     {runner.Summary{Ready: true}, prCheck{OutOfPlan: []string{"b.go"}}, true},
		"a concurrent run's plan": {runner.Summary{Ready: true}, prCheck{Overlap: "M-1"}, true},
		"an unread write set":     {runner.Summary{Ready: true}, prCheck{Unread: true}, true},
		"a front-end change":      {runner.Summary{Ready: true}, prCheck{FrontEnd: []string{"web/"}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := draftPR(tt.sum, tt.check); got != tt.want {
				t.Fatalf("draftPR = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRunWorkflowReadsTheBranchOutOfTheBundle is the credential-free design:
// the pr job has not pushed the branch yet, so pr-meta reads the write set out
// of the bundle the model job uploaded, diffed against the main it checked out.
func TestRunWorkflowReadsTheBranchOutOfTheBundle(t *testing.T) {
	yml, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "run.yml"))
	if err != nil {
		t.Fatal(err)
	}
	_, job, _ := strings.Cut(string(yml), "\n  pr-meta:\n")
	for _, want := range []string{
		"          fetch-depth: 0\n",
		"          name: bundle\n",
		`-bundle "$RUNNER_TEMP/wingman.bundle" -branch "$BRANCH" -repository "$REPOSITORY"`,
		"BRANCH: ${{ needs.model.outputs.branch }}",
		"REPOSITORY: ${{ github.repository }}",
		"draft: ${{ steps.meta.outputs.draft }}",
	} {
		if !strings.Contains(job, want) {
			t.Errorf("run.yml's pr-meta job lacks %q", want)
		}
	}
}

// TestRunWorkflowOpensTheDraftPRMetasDecisionNames is the S-overlap draft: the
// pr job opens a draft from pr-meta's own decision, which the model job's ready
// output could otherwise override.
func TestRunWorkflowOpensTheDraftPRMetasDecisionNames(t *testing.T) {
	yml, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "run.yml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(yml)
	if !strings.Contains(s, "DRAFT: ${{ needs.pr-meta.outputs.draft }}") ||
		!strings.Contains(s, `[[ "$DRAFT" != "true" ]] && draft=()`) {
		t.Errorf("run.yml's pr job does not open a draft from pr-meta's decision:\n%s", openStep(s))
	}
	if strings.Contains(s, "needs.model.outputs.ready") {
		t.Error("run.yml still reads the model job's own ready output; pr-meta decides the draft")
	}
}

// openStep is the pr job's open step, for an assertion message that shows it.
func openStep(yml string) string {
	_, after, _ := strings.Cut(yml, "\n  pr:\n")
	_, step, _ := strings.Cut(after, "\n      - id: open\n")
	step, _, _ = strings.Cut(step, "\n\n")
	return step
}

func TestPRBodyNamesAnUnreadWriteSetAsWhyThePRIsADraft(t *testing.T) {
	now := time.Now()
	sum := buildSummary(slog.New(slog.NewTextHandler(io.Discard, nil)),
		encodedSummary(t, "42-1", now), "42-1", prRecord.Ticket(), now)
	_, body, err := renderPR(prTemplate(t), prRecord, sum, "", "https://example.test/run", "",
		prCheck{Unread: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "**Write set unread:**") {
		t.Fatalf("body = %q, want the unread write set named as why the PR is a draft", body)
	}
}

// TestRenderPRCarriesTheSummarysGuardAndFindingCounts keeps the summary's
// counts wired to the notes: the guards left unprobed, and the findings the
// cap left out of the list.
func TestRenderPRCarriesTheSummarysGuardAndFindingCounts(t *testing.T) {
	now := time.Now()
	sum := buildSummary(slog.New(slog.NewTextHandler(io.Discard, nil)),
		encodedSummary(t, "42-1", now), "42-1", prRecord.Ticket(), now)
	sum.GuardsUntested = 2
	sum.ReviewFindings = []runner.ReviewFinding{{Finding: "a finding", Resolution: "still open"}}
	sum.ReviewFindingsOmitted = 5
	_, body, err := renderPR(prTemplate(t), prRecord, sum, "", "https://example.test/run", "", prCheck{Planned: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"**Guards unprobed:** 2 of the guards", "- +5 more, not listed"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body lacks %q:\n%s", want, body)
		}
	}
}

func TestRenderPRNamesAMissingPlan(t *testing.T) {
	now := time.Now()
	sum := buildSummary(slog.New(slog.NewTextHandler(io.Discard, nil)),
		encodedSummary(t, "42-1", now), "42-1", prRecord.Ticket(), now)
	_, body, err := renderPR(prTemplate(t), prRecord, sum, "", "https://example.test/run", "", prCheck{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "**No recorded plan:**") {
		t.Fatalf("body = %q, want the missing plan named", body)
	}
	_, body, err = renderPR(prTemplate(t), prRecord, sum, "", "https://example.test/run", "", prCheck{Planned: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "No recorded plan") {
		t.Fatalf("body names a missing plan the run has:\n%s", body)
	}
}

func TestDraftPRNamesEveryReasonTheRunHoldsItBack(t *testing.T) {
	now := time.Now()
	ready := buildSummary(slog.New(slog.NewTextHandler(io.Discard, nil)),
		encodedSummary(t, "42-1", now), "42-1", prRecord.Ticket(), now)
	ready.Ready = true
	for name, tt := range map[string]struct {
		check prCheck
		want  bool
	}{
		"a clear write set":          {prCheck{}, false},
		"no recorded plan":           {prCheck{Planned: false}, false},
		"an unread write set":        {prCheck{Unread: true}, true},
		"an edit outside the plan":   {prCheck{OutOfPlan: []string{"a.go"}}, true},
		"a concurrent run's plan":    {prCheck{Overlap: "FRG-79"}, true},
		"a planned read that failed": {prCheck{Overlap: "", Unread: true}, true},
		"a ticket changed mid-run":   {prCheck{TicketChanged: &dispatcher.TicketChange{At: now, Body: "new"}}, true},
		"an unread ticket change":    {prCheck{TicketChangedUnread: true}, true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := draftPR(ready, tt.check); got != tt.want {
				t.Fatalf("draftPR = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRenderPRQuotesTheTicketLinesThatChangedMidRun(t *testing.T) {
	now := time.Now()
	sum := buildSummary(slog.New(slog.NewTextHandler(io.Discard, nil)),
		encodedSummary(t, "42-1", now), "42-1", prRecord.Ticket(), now)
	change := &dispatcher.TicketChange{At: now, Body: prRecord.TicketBody + "\n\nAC5: also do X"}
	_, body, err := renderPR(prTemplate(t), prRecord, sum, "", "https://example.test/run", "",
		prCheck{Planned: true, TicketChanged: change})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "**Ticket changed mid-run:**") {
		t.Fatalf("body does not name the ticket change:\n%s", body)
	}
	if !strings.Contains(body, "+ AC5: also do X") {
		t.Fatalf("body does not quote the line the owner added:\n%s", body)
	}
}

func TestTicketDriftReportsAnUnreadRecord(t *testing.T) {
	if got := (prCheck{TicketChangedUnread: true}).ticketDrift("built"); got == nil || !got.Unread {
		t.Fatalf("ticketDrift = %+v, want an unread drift", got)
	}
	if got := (prCheck{}).ticketDrift("built"); got != nil {
		t.Fatalf("ticketDrift = %+v, want none for a ticket that did not change", got)
	}
	got := (prCheck{TicketChanged: &dispatcher.TicketChange{At: time.Now(), Body: "built\n\nAC5: also do X"}}).ticketDrift("built")
	if got == nil || got.Unread || len(got.Lines) == 0 {
		t.Fatalf("ticketDrift = %+v, want the differing lines", got)
	}
}

func TestRenderPRNamesAnUnreadTicketChange(t *testing.T) {
	now := time.Now()
	sum := buildSummary(slog.New(slog.NewTextHandler(io.Discard, nil)),
		encodedSummary(t, "42-1", now), "42-1", prRecord.Ticket(), now)
	_, body, err := renderPR(prTemplate(t), prRecord, sum, "", "https://example.test/run", "",
		prCheck{Planned: true, TicketChangedUnread: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "**Ticket change unread:**") {
		t.Fatalf("body does not name the unread ticket change:\n%s", body)
	}
}

func TestFrontEnd(t *testing.T) {
	const setting = `{"octo/repo":["web/","ui"],"octo/other":["docs/"]}`
	changed := []string{"internal/a.go", "web/src/app.ts", "uikit/x.ts"}
	for name, tt := range map[string]struct {
		labelled           bool
		repository, config string
		changed            []string
		want               []string
	}{
		"a file under a declared prefix":      {false, "octo/repo", setting, changed, []string{"web/"}},
		"a prefix without a trailing slash":   {false, "octo/repo", setting, []string{"ui/x.ts", "ui"}, []string{"ui"}},
		"a file named as the prefix":          {false, "octo/repo", setting, []string{"web"}, []string{"web/"}},
		"a sibling sharing the prefix's name": {false, "octo/repo", setting, []string{"uikit/x.ts"}, nil},
		"webhook is not under web":            {false, "octo/repo", setting, []string{"webhook/x.go"}, nil},
		"a prefix written ./web":              {false, "octo/repo", `{"octo/repo":["./web"]}`, []string{"web/a.ts"}, []string{"./web"}},
		"a rooted changed path":               {false, "octo/repo", setting, []string{"/web/a.ts"}, []string{"web/"}},
		"an empty prefix holds every change":  {false, "octo/repo", `{"octo/repo":[""]}`, []string{"a.go"}, []string{""}},
		"an empty prefix and no change":       {false, "octo/repo", `{"octo/repo":[""]}`, nil, nil},
		"the prefix's case is exact":          {false, "octo/repo", setting, []string{"Web/a.ts"}, nil},
		"an unset setting is not malformed":   {false, "octo/repo", "", []string{"web/a.ts"}, nil},
		"the repository's casing":             {false, "Octo/Repo", setting, changed, []string{"web/"}},
		"a prefix declared for another repo":  {false, "octo/other", setting, changed, nil},
		"no setting and no label":             {false, "octo/repo", "", changed, nil},
		"an empty prefix list":                {false, "octo/repo", `{"octo/repo":[]}`, changed, nil},
		"the label alone":                     {true, "octo/repo", "", []string{"a.go"}, []string{"area:fe"}},
		"the label and a prefix":              {true, "octo/repo", setting, changed, []string{"area:fe", "web/"}},
		"a setting that is not JSON holds":    {false, "octo/repo", "web/", []string{"a.go"}, []string{"WINGMAN_FRONT_END_PATHS"}},
		"no changed files and no label":       {false, "octo/repo", setting, nil, nil},
	} {
		t.Run(name, func(t *testing.T) {
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			if got := frontEnd(logger, tt.labelled, tt.repository, tt.config, tt.changed); !slices.Equal(got, tt.want) {
				t.Fatalf("frontEnd = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPRBodyNamesWhatHeldAFrontEndChange(t *testing.T) {
	now := time.Now()
	sum := buildSummary(slog.New(slog.NewTextHandler(io.Discard, nil)),
		encodedSummary(t, "42-1", now), "42-1", prRecord.Ticket(), now)
	_, body, err := renderPR(prTemplate(t), prRecord, sum, "", "https://example.test/run", "",
		prCheck{FrontEnd: []string{"area:fe", "web/"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "**Front-end change:** waits for the owner — matched `area:fe`, `web/`; run /fe-sweep before reading the diff") {
		t.Fatalf("body = %q, want the front-end marks named", body)
	}
}

func TestSupersededOutputIsEmptyUnlessAPRIsReplaced(t *testing.T) {
	if got := supersededOutput(141); got != "141" {
		t.Fatalf("supersededOutput(141) = %q, want 141", got)
	}
	if got := supersededOutput(0); got != "" {
		t.Fatalf("supersededOutput(0) = %q, want empty", got)
	}
}

func TestRetriedOutputIsSetOnlyWhenARetryReplacedAPR(t *testing.T) {
	for _, tt := range []struct {
		superseded int
		retried    bool
		want       string
	}{{141, true, "true"}, {141, false, ""}, {0, true, ""}, {0, false, ""}} {
		if got := retriedOutput(tt.superseded, tt.retried); got != tt.want {
			t.Errorf("retriedOutput(%d, %v) = %q, want %q", tt.superseded, tt.retried, got, tt.want)
		}
	}
}

func TestPRMetaWordsASupersedeByWhyTheOwnerMadeIt(t *testing.T) {
	for name, tt := range map[string]struct {
		fields      map[string]any
		wantOutput  string
		wantInBody  string
		wantNotBody string
	}{
		"a retry": {
			map[string]any{"superseded_pr": int64(141), "superseded_by_retry": true, "operator_assisted": true, "operator_note": "Keep the old flag name."},
			"superseded_by_retry=true\n", "Supersedes #141: the owner retried this run with a note.", "chose a different option",
		},
		"a requeue after a retry": {
			map[string]any{"superseded_pr": int64(141), "superseded_by_retry": true},
			"superseded_by_retry=true\n", "Supersedes #141: the owner retried this run with a note.", "chose a different option",
		},
		"a chosen option": {
			map[string]any{"superseded_pr": int64(141)},
			"superseded_by_retry=\n", "Supersedes #141: the owner chose a different option.", "retried this run",
		},
		"a retry that replaced no PR": {
			map[string]any{"operator_assisted": true, "operator_note": "Keep the old flag name."},
			"superseded_by_retry=\n", "", "Supersedes",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, runID, jobs := retriedRun(t, tt.fields)
			out := jobs("pr-meta", "-run-id", runID, "-repository", "octo/scratch", "-template", filepath.Join("..", "..", runner.DefaultPRTemplate))
			if !strings.Contains(out, tt.wantOutput) || !strings.Contains(out, tt.wantInBody) || strings.Contains(out, tt.wantNotBody) {
				t.Fatalf("outputs = %q, want %q and %q, and not %q", out, tt.wantOutput, tt.wantInBody, tt.wantNotBody)
			}
		})
	}
}

func TestOpenFindingsOutputIsEmptyWhenNoneAreOpen(t *testing.T) {
	if got := openFindingsOutput(3); got != "3" {
		t.Fatalf("openFindingsOutput(3) = %q, want 3", got)
	}
	if got := openFindingsOutput(0); got != "" {
		t.Fatalf("openFindingsOutput(0) = %q, want empty", got)
	}
}

func TestFixRoundsEligible(t *testing.T) {
	const unsampled, sampled = "run-abc-12", "run-0"
	for name, tt := range map[string]struct {
		runID, size, failedGate string
		edit                    func(*runner.Summary)
		check                   prCheck
		want                    bool
	}{
		"size L is not":                       {unsampled, "L", "", nil, prCheck{Planned: true}, false},
		"a sampled run is not":                {sampled, "M", "", nil, prCheck{Planned: true}, false},
		"a draft is not":                      {unsampled, "M", "", func(s *runner.Summary) { s.Ready = false }, prCheck{Planned: true}, false},
		"a failed gate is not":                {unsampled, "M", "test", nil, prCheck{Planned: true}, false},
		"a front-end change is not":           {unsampled, "M", "", nil, prCheck{Planned: true, FrontEnd: []string{"web/"}}, false},
		"size M with no plan is not":          {unsampled, "M", "", nil, prCheck{}, false},
		"size M with a plan is":               {unsampled, "M", "", nil, prCheck{Planned: true}, true},
		"size S with no plan is":              {unsampled, "S", "", nil, prCheck{}, true},
		"an unconfirmed decision alone is":    {unsampled, "M", "", nil, prCheck{Planned: true, Provisional: []runner.Provisional{{Question: "Q?", Choice: "A"}}}, true},
		"unread decisions alone are":          {unsampled, "M", "", nil, prCheck{Planned: true, ProvisionalUnread: true}, true},
		"a workflow stop is judged elsewhere": {unsampled, "M", "", func(s *runner.Summary) { s.StopReason = runner.StopWorkflowChange }, prCheck{Planned: true}, true},
	} {
		t.Run(name, func(t *testing.T) {
			sum := runner.Summary{Outcome: runner.OutcomeBuilt, Ready: true}
			if tt.edit != nil {
				tt.edit(&sum)
			}
			tk := prRecord.Ticket()
			tk.Size = tt.size
			if got := fixRoundsEligible(tt.runID, tk, sum, tt.failedGate, tt.check); got != tt.want {
				t.Fatalf("fixRoundsEligible = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestFixRoundsEligibleIgnoresTheAutoMergeDecision: a PR that auto-merge refuses
// for its switch, a hold or an unconfirmed decision is still given fix rounds.
func TestFixRoundsEligibleIgnoresTheAutoMergeDecision(t *testing.T) {
	sum := runner.Summary{Outcome: runner.OutcomeBuilt, Ready: true}
	tk := prRecord.Ticket()
	tk.Size = "M"
	check := prCheck{Planned: true}
	hold := autoMergeHold{Reason: runner.WaitEscapedPR, Detail: "octo/scratch#341 was reverted"}
	unconfirmed := prCheck{Planned: true, Provisional: []runner.Provisional{{Question: "Q?", Choice: "A"}}}
	for name, merges := range map[string]bool{
		"the switch off":          autoMerge(false, "run-abc-12", tk, sum, "", check, autoMergeHold{}),
		"a hold":                  autoMerge(true, "run-abc-12", tk, sum, "", check, hold),
		"an unconfirmed decision": autoMerge(true, "run-abc-12", tk, sum, "", unconfirmed, autoMergeHold{}),
	} {
		if merges {
			t.Fatalf("%s: autoMerge = true, want the case where auto-merge refuses", name)
		}
	}
	if !fixRoundsEligible("run-abc-12", tk, sum, "", check) || !fixRoundsEligible("run-abc-12", tk, sum, "", unconfirmed) {
		t.Fatal("fixRoundsEligible = false for a PR only auto-merge refuses, want true")
	}
}

func TestPROutputsReportsFixRoundsAsItsOwnOutput(t *testing.T) {
	for _, want := range []bool{true, false} {
		got := prOutputs("feat: x", "abc-12", !want, !want, want, prCheck{}, 0)
		if got["fix_rounds"] != strconv.FormatBool(want) || got["auto_merge"] != strconv.FormatBool(!want) {
			t.Fatalf("outputs = %v, want fix_rounds %v apart from auto_merge", got, want)
		}
	}
}
