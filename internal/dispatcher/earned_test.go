package dispatcher

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// fakeEarned holds the snapshot a test seeds and records what is stored.
type fakeEarned struct {
	snap   Earned
	found  bool
	err    error
	setErr error
	stored []Earned
	staled int
}

func (e *fakeEarned) Earned(context.Context) (Earned, bool, error) {
	return e.snap, e.found, e.err
}

func (e *fakeEarned) SetEarned(_ context.Context, s Earned) error {
	if e.setErr != nil {
		return e.setErr
	}
	e.stored = append(e.stored, s)
	e.snap, e.found = s, true
	return nil
}

func (e *fakeEarned) MarkStale(context.Context) error {
	e.staled++
	e.snap.Stale = true
	return nil
}

// fakeShadow holds when the shadow began and how many merges it has counted.
type fakeShadow struct {
	start time.Time
	count int
	err   error
}

func (s *fakeShadow) ShadowStart(context.Context, time.Time) (time.Time, error) {
	return s.start, s.err
}

func (s *fakeShadow) CountShadow(context.Context) (int, error) {
	s.count++
	return s.count, s.err
}

// fakeCDs reports the CD flags configured.
type fakeCDs struct {
	flags map[string]bool
	err   error
}

func (c fakeCDs) Recorded(context.Context) (map[string]bool, error) { return c.flags, c.err }

// fakeHistory records the changes appended.
type fakeHistory struct {
	changes []Change
	err     error
}

func (h *fakeHistory) Append(_ context.Context, c Change) error {
	if h.err != nil {
		return h.err
	}
	h.changes = append(h.changes, c)
	return nil
}

// freshEarned is a snapshot of level computed at the fixture clock.
func freshEarned(level Autonomy) *fakeEarned {
	return &fakeEarned{snap: Earned{Level: level, ComputedAt: mergedAt}, found: true}
}

func TestReworkTrailerSplitsTheOwnersLinesThreeWays(t *testing.T) {
	commits := []Commit{
		{Name: "the owner", Email: "owner@example.com", Additions: 6, Message: "fix: x\n\nRework: review-found\n"},
		{Name: "the owner", Email: "owner@example.com", Additions: 3, Message: "tweak\r\n\r\nRework: Owner-Change\r\n"},
		{Name: "the owner", Email: "owner@example.com", Additions: 1, Message: "no trailer"},
		{Name: "the owner", Email: "owner@example.com", Additions: 2, Message: "Rework: review-found and more"},
	}
	got := RewriteOf(commits)
	want := Rewrite{OwnerLines: 12, ReviewFoundLines: 6, OwnerChangeLines: 3, UnlabelledLines: 3}
	if got != want {
		t.Fatalf("RewriteOf = %+v, want %+v", got, want)
	}
}

func TestNumbersCarryThePRsAndTheOwnerLinesByLabel(t *testing.T) {
	a := Sample{PR: 7, Rewrite: Rewrite{AgentLines: 100, OwnerLines: 10, ReviewFoundLines: 4, OwnerChangeLines: 5, UnlabelledLines: 1}}
	b := Sample{PR: 6, AutoMerged: true, Reverted: true, Rewrite: Rewrite{AgentLines: 100}}
	got := NumbersOf([]Sample{a, b})
	if len(got.PRs) != 2 || got.PRs[0] != 7 || got.PRs[1] != 6 || got.Rate != 0.05 || got.AgentLines != 200 ||
		got.OwnerLines != 10 || got.ReviewFoundLines != 4 || got.OwnerChangeLines != 5 || got.UnlabelledLines != 1 || got.Reverts != 1 {
		t.Fatalf("NumbersOf = %+v, want both PRs and the lines split by label", got)
	}
}

