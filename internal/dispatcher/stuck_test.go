package dispatcher

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

func stuckDeps(runs ...Stuck) (Deps, *fakeQueue, *fakeNotices) {
	q, n := &fakeQueue{stuck: runs}, &fakeNotices{}
	d := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	d.Notices = n
	return d, q, n
}

var stoppedRun = Stuck{RunID: "ABC-7", Ticket: "ABC-7", State: StuckStopped, Reason: string(runner.StopAgentExit)}

func TestNoticeStuckNamesTheRunStateReasonAndTheRequeueCommand(t *testing.T) {
	d, q, n := stuckDeps(stoppedRun)
	if err := noticeStuck(context.Background(), d, nil); err != nil {
		t.Fatal(err)
	}
	if len(n.raised) != 1 {
		t.Fatalf("raised %d notices, want 1", len(n.raised))
	}
	text := n.raised[0].Text()
	for _, want := range []string{"ABC-7", "stopped", "agent-exit", "runner requeue -run-id ABC-7"} {
		if !strings.Contains(text, want) {
			t.Errorf("notice %q lacks %q", text, want)
		}
	}
	if got := q.stuckWindow[0]; !got.Equal(pollAt.Add(-DefaultStuckRunAfter)) {
		t.Errorf("before = %v, want the limit back from now", got)
	}
	if got := q.stuckWindow[1]; !got.Equal(pollAt.Add(-DefaultStuckLookback)) {
		t.Errorf("since = %v, want the lookback from now", got)
	}
}

func TestNoticeStuckRaisesNothingWhenTheStoreListsNoRun(t *testing.T) {
	d, _, n := stuckDeps()
	if err := noticeStuck(context.Background(), d, nil); err != nil {
		t.Fatal(err)
	}
	if len(n.raised) != 0 {
		t.Fatalf("raised %+v, want none", n.raised)
	}
}

func TestNoticeStuckNamesAQueuedRunsOpenBlockerAndItsState(t *testing.T) {
	run := Stuck{RunID: "ABC-8", Ticket: "ABC-8", State: StuckQueued, Reason: ConditionBlocked}
	issue := Issue{ID: "ABC-8", URL: "https://linear.app/t/issue/ABC-8", BlockedByOpen: []string{"ABC-9"}, BlockerStates: map[string]string{"ABC-9": "started"}}
	d, _, n := stuckDeps(run)
	if err := noticeStuck(context.Background(), d, []Issue{issue}); err != nil {
		t.Fatal(err)
	}
	text := n.raised[0].Text()
	if !strings.Contains(text, "blocked by ABC-9 (started)") || !strings.HasSuffix(text, issue.URL) {
		t.Fatalf("notice = %q", text)
	}
}

func TestNoticeStuckIDChangesWithTheReasonAndIsStableAcrossPolls(t *testing.T) {
	ids := func(r Stuck) string {
		d, _, n := stuckDeps(r)
		if err := noticeStuck(context.Background(), d, nil); err != nil {
			t.Fatal(err)
		}
		return n.raised[0].ID
	}
	changed := stoppedRun
	changed.Reason = string(runner.StopAgentTimeout)
	first := ids(stoppedRun)
	if first != ids(stoppedRun) {
		t.Error("a repeat poll changed the id")
	}
	if first == ids(changed) {
		t.Error("a changed reason kept the id")
	}
}

func TestNoticeStuckReturnsTheStoreReadError(t *testing.T) {
	d, q, n := stuckDeps(stoppedRun)
	q.stuckErr = errors.New("read failed")
	if err := noticeStuck(context.Background(), d, nil); err == nil || len(n.raised) != 0 {
		t.Fatalf("err = %v, raised %d", err, len(n.raised))
	}
}

func TestNoticeStuckReturnsAFailedRaise(t *testing.T) {
	d, _, n := stuckDeps(stoppedRun)
	n.raiseErr = errors.New("write failed")
	if err := noticeStuck(context.Background(), d, nil); err == nil {
		t.Fatal("a failed raise was swallowed")
	}
}

func TestPollRaisesTheStuckNoticeBeforePostingNotices(t *testing.T) {
	d, _, n := stuckDeps(stoppedRun)
	if _, err := Poll(context.Background(), d, Config{}); err != nil {
		t.Fatal(err)
	}
	if len(n.raised) != 1 {
		t.Fatalf("raised %d, want 1", len(n.raised))
	}
}

func TestPollReturnsTheStuckReadError(t *testing.T) {
	d, q, _ := stuckDeps()
	q.stuckErr = errors.New("read failed")
	if _, err := Poll(context.Background(), d, Config{}); !errors.Is(err, q.stuckErr) {
		t.Fatalf("err = %v, want the stuck read error", err)
	}
}

func TestStuckDetailSaysWhatHoldsAWaitingRun(t *testing.T) {
	for _, tc := range []struct {
		run  Stuck
		want string
	}{
		{Stuck{RunID: "ABC-8", State: StuckQueued, Reason: ConditionReviewWIP}, "queued, held by review-wip; clears when that condition lifts"},
		{Stuck{RunID: "ABC-8", State: StuckPlanned}, "planned, not yet claimed"},
		{Stuck{RunID: "ABC-8", State: StuckAbandoned}, "abandoned; requeue with: runner requeue -run-id ABC-8"},
	} {
		if got := stuckDetail(tc.run, Issue{}); got != tc.want {
			t.Errorf("stuckDetail(%+v) = %q, want %q", tc.run, got, tc.want)
		}
	}
}

func TestBlockerListNamesBlockersInIdentifierOrder(t *testing.T) {
	issue := Issue{BlockedByOpen: []string{"ABC-9", "ABC-10"}, BlockerStates: map[string]string{"ABC-9": "started", "ABC-10": "unstarted"}}
	if got, want := blockerList(issue), "ABC-10 (unstarted), ABC-9 (started)"; got != want {
		t.Fatalf("blockerList = %q, want %q", got, want)
	}
}

func TestPollNamesTheBlockerOfAStuckRunFromItsDelegatedIssue(t *testing.T) {
	run := Stuck{RunID: "ABC-8", Ticket: "ABC-8", State: StuckQueued, Reason: ConditionBlocked}
	issue := Issue{ID: "ABC-8", URL: "https://linear.app/t/issue/ABC-8", BlockedByOpen: []string{"ABC-9"}, BlockerStates: map[string]string{"ABC-9": "started"}}
	d, _, n := stuckDeps(run)
	d.Source = fakeSource{issues: []Issue{issue}}
	if _, err := Poll(context.Background(), d, Config{}); err != nil {
		t.Fatal(err)
	}
	if len(n.raised) != 1 || !strings.Contains(n.raised[0].Text(), "blocked by ABC-9 (started)") {
		t.Fatalf("raised %+v, want one notice naming the blocker", n.raised)
	}
}
