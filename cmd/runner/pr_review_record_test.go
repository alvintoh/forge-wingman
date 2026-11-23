package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// recordNothing is a recorder for a test that does not look at the record.
func recordNothing(context.Context, store.ReviewRecord) error { return nil }

// capturing is a recorder that keeps what it is given and returns err.
func capturing(into *[]store.ReviewRecord, err error) reviewRecorder {
	return func(_ context.Context, rec store.ReviewRecord) error {
		*into = append(*into, rec)
		return err
	}
}

// postedWith runs pr-review-post on v for the fake pull request 7, which is a
// run's pull request and eligible to merge, with record as the recorder.
func postedWith(t *testing.T, f *fakePulls, v runner.PRVerdict, record reviewRecorder, extra ...string) {
	t.Helper()
	getenv := f.start(t)
	args := append([]string{"-pr", "7", "-reviewed-sha", headSHA, "-verdict", writeVerdict(t, v), "-context-result", "success"}, extra...)
	if err := prReviewPostWith(context.Background(), quietLogger(), getenv, switchedOn, rateUnder, record, args); err != nil {
		t.Fatal(err)
	}
}

func eligiblePull() *fakePulls {
	return &fakePulls{pull: openPull("t", "", headSHA, autoMergeEligibleLabel)}
}

func costed(model, outcome string, cost float64) runner.PRReviewAttempt {
	return runner.PRReviewAttempt{Model: model, Outcome: outcome, Cost: cost}
}

func TestAPostedReviewWithNoSpecialistRecordsExactlyTheGeneralCalls(t *testing.T) {
	v := verdict(runner.PRReviewClean, "", pricedModel)
	v.Attempts = []runner.PRReviewAttempt{costed(museModel, runner.AttemptTimedOut, 0.001), costed(pricedModel, runner.AttemptReviewed, 0.002)}
	var got []store.ReviewRecord
	postedWith(t, eligiblePull(), v, capturing(&got, nil))
	want := store.ReviewRecord{PR: 7, HeadSHA: headSHA, Risk: "low", Specialists: []string{}, Total: 0.003, Calls: []store.ReviewCall{
		{Role: "general", Model: museModel, Outcome: "timed out", Cost: 0.001},
		{Role: "general", Model: pricedModel, Outcome: "reviewed", Input: 1200, Output: 80, Cost: 0.002},
	}}
	if len(got) != 1 || !jsonEqual(t, got[0], want) {
		t.Fatalf("recorded %+v, want exactly %+v", got, want)
	}
}

func TestAPostedReviewRecordsEachSpecialistsCallsRiskAndNames(t *testing.T) {
	v := withSpecialists(verdict(runner.PRReviewClean, "", pricedModel),
		reviewedBy("data", "the query is unbounded", 0.2),
		missedBy("infra", runner.SpecialistReasonTimedOut, costed(specialistModel, runner.AttemptTimedOut, 0.05)),
		missedBy("cloud", runner.SpecialistReasonDeadline))
	v.Attempts = []runner.PRReviewAttempt{costed(pricedModel, runner.AttemptReviewed, 0.1)}
	var got []store.ReviewRecord
	postedWith(t, eligiblePull(), v, capturing(&got, nil))
	want := store.ReviewRecord{PR: 7, HeadSHA: headSHA, Risk: "high", Specialists: []string{"data", "infra", "cloud"}, Total: 0.35, Calls: []store.ReviewCall{
		{Role: "general", Model: pricedModel, Outcome: "reviewed", Input: 1200, Output: 80, Cost: 0.1},
		{Role: "data", Model: specialistModel, Outcome: "reviewed", Input: 1000, Output: 50, Cost: 0.2},
		{Role: "infra", Model: specialistModel, Outcome: "timed out", Cost: 0.05},
	}}
	if len(got) != 1 || !jsonEqual(t, got[0], want) {
		t.Fatalf("recorded %+v, want %+v", got, want)
	}
	if raw, _ := json.Marshal(got[0]); strings.Contains(string(raw), "unbounded") {
		t.Fatalf("the record carries finding text: %s", raw)
	}
}