func TestEarnedLevelIsTheHighestWhoseConditionHolds(t *testing.T) {
	for name, tt := range map[string]struct {
		ev   Evidence
		want Autonomy
	}{
		"ten clean PRs earn four":            {Evidence{Level: 3, Trailing: samples(scratch, 10, 100, 0), CD: cdOnly}, 4},
		"no CD recorded earns three":         {Evidence{Level: 3, Trailing: samples(scratch, 10, 100, 0)}, 3},
		"a rate over the advance rate":       {Evidence{Level: 3, Trailing: samples(scratch, 10, 100, 10), CD: cdOnly}, 3},
		"twenty auto-merged PRs earn five":   {Evidence{Level: 4, Trailing: samples(scratch, 20, 100, 0), CD: cdOnly}, 5},
		"a hand-merged PR holds five back":   {Evidence{Level: 4, Trailing: joined(samples(scratch, 19, 100, 0), []Sample{sample(scratch, 100, 0, false, false)}), CD: cdOnly}, 4},
		"a revert in the window earns three": {Evidence{Level: 4, Trailing: joined(samples(scratch, 19, 100, 0), []Sample{sample(scratch, 100, 0, true, true)}), CD: cdOnly}, 3},
	} {
		t.Run(name, func(t *testing.T) {
			if got, _ := EarnedLevel(tt.ev); got != tt.want {
				t.Fatalf("EarnedLevel = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestEffectiveLevelIsTheLowerOfRecordedAndEarned(t *testing.T) {
	for name, tt := range map[string]struct {
		recorded Autonomy
		earned   *fakeEarned
		want     Autonomy
	}{
		"earned lower":                {5, freshEarned(4), 4},
		"a stored level above earned": {4, freshEarned(3), 3},
		"recorded lower":              {4, freshEarned(5), 4},
		"a level below auto-merge":    {3, &fakeEarned{err: errFirestore}, 3},
		"no snapshot":                 {4, &fakeEarned{}, 3},
		"an unreadable snapshot":      {5, &fakeEarned{err: errFirestore}, 3},
		"an invalid snapshot":         {4, &fakeEarned{snap: Earned{Level: 9, ComputedAt: mergedAt}, found: true}, 3},
		"a stale snapshot at four":    {4, &fakeEarned{snap: Earned{Level: 5, ComputedAt: mergedAt, Stale: true}, found: true}, 3},
	} {
		t.Run(name, func(t *testing.T) {
			deps, _ := rewriteDeps(&fakePullRequests{}, &fakeRewrites{})
			deps.Earned = tt.earned
			if got := effectiveLevel(context.Background(), deps, tt.recorded); got != tt.want {
				t.Fatalf("effectiveLevel = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestPollRunsAtTheLowerOfRecordedAndEarned(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/scratch", Size: "M"}}}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	deps.Autonomies = &fakeAutonomies{level: 4}
	deps.Earned = &fakeEarned{}
	if _, err := Poll(context.Background(), deps, buildConfig); err != nil {
		t.Fatal(err)
	}
	if len(q.tryClaims) != 1 || q.tryClaims[0].facts.Autonomy != 3 {
		t.Fatalf("tryClaims = %+v, want the claim capped at level 3 with no snapshot", q.tryClaims)
	}
}

// advanceDeps is deps over ten clean merged PRs at level 3 with CD recorded.
func advanceDeps(t *testing.T, logs *bytes.Buffer) (Deps, *fakeAutonomies, *fakeEarned, *fakeHistory, *fakeNotices) {
	t.Helper()
	rows := &fakeRewrites{recent: samples(scratch, 10, 100, 2)}
	for i := range rows.recent {
		rows.recent[i].PR = 100 - i
	}
	deps, notices := rewriteDeps(&fakePullRequests{}, rows)
	autonomies, earned, hist := &fakeAutonomies{level: 3}, &fakeEarned{}, &fakeHistory{}
	deps.Autonomies, deps.Earned, deps.History, deps.CDs = autonomies, earned, hist, fakeCDs{flags: cdOnly}
	deps.Logger = slog.New(slog.NewTextHandler(logs, nil))
	return deps, autonomies, earned, hist, notices
}

func TestAdvanceAfterTheShadowRaisesOneNoticeAndOneHistoryEntry(t *testing.T) {
	var logs bytes.Buffer
	deps, autonomies, earned, hist, notices := advanceDeps(t, &logs)
	if err := refreshEarned(context.Background(), deps, 3, []freshMerge{{PR: 101, Ordinal: DefaultShadowPRs + 1}}); err != nil {
		t.Fatal(err)
	}
	if len(autonomies.set) != 1 || autonomies.set[0] != 4 {
		t.Fatalf("set = %v, want one advance to 4", autonomies.set)
	}
	if len(earned.stored) != 1 || earned.stored[0].Level != 4 || !earned.stored[0].ComputedAt.Equal(mergedAt) ||
		len(earned.stored[0].Numbers.PRs) != 10 || earned.stored[0].Numbers.OwnerLines != 20 {
		t.Fatalf("stored = %+v, want a level 4 snapshot with the PR numbers and lines", earned.stored)
	}
	if len(hist.changes) != 1 || hist.changes[0].From != 3 || hist.changes[0].To != 4 || hist.changes[0].Cause != ChangeAdvance ||
		hist.changes[0].Numbers.AgentLines != 1000 {
		t.Fatalf("history = %+v, want the advance recorded with its numbers", hist.changes)
	}
	if len(notices.raised) != 1 || !strings.Contains(notices.raised[0].Class, "level 3 to 4") ||
		!strings.Contains(notices.raised[0].Class, "2.0% over 10 PRs") || !strings.Contains(notices.raised[0].Class, "20 owner lines of 1000 agent lines") {
		t.Fatalf("raised = %+v, want one notice naming the level and the numbers", notices.raised)
	}
	if strings.Contains(logs.String(), "autonomyWouldAdvance") {
		t.Fatalf("logs = %q, want no would-advance past the shadow", logs.String())
	}
}

func TestAdvanceInsideTheShadowOnlyLogsWhatItWouldDo(t *testing.T) {
	var logs bytes.Buffer
	deps, autonomies, _, hist, notices := advanceDeps(t, &logs)
	if err := refreshEarned(context.Background(), deps, 3, []freshMerge{{PR: 101, Ordinal: DefaultShadowPRs}}); err != nil {
		t.Fatal(err)
	}
	if len(autonomies.set) != 0 || len(hist.changes) != 0 || len(notices.raised) != 0 {
		t.Fatalf("set = %v, history = %+v, raised = %+v, want nothing changed inside the shadow", autonomies.set, hist.changes, notices.raised)
	}
	for _, want := range []string{"autonomyWouldAdvance", "level=4", "pr=101", "shadowPR=10", "rate=0.02", "ownerLines=20"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs = %q, want %q", logs.String(), want)
		}
	}
}

func TestAdvanceIgnoresAMergeThatCountsTowardNothing(t *testing.T) {
	var logs bytes.Buffer
	deps, autonomies, _, hist, notices := advanceDeps(t, &logs)
	if err := refreshEarned(context.Background(), deps, 3, []freshMerge{{PR: 101}}); err != nil {
		t.Fatal(err)
	}
	if len(autonomies.set) != 0 || len(hist.changes) != 0 || len(notices.raised) != 0 {
		t.Fatalf("set = %v, want a merge with no shadow ordinal to advance nothing", autonomies.set)
	}
	if strings.Contains(logs.String(), "autonomyWouldAdvance") {
		t.Fatalf("logs = %q, want a merge outside the shadow not logged as one inside it", logs.String())
	}
}

// shadowMergeDeps is deps with one merged runner PR at 2026-10-14 03:00 to measure,
// ten clean PRs already measured and CD recorded, at level 3 with the shadow count
// at count.
func shadowMergeDeps(shadow *fakeShadow) (Deps, *fakeAutonomies) {
	prs := &fakePullRequests{
		closed:  map[string][]ClosedPR{scratch: {{Number: 7, Head: runBranch, MergedAt: mergedAt}}},
		commits: map[int][]Commit{7: {{Name: runner.CommitAuthorName, Email: runner.CommitAuthorEmail, Additions: 100}}},
	}
	rows := &fakeRewrites{rows: map[string]RunRow{runBranch: {RunID: runOnBranch}}, recent: samples(scratch, 10, 100, 2)}
	deps, _ := rewriteDeps(prs, rows)
	autonomies := &fakeAutonomies{level: 3}
	deps.Autonomies, deps.Earned, deps.CDs, deps.Shadow = autonomies, &fakeEarned{}, fakeCDs{flags: cdOnly}, shadow
	return deps, autonomies
}

func TestShadowFailsClosedWhenItsStartIsUnknown(t *testing.T) {
	for name, tt := range map[string]struct {
		shadow *fakeShadow
		want   int
	}{
		"past the shadow":             {&fakeShadow{start: mergedAt.Add(-time.Hour), count: DefaultShadowPRs}, 1},
		"inside the shadow":           {&fakeShadow{start: mergedAt.Add(-time.Hour)}, 0},
		"a start that cannot be read": {&fakeShadow{err: errFirestore, count: DefaultShadowPRs}, 0},
		"no start recorded":           {&fakeShadow{count: DefaultShadowPRs}, 0},
		"a merge before the start":    {&fakeShadow{start: mergedAt.Add(time.Hour), count: DefaultShadowPRs}, 0},
	} {
		t.Run(name, func(t *testing.T) {
			deps, autonomies := shadowMergeDeps(tt.shadow)
			_ = measureRewrites(context.Background(), deps, oneRepo)
			if len(autonomies.set) != tt.want {
				t.Fatalf("set = %v, want %d advances", autonomies.set, tt.want)
			}
		})
	}
}

func TestUnmeasuredCommitWithholdsTheAdvanceButNotTheRate(t *testing.T) {
	commits := []Commit{
		{Name: runner.CommitAuthorName, Email: runner.CommitAuthorEmail, Additions: 100},
		{Name: "the owner", Email: "owner@example.com", Unmeasured: true},
	}
	got := RewriteOf(commits)
	if got.UnmeasuredCommits != 1 || got.AgentLines != 100 || got.OwnerLines != 0 {
		t.Fatalf("RewriteOf = %+v, want one unmeasured commit and the measured lines kept", got)
	}
	unmeasured := Sample{Repo: scratch, AutoMerged: true, Rewrite: got}
	ev := Evidence{Level: 3, Trailing: joined([]Sample{unmeasured}, samples(scratch, 9, 100, 0)), CD: cdOnly}
	if err := Autonomy(4).Settable(ev); !errors.Is(err, ErrAutonomyNotEarned) || !strings.Contains(err.Error(), "1 PRs with unmeasured commits") {
		t.Fatalf("Settable(4) = %v, want a window with an unmeasured PR withheld", err)
	}
	if level, _ := EarnedLevel(ev); level != 3 {
		t.Fatalf("EarnedLevel = %d, want 3", level)
	}
	deps, autonomies, _, _, _ := advanceDeps(t, &bytes.Buffer{})
	deps.Rewrites = &fakeRewrites{recent: ev.Trailing}
	if err := refreshEarned(context.Background(), deps, 3, []freshMerge{{PR: 1, Ordinal: 11}}); err != nil {
		t.Fatal(err)
	}
	if len(autonomies.set) != 0 {
		t.Fatalf("set = %v, want no advance over an unmeasured PR", autonomies.set)
	}
	// A high rate beside an unmeasured commit still rolls a level back.
	high := Evidence{Level: 4, Trailing: joined([]Sample{unmeasured}, samples(scratch, 19, 100, 30))}
	if RollbackFor(4, high) == "" {
		t.Fatal("RollbackFor = \"\", want the measured rate to still roll back")
	}
}

func TestAdvanceNeedsTheConditionAndLevelThree(t *testing.T) {
	for name, tt := range map[string]struct {
		level   Autonomy
		samples []Sample
	}{
		"a rate over the advance rate":   {3, samples(scratch, 10, 100, 10)},
		"level four stays for the owner": {4, samples(scratch, 20, 100, 0)},
		"a level below three":            {2, samples(scratch, 10, 100, 0)},
	} {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			deps, autonomies, _, _, _ := advanceDeps(t, &logs)
			deps.Rewrites = &fakeRewrites{recent: tt.samples}
			if err := refreshEarned(context.Background(), deps, tt.level, []freshMerge{{PR: 1, Ordinal: 11}}); err != nil {
				t.Fatal(err)
			}
			if len(autonomies.set) != 0 {
				t.Fatalf("set = %v, want no advance", autonomies.set)
			}
		})
	}
}

func TestPollCountsOnlyMergesAfterTheShadowBegan(t *testing.T) {
	prs := &fakePullRequests{
		closed: map[string][]ClosedPR{scratch: {
			{Number: 7, Head: runBranch, MergedAt: mergedAt},
			{Number: 6, Head: runner.BranchPrefix + "FRG-1-9-1", MergedAt: mergedAt.Add(-2 * time.Hour)},
		}},
		commits: map[int][]Commit{7: {{Name: "o", Email: "o@example.com", Additions: 1}}, 6: {{Name: "o", Email: "o@example.com", Additions: 1}}},
	}
	rows := &fakeRewrites{rows: map[string]RunRow{
		runBranch:                         {RunID: "1"},
		runner.BranchPrefix + "FRG-1-9-1": {RunID: "2"},
	}}
	deps, _ := rewriteDeps(prs, rows)
	shadow := &fakeShadow{start: mergedAt.Add(-time.Hour)}
	deps.Shadow = shadow
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	if shadow.count != 1 {
		t.Fatalf("count = %d, want only the merge after the shadow began counted", shadow.count)
	}
}

func TestRecomputeHappensOnlyAfterAMergeOrARollback(t *testing.T) {
	deps, _ := rewriteDeps(&fakePullRequests{}, &fakeRewrites{recent: samples(scratch, 10, 100, 0)})
	earned := &fakeEarned{}
	deps.Earned, deps.Autonomies = earned, &fakeAutonomies{level: 3}
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	if len(earned.stored) != 0 {
		t.Fatalf("stored = %+v, want a poll with no new merge to recompute nothing", earned.stored)
	}
}

func TestFailedRecomputeMarksTheSnapshotStaleAtOnce(t *testing.T) {
	for name, tt := range map[string]struct {
		rows   *fakeRewrites
		cds    fakeCDs
		earned *fakeEarned
	}{
		"samples": {&fakeRewrites{recentErr: errFirestore}, fakeCDs{}, &fakeEarned{}},
		"cd":      {&fakeRewrites{}, fakeCDs{err: errFirestore}, &fakeEarned{}},
		"store":   {&fakeRewrites{}, fakeCDs{}, &fakeEarned{setErr: errFirestore}},
	} {
		t.Run(name, func(t *testing.T) {
			deps, _ := rewriteDeps(&fakePullRequests{}, tt.rows)
			deps.Earned, deps.CDs = tt.earned, tt.cds
			err := refreshEarned(context.Background(), deps, 4, []freshMerge{{PR: 1}})
			if !errors.Is(err, errFirestore) {
				t.Fatalf("err = %v, want the failure reported", err)
			}
			if tt.earned.staled != 1 {
				t.Fatalf("staled = %d, want the snapshot marked stale", tt.earned.staled)
			}
			if got := effectiveLevel(context.Background(), deps, 4); got != 3 {
				t.Fatalf("effectiveLevel = %d, want a stale snapshot to cap the level at 3", got)
			}
		})
	}
}

func TestRollbackAppendsToTheHistory(t *testing.T) {
	rows := &fakeRewrites{recent: joined(samples(scratch, 19, 100, 0), []Sample{sample(scratch, 100, 0, true, true)})}
	deps, _ := rewriteDeps(&fakePullRequests{}, rows)
	hist := &fakeHistory{}
	deps.Autonomies, deps.History = &fakeAutonomies{level: 4}, hist
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	if len(hist.changes) != 1 {
		t.Fatalf("history = %+v, want one change", hist.changes)
	}
	c := hist.changes[0]
	if c.From != 4 || c.To != 3 || c.Cause != string(RollbackReverted) || !c.At.Equal(mergedAt) ||
		c.Numbers.Reverts != 1 || c.Numbers.AgentLines != 2000 {
		t.Fatalf("change = %+v, want the reverted-auto-merge rollback with its numbers", c)
	}
}

func TestRollbackStillRaisesItsNoticeWhenTheHistoryFails(t *testing.T) {
	rows := &fakeRewrites{recent: joined(samples(scratch, 19, 100, 0), []Sample{sample(scratch, 100, 0, true, true)})}
	deps, notices := rewriteDeps(&fakePullRequests{}, rows)
	deps.Autonomies, deps.History = &fakeAutonomies{level: 4}, &fakeHistory{err: errFirestore}
	if err := measureRewrites(context.Background(), deps, oneRepo); !errors.Is(err, errFirestore) {
		t.Fatalf("err = %v, want the history failure reported", err)
	}
	if len(notices.raised) != 1 {
		t.Fatalf("raised = %+v, want the notice raised regardless", notices.raised)
	}
}
