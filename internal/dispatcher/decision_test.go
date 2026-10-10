package dispatcher

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// decisionConfig is a poll config with a 24-hour decision window.
var decisionConfig = Config{DecisionTimeout: 24 * time.Hour}

// decisionFixture is the decision the dispatcher tests post and answer.
var decisionFixture = runner.Decision{
	Question: "Which flag should the run use?",
	Context:  []string{"The flag is read in two places."},
	Options: []runner.DecisionOption{
		{Label: "Reuse the existing flag", Value: "reuse", Cost: "no new config"},
		{Label: "Add a new flag", Value: "new", Cost: "one more env var"},
	},
}

func decisionDeps(dec *fakeDecisions, el *fakeElicitor, notices *fakeNotices) Deps {
	d := pollDeps(fakeSource{}, &fakeQueue{}, &fakeWorkflow{})
	d.Decisions = dec
	d.Elicit = el
	d.Notices = notices
	return d
}

// waitingRun is a run parked on decisionFixture, asked at asked.
func waitingRun(asked, posted time.Time, reply string, replied time.Time) WaitingRun {
	return WaitingRun{
		RunID: "FRG-71", Decision: decisionFixture, SessionID: "session-1",
		AskedAt: asked, PostedAt: posted, Reply: reply, RepliedAt: replied,
		RunURL: "https://github.com/o/r/actions/runs/7", Index: 1,
	}
}

func TestPollPostsADecisionOnce(t *testing.T) {
	dec := &fakeDecisions{waiting: []WaitingRun{waitingRun(pollAt, time.Time{}, "", time.Time{})}, sessions: map[string]string{"FRG-71": "session-1"}}
	el, notices := &fakeElicitor{}, &fakeNotices{}
	if _, err := Poll(context.Background(), decisionDeps(dec, el, notices), decisionConfig); err != nil {
		t.Fatal(err)
	}
	if len(el.calls) != 1 || el.calls[0].sessionID != "session-1" {
		t.Fatalf("elicit calls = %+v", el.calls)
	}
	if len(dec.posted) != 1 || dec.posted[0].sessionID != "session-1" {
		t.Fatalf("posted = %+v", dec.posted)
	}
	if len(dec.answered) != 0 || len(dec.stopped) != 0 {
		t.Fatalf("answered %+v, stopped %+v", dec.answered, dec.stopped)
	}
}

func TestPollSkipsAnAlreadyPostedDecision(t *testing.T) {
	dec := &fakeDecisions{waiting: []WaitingRun{waitingRun(pollAt, pollAt.Add(-time.Hour), "", time.Time{})}}
	el := &fakeElicitor{}
	if _, err := Poll(context.Background(), decisionDeps(dec, el, &fakeNotices{}), decisionConfig); err != nil {
		t.Fatal(err)
	}
	if len(el.calls) != 0 || len(dec.posted) != 0 {
		t.Fatalf("re-posted a decision: elicit %+v, posted %+v", el.calls, dec.posted)
	}
}

func TestPollResumesOnAMatchingReply(t *testing.T) {
	dec := &fakeDecisions{waiting: []WaitingRun{waitingRun(pollAt, pollAt.Add(-time.Hour), "reuse", pollAt.Add(-time.Minute))}}
	el := &fakeElicitor{}
	if _, err := Poll(context.Background(), decisionDeps(dec, el, &fakeNotices{}), decisionConfig); err != nil {
		t.Fatal(err)
	}
	if len(dec.answered) != 1 || dec.answered[0].answer != "Reuse the existing flag" {
		t.Fatalf("answered = %+v, want the matched option's label", dec.answered)
	}
	if len(el.calls) != 0 {
		t.Fatalf("re-asked a matching reply: %+v", el.calls)
	}
}

// TestPollReasksAfterAnUnmatchedReply covers AC4: free text matching no option
// gets the same options again, and nothing is guessed.
func TestPollReasksAfterAnUnmatchedReply(t *testing.T) {
	dec := &fakeDecisions{waiting: []WaitingRun{waitingRun(pollAt, pollAt.Add(-time.Hour), "do something else", pollAt.Add(-time.Minute))}, sessions: map[string]string{"FRG-71": "session-1"}}
	el := &fakeElicitor{}
	if _, err := Poll(context.Background(), decisionDeps(dec, el, &fakeNotices{}), decisionConfig); err != nil {
		t.Fatal(err)
	}
	if len(dec.answered) != 0 {
		t.Fatalf("guessed an option: %+v", dec.answered)
	}
	if len(el.calls) != 1 || !slices.Equal(el.calls[0].decision.Options, decisionFixture.Options) {
		t.Fatalf("elicit calls = %+v, want the same options again", el.calls)
	}
}