func TestAPostedReviewThatSpentItsAllowanceRecordsItsCallsAtNoCost(t *testing.T) {
	f := &fakePulls{pull: openPull("t", "", headSHA, autoMergeEligibleLabel), checkRuns: greenChecks()}
	var got []store.ReviewRecord
	postedWith(t, f, spentAllowance(), capturing(&got, nil))
	want := store.ReviewRecord{PR: 7, HeadSHA: headSHA, Risk: "low", Specialists: []string{}, Calls: []store.ReviewCall{
		{Role: "general", Model: museModel, Outcome: "allowance exhausted"},
		{Role: "general", Model: pricedModel, Outcome: "allowance exhausted"},
	}}
	if len(got) != 1 || !jsonEqual(t, got[0], want) {
		t.Fatalf("recorded %+v, want %+v", got, want)
	}
}

// The record is written after the comment, the status and the merge decision, and
// a write that fails changes none of them.
func TestAFailedRecordWriteStillPostsAndDecidesTheMerge(t *testing.T) {
	f := eligiblePull()
	getenv := f.start(t)
	var seen struct {
		comments, statuses int
		output             string
	}
	record := func(context.Context, store.ReviewRecord) error {
		seen.comments, seen.statuses, seen.output = len(f.created), len(f.statuses), readOutput(t, getenv)
		return errors.New("permission denied")
	}
	args := []string{"-pr", "7", "-reviewed-sha", headSHA, "-verdict", writeVerdict(t, verdict(runner.PRReviewClean, "", museModel)), "-context-result", "success"}
	var logs bytes.Buffer
	if err := prReviewPostWith(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), getenv, switchedOn, rateUnder, record, args); err != nil {
		t.Fatal(err)
	}
	if seen.comments != 1 || seen.statuses != 1 || !strings.Contains(seen.output, "merge=true") {
		t.Fatalf("at the write: %d comments, %d statuses, output %q; want all three already published", seen.comments, seen.statuses, seen.output)
	}
	if got := logs.String(); !strings.Contains(got, "level=WARN") || !strings.Contains(got, "prReviewRecordUnwritten") || !strings.Contains(got, "permission denied") {
		t.Fatalf("logs = %q, want a warning naming prReviewRecordUnwritten and the error", got)
	}
}

func TestAPostWithNoRecorderStillPostsAndDecidesTheMerge(t *testing.T) {
	f := eligiblePull()
	getenv := f.start(t)
	args := []string{"-pr", "7", "-reviewed-sha", headSHA, "-verdict", writeVerdict(t, verdict(runner.PRReviewClean, "", museModel)), "-context-result", "success"}
	if err := prReviewPostWith(context.Background(), quietLogger(), getenv, switchedOn, rateUnder, nil, args); err != nil {
		t.Fatal(err)
	}
	if got := readOutput(t, getenv); !strings.Contains(got, "merge=true") || len(f.created) != 1 {
		t.Fatalf("output %q, %d comments, want the merge decided and the comment posted", got, len(f.created))
	}
}

func TestTheRecordWriteIsBoundedByADeadline(t *testing.T) {
	var deadline time.Time
	var ok bool
	postedWith(t, eligiblePull(), verdict(runner.PRReviewClean, "", museModel), func(ctx context.Context, _ store.ReviewRecord) error {
		deadline, ok = ctx.Deadline()
		return nil
	})
	if !ok || time.Until(deadline) > prReviewRecordTimeout {
		t.Fatalf("deadline %v (set %v), want one within %v", deadline, ok, prReviewRecordTimeout)
	}
}

func TestAPostWithNoReadVerdictRecordsNothing(t *testing.T) {
	for name, tt := range map[string]struct {
		noFile bool
		extra  []string
	}{
		"a skipped review":      {false, []string{"-skip-reason", skipNoTicketID}},
		"a failed context":      {false, []string{"-context-result", "failure"}},
		"a verdict not written": {true, nil},
	} {
		t.Run(name, func(t *testing.T) {
			f := eligiblePull()
			getenv := f.start(t)
			path := writeVerdict(t, verdict(runner.PRReviewClean, "", museModel))
			if tt.noFile {
				path = ""
			}
			args := append([]string{"-pr", "7", "-reviewed-sha", headSHA, "-verdict", path, "-context-result", "success"}, tt.extra...)
			var got []store.ReviewRecord
			if err := prReviewPostWith(context.Background(), quietLogger(), getenv, switchedOn, rateUnder, capturing(&got, nil), args); err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Fatalf("recorded %+v, want nothing", got)
			}
		})
	}
}

