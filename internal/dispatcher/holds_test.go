package dispatcher

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeHolds records each hold a poll wrote.
type fakeHolds struct {
	recorded []heldCall
	err      error
}

type heldCall struct {
	run, reason, day string
	at               time.Time
}

func (f *fakeHolds) RecordHold(_ context.Context, runID, reason, day string, at time.Time) error {
	f.recorded = append(f.recorded, heldCall{runID, reason, day, at})
	return f.err
}

func TestHoldReasonCountsEachConditionUnderItsReason(t *testing.T) {
	budget := BudgetConfig{
		ProviderWindows: []Window{{Name: "5h"}},
		ModelCaps:       map[string]Window{"plan/model": {Name: "model-month"}},
	}
	for _, tt := range []struct{ binding, want string }{
		{ConditionConcurrency, HoldConcurrency},
		{ConditionRepoCap, HoldRepoBusy},
		{ConditionRepoOverlap, HoldRepoBusy},
		{ConditionReviewWIP, HoldReviewWIP},
		{ConditionLargeCap, HoldLargeCap},
		{ConditionBlocked, HoldBlocked},
		{CeilingCash, HoldBudget},
		{CeilingRunnerMinutes, HoldBudget},
		{"5h", HoldBudget},
		{"model-month", HoldBudget},
		{ConditionPlatformCap, HoldJobLimit},
		{ConditionCircuitBreaker, HoldBreaker},
		{ConditionOwnerPaused, HoldPaused},
		{ConditionPeakHours, HoldPeakHours},
		{CeilingProviderHalted, HoldProviderDown},
		{ConditionEstimateFailed, HoldNoEstimate},
		// A name no condition carries counts nothing.
		{"a-window-of-another-plan", ""},
	} {
		if got := holdReason(tt.binding, budget); got != tt.want {
			t.Errorf("holdReason(%q) = %q, want %q", tt.binding, got, tt.want)
		}
	}
}

// pollHolding polls once with candidates, each withheld on the binding bindings
// names, and returns what was recorded and what was logged.
func pollHolding(t *testing.T, holds *fakeHolds, bindings map[string]string, candidates ...Candidate) (Result, string) {
	t.Helper()
	q := &fakeQueue{candidates: candidates, bindings: bindings}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	deps.Holds = holds
	var logs bytes.Buffer
	deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	res, err := Poll(context.Background(), deps, buildConfig)
	if err != nil {
		t.Fatal(err)
	}
	return res, logs.String()
}

func TestPollRecordsAHeldRunUnderItsReasonAndDay(t *testing.T) {
	holds := &fakeHolds{}
	pollHolding(t, holds, map[string]string{"FRG-18": ConditionRepoOverlap, "FRG-19": CeilingCash},
		Candidate{RunID: "FRG-18", Repo: "octo/scratch", Size: "M", Priority: 1},
		Candidate{RunID: "FRG-19", Repo: "octo/scratch", Size: "M", Priority: 2})
	want := []heldCall{
		{"FRG-18", HoldRepoBusy, "2026-09-27", pollAt},
		{"FRG-19", HoldBudget, "2026-09-27", pollAt},
	}
	if !slices.Equal(holds.recorded, want) {
		t.Fatalf("recorded %+v, want %+v", holds.recorded, want)
	}
}

func TestPollRecordsAHoldOncePerRunReasonAndDay(t *testing.T) {
	for _, tt := range []struct {
		name  string
		held  map[string][]string
		write bool
	}{
		{"nothing recorded yet", nil, true},
		{"the reason already recorded today", map[string][]string{"2026-09-27": {HoldBlocked, HoldConcurrency}}, false},
		{"the reason recorded only yesterday", map[string][]string{"2026-09-26": {HoldConcurrency}}, true},
		{"another reason recorded today", map[string][]string{"2026-09-27": {HoldReviewWIP}}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			holds := &fakeHolds{}
			pollHolding(t, holds, map[string]string{"FRG-18": ConditionConcurrency},
				Candidate{RunID: "FRG-18", Repo: "octo/scratch", Size: "M", Held: tt.held})
			if wrote := len(holds.recorded) == 1; wrote != tt.write || len(holds.recorded) > 1 {
				t.Fatalf("recorded %+v, want a write: %t", holds.recorded, tt.write)
			}
		})
	}
}