// TestPollStopsPastTheDeadlineOnce covers AC5: no reply within the window stops
// the run as waiting-on-owner with one notice, raised once.
func TestPollStopsPastTheDeadlineOnce(t *testing.T) {
	dec := &fakeDecisions{waiting: []WaitingRun{waitingRun(pollAt.Add(-25*time.Hour), pollAt.Add(-25*time.Hour), "", time.Time{})}}
	el, notices := &fakeElicitor{}, &fakeNotices{}
	d := decisionDeps(dec, el, notices)
	if _, err := Poll(context.Background(), d, decisionConfig); err != nil {
		t.Fatal(err)
	}
	if len(dec.stopped) != 1 || dec.stopped[0].question != decisionFixture.Question {
		t.Fatalf("stopped = %+v", dec.stopped)
	}
	if len(notices.raised) != 1 || notices.raised[0].ID != "waiting-FRG-71#1" || notices.raised[0].Class != "waiting-on-owner" {
		t.Fatalf("raised = %+v", notices.raised)
	}
	if len(el.calls) != 0 {
		t.Fatalf("asked a run past its deadline: %+v", el.calls)
	}
	// A second poll finds the run no longer waiting and raises nothing.
	if _, err := Poll(context.Background(), d, decisionConfig); err != nil {
		t.Fatal(err)
	}
	if len(notices.raised) != 1 || len(dec.stopped) != 1 {
		t.Fatalf("a second poll stopped again: raised %+v, stopped %+v", notices.raised, dec.stopped)
	}
}

// TestPollMeasuresTheDeadlineFromTheLastReply: an owner who replies late keeps
// the run alive, since the window restarts at their reply.
func TestPollMeasuresTheDeadlineFromTheLastReply(t *testing.T) {
	dec := &fakeDecisions{waiting: []WaitingRun{waitingRun(pollAt.Add(-25*time.Hour), pollAt.Add(-25*time.Hour), "reuse", pollAt.Add(-time.Minute))}}
	if _, err := Poll(context.Background(), decisionDeps(dec, &fakeElicitor{}, &fakeNotices{}), decisionConfig); err != nil {
		t.Fatal(err)
	}
	if len(dec.answered) != 1 || len(dec.stopped) != 0 {
		t.Fatalf("answered %+v, stopped %+v; want the run resumed", dec.answered, dec.stopped)
	}
}

func TestPollToleratesAFailedElicit(t *testing.T) {
	dec := &fakeDecisions{waiting: []WaitingRun{waitingRun(pollAt, time.Time{}, "", time.Time{})}, sessions: map[string]string{"FRG-71": "session-1"}}
	el := &fakeElicitor{err: errors.New("linear is unreachable")}
	if _, err := Poll(context.Background(), decisionDeps(dec, el, &fakeNotices{}), decisionConfig); err != nil {
		t.Fatalf("a failed elicit propagated: %v", err)
	}
	if len(dec.posted) != 0 {
		t.Fatalf("posted a decision Linear refused: %+v", dec.posted)
	}
}

func TestPollLeavesADecisionUnpostedWithoutASession(t *testing.T) {
	dec := &fakeDecisions{waiting: []WaitingRun{waitingRun(pollAt, time.Time{}, "", time.Time{})}}
	el := &fakeElicitor{}
	if _, err := Poll(context.Background(), decisionDeps(dec, el, &fakeNotices{}), decisionConfig); err != nil {
		t.Fatal(err)
	}
	if len(el.calls) != 0 || len(dec.posted) != 0 {
		t.Fatalf("asked without a session: elicit %+v, posted %+v", el.calls, dec.posted)
	}
}

// TestPollRetriesWhenTheNoticeCannotBeRaised: a stop that could not notice
// leaves the run waiting, so the next poll raises it rather than stopping the
// run with no notice at all.
func TestPollRetriesWhenTheNoticeCannotBeRaised(t *testing.T) {
	dec := &fakeDecisions{waiting: []WaitingRun{waitingRun(pollAt.Add(-25*time.Hour), pollAt.Add(-25*time.Hour), "", time.Time{})}}
	notices := &fakeNotices{raiseErr: errors.New("the notice store is down")}
	if _, err := Poll(context.Background(), decisionDeps(dec, &fakeElicitor{}, notices), decisionConfig); err == nil {
		t.Fatal("a failed raise was swallowed")
	}
	if len(dec.stopped) != 0 {
		t.Fatalf("stopped with no notice: %+v", dec.stopped)
	}
	notices.raiseErr = nil
	if _, err := Poll(context.Background(), decisionDeps(dec, &fakeElicitor{}, notices), decisionConfig); err != nil {
		t.Fatal(err)
	}
	if len(notices.raised) != 1 || len(dec.stopped) != 1 {
		t.Fatalf("retry raised %+v, stopped %+v", notices.raised, dec.stopped)
	}
}