// The calls' tokens and dollars sum to what the comment's footer prints.
func TestTheRecordAgreesWithTheFooter(t *testing.T) {
	v := withSpecialists(verdict(runner.PRReviewClean, "", pricedModel), reviewedBy("data", "", 0.2))
	v.Attempts = []runner.PRReviewAttempt{costed(pricedModel, runner.AttemptReviewed, 0.1)}
	rec := reviewRecordOf(7, headSHA, v)
	usage, cost, attempts := reviewTotals(v)
	var in, out int64
	for _, c := range rec.Calls {
		in, out = in+c.Input, out+c.Output
	}
	if in != usage.Input || out != usage.Output || len(rec.Calls) != attempts || rec.Total != cost {
		t.Fatalf("record %d in / %d out / %d calls / $%v, footer %+v / %d attempts / $%v", in, out, len(rec.Calls), rec.Total, usage, attempts, cost)
	}
}

// The reader accepts at most runner.MaxPRReviewCalls calls, so a review at every
// cap the verdict reader allows must produce a record of exactly that many.
func TestTheWorstVerdictTheReaderAcceptsYieldsTheMostCallsTheRecordTakes(t *testing.T) {
	attempts := func(model string) []runner.PRReviewAttempt {
		var out []runner.PRReviewAttempt
		for range 16 {
			out = append(out, runner.PRReviewAttempt{Model: model, Outcome: runner.AttemptTimedOut})
		}
		return out
	}
	v := noVerdict(attempts(museModel)...)
	for _, a := range runner.ReviewAreas() {
		v.Specialists = append(v.Specialists, missedBy(a.Name, runner.SpecialistReasonTimedOut, attempts(specialistModel)...))
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := runner.ParsePRVerdict(raw)
	if err != nil {
		t.Fatalf("the worst verdict is refused by its own reader: %v", err)
	}
	if got := len(reviewRecordOf(7, headSHA, parsed).Calls); got != runner.MaxPRReviewCalls() {
		t.Fatalf("%d calls, want the %d the record's cap allows", got, runner.MaxPRReviewCalls())
	}
}

// jsonEqual compares two records by the JSON they marshal to, which ignores the
// time the store stamps.
func jsonEqual(t *testing.T, got, want store.ReviewRecord) bool {
	t.Helper()
	got.RecordedAt, want.RecordedAt = time.Time{}, time.Time{}
	a, errA := json.Marshal(got)
	b, errB := json.Marshal(want)
	if errA != nil || errB != nil {
		t.Fatal(errA, errB)
	}
	if !slices.Equal(a, b) {
		t.Logf("got  %s\nwant %s", a, b)
		return false
	}
	return true
}

func TestStoredReviewRecorderRefusesAMissingProject(t *testing.T) {
	err := storedReviewRecorder(func(string) string { return "" })(context.Background(), reviewRecordOf(7, headSHA, verdict(runner.PRReviewClean, "", museModel)))
	if err == nil || !strings.Contains(err.Error(), "GOOGLE_CLOUD_PROJECT") {
		t.Fatalf("err = %v, want the missing project named", err)
	}
}

// The real recorder writes the document under the pull request and head the
// review named, against the emulator.
func TestStoredReviewRecorderWritesTheDocument(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "forge-wingman-test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	doc := client.Collection("pr_reviews").Doc("7005-" + headSHA)
	t.Cleanup(func() { _, _ = doc.Delete(ctx) })
	getenv := func(k string) string {
		if k == "GOOGLE_CLOUD_PROJECT" {
			return "forge-wingman-test"
		}
		return ""
	}
	if err := storedReviewRecorder(getenv)(ctx, reviewRecordOf(7005, headSHA, verdict(runner.PRReviewClean, "", museModel))); err != nil {
		t.Fatal(err)
	}
	snap, err := doc.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if row := snap.Data(); row["pr"] != int64(7005) || row["call_count"] != int64(1) {
		t.Fatalf("stored %v, want the review of 7005 with one call", row)
	}
}