func TestPollRecordsNoHoldForANameNoConditionCarries(t *testing.T) {
	holds := &fakeHolds{}
	res, _ := pollHolding(t, holds, map[string]string{"FRG-18": "a-window-of-another-plan"},
		Candidate{RunID: "FRG-18", Repo: "octo/scratch", Size: "M"})
	if len(res.Deferrals) != 1 {
		t.Fatalf("deferrals = %+v, want the run deferred", res.Deferrals)
	}
	if len(holds.recorded) != 0 {
		t.Fatalf("recorded %+v, want nothing", holds.recorded)
	}
}

func TestPollRecordsAHoldOnTheEstimateItCouldNotRead(t *testing.T) {
	for _, tt := range []struct {
		name      string
		estimator fakeEstimator
		twoStage  bool
	}{
		{"the run's estimate", fakeEstimator{err: errors.New("unavailable")}, false},
		{"the plan stage's estimate", fakeEstimator{
			bySize:  map[string]Estimate{"M": {Minutes: 30}},
			planErr: errors.New("unavailable"),
		}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			holds := &fakeHolds{}
			deps := pollDeps(fakeSource{}, &fakeQueue{candidates: []Candidate{{RunID: "FRG-18", Size: "M"}}}, &fakeWorkflow{})
			deps.Holds, deps.Estimator = holds, tt.estimator
			cfg := buildConfig
			cfg.TwoStage = tt.twoStage
			if _, err := Poll(context.Background(), deps, cfg); err != nil {
				t.Fatal(err)
			}
			want := []heldCall{{"FRG-18", HoldNoEstimate, "2026-09-27", pollAt}}
			if !slices.Equal(holds.recorded, want) {
				t.Fatalf("recorded %+v, want %+v", holds.recorded, want)
			}
		})
	}
}

func TestPollRecordsNoHoldForAClaimedRun(t *testing.T) {
	holds := &fakeHolds{}
	res, _ := pollHolding(t, holds, nil, Candidate{RunID: "FRG-18", Repo: "octo/scratch", Size: "M"})
	if !slices.Equal(res.Dispatched, []string{"FRG-18"}) || len(holds.recorded) != 0 {
		t.Fatalf("dispatched %v, recorded %+v; want the run dispatched and no hold", res.Dispatched, holds.recorded)
	}
}

func TestPollGoesOnWhenAHoldCannotBeRecorded(t *testing.T) {
	holds := &fakeHolds{err: errors.New("firestore unavailable")}
	res, logs := pollHolding(t, holds, map[string]string{"FRG-18": ConditionReviewWIP},
		Candidate{RunID: "FRG-18", Repo: "octo/scratch", Size: "M", Priority: 1},
		Candidate{RunID: "FRG-19", Repo: "octo/scratch", Size: "M", Priority: 2})
	if !slices.Equal(res.Dispatched, []string{"FRG-19"}) {
		t.Fatalf("dispatched %v, want FRG-19 despite the failed write", res.Dispatched)
	}
	if !strings.Contains(logs, "msg=holdNotRecorded") || !strings.Contains(logs, "reason=review-wip") {
		t.Fatalf("logs = %s, want holdNotRecorded naming the reason", logs)
	}
}

func TestRankHoldsListsLargestFirstWithoutZeros(t *testing.T) {
	got := RankHolds(map[string]int{
		HoldBudget: 0, HoldBlocked: 2, HoldConcurrency: 14, HoldReviewWIP: 6,
		HoldRepoBusy: 6, HoldLargeCap: 1, "unknown": 9,
		HoldNoEstimate: 2, HoldBreaker: 6, HoldJobLimit: 6, HoldPaused: 0,
	})
	want := []HeldCount{
		{HoldConcurrency, 14},
		// A tie keeps the reasons' own order: the ticket's six, then the rest.
		{HoldRepoBusy, 6}, {HoldReviewWIP, 6}, {HoldJobLimit, 6}, {HoldBreaker, 6},
		{HoldBlocked, 2}, {HoldNoEstimate, 2},
		{HoldLargeCap, 1},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("RankHolds = %+v, want %+v", got, want)
	}
	if got := RankHolds(nil); got == nil || len(got) != 0 {
		t.Fatalf("RankHolds(nil) = %#v, want an empty list", got)
	}
}

// sydney is the zone holds are counted in, loaded apart from the code's own.
func sydney(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Australia/Sydney")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestHoldDayIsTheSydneyDate(t *testing.T) {
	syd := sydney(t)
	for _, tt := range []struct {
		name string
		at   time.Time
		want string
	}{
		{"23:30 in Sydney", time.Date(2026, 11, 3, 23, 30, 0, 0, syd), "2026-11-03"},
		{"00:30 the next Sydney morning", time.Date(2026, 11, 4, 0, 30, 0, 0, syd), "2026-11-04"},
		// 23:30 UTC on the 3rd is 10:30 on the 4th in Sydney, and 00:30 UTC on
		// the 4th is 11:30 the same Sydney morning: the UTC date changes between
		// them, the Sydney one does not.
		{"23:30 UTC", time.Date(2026, 11, 3, 23, 30, 0, 0, time.UTC), "2026-11-04"},
		{"00:30 UTC an hour later", time.Date(2026, 11, 4, 0, 30, 0, 0, time.UTC), "2026-11-04"},
	} {
		if got := HoldDay(tt.at); got != tt.want {
			t.Errorf("%s: HoldDay = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestPollRecordsAHoldOnTheSydneyDay(t *testing.T) {
	// 23:30 UTC on 27 September is 09:30 on the 28th in Sydney.
	holds := &fakeHolds{}
	q := &fakeQueue{candidates: []Candidate{{RunID: "FRG-18", Repo: "octo/scratch", Size: "M",
		Held: map[string][]string{"2026-09-27": {HoldConcurrency}}}}, bindings: map[string]string{"FRG-18": ConditionConcurrency}}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	at := time.Date(2026, 9, 27, 23, 30, 0, 0, time.UTC)
	deps.Holds, deps.Now = holds, func() time.Time { return at }
	if _, err := Poll(context.Background(), deps, buildConfig); err != nil {
		t.Fatal(err)
	}
	if want := []heldCall{{"FRG-18", HoldConcurrency, "2026-09-28", at}}; !slices.Equal(holds.recorded, want) {
		t.Fatalf("recorded %+v, want %+v", holds.recorded, want)
	}
}

func TestHoldWindowStartIsSydneyMidnight29DaysBeforeToday(t *testing.T) {
	syd := sydney(t)
	for _, tt := range []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{"late on a Sydney evening", time.Date(2026, 11, 3, 23, 30, 0, 0, syd), time.Date(2026, 10, 5, 0, 0, 0, 0, syd)},
		// 13:30 UTC on the 3rd is already the 4th in Sydney.
		{"a UTC afternoon that is the next Sydney morning", time.Date(2026, 11, 3, 13, 30, 0, 0, time.UTC), time.Date(2026, 10, 6, 0, 0, 0, 0, syd)},
		// Daylight saving began on 4 October 2026, so the window spans a 23-hour day.
		{"across the start of daylight saving", time.Date(2026, 10, 20, 12, 0, 0, 0, syd), time.Date(2026, 9, 21, 0, 0, 0, 0, syd)},
	} {
		got := HoldWindowStart(tt.now)
		if !got.Equal(tt.want) {
			t.Errorf("%s: HoldWindowStart = %v, want %v", tt.name, got, tt.want)
		}
		if HoldDay(got) != tt.want.Format(time.DateOnly) {
			t.Errorf("%s: the window opens on %s, want %s", tt.name, HoldDay(got), tt.want.Format(time.DateOnly))
		}
	}
}
