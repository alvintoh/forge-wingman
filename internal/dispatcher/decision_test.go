package dispatcher

import (
	"context"
	"errors"
	"fmt"
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

func TestPollAnswersAWaitingRunWhenTheProvisionalReadFails(t *testing.T) {
	dec := &fakeDecisions{
		waiting:        []WaitingRun{waitingRun(pollAt, pollAt.Add(-time.Hour), "reuse", pollAt.Add(-time.Minute))},
		provisionalErr: errors.New("query refused"),
	}
	_, err := Poll(context.Background(), decisionDeps(dec, &fakeElicitor{}, &fakeNotices{}), decisionConfig)
	if err == nil {
		t.Fatal("the failed provisional read was not reported")
	}
	if len(dec.answered) != 1 || dec.answered[0].answer != "Reuse the existing flag" {
		t.Fatalf("answered = %+v, want the waiting run still answered", dec.answered)
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

// TestPollMeasuresTheDeadlineFromTheLastWordAfterAReask: a decision re-asked
// after an unmatched reply measures its window from that reply rather than from
// the question that predates it, so a run the owner is still talking about is
// never stopped mid-conversation.
func TestPollMeasuresTheDeadlineFromTheLastWordAfterAReask(t *testing.T) {
	dec := &fakeDecisions{waiting: []WaitingRun{waitingRun(pollAt.Add(-25*time.Hour), pollAt.Add(-2*time.Hour), "do something else", pollAt.Add(-2*time.Hour))}, sessions: map[string]string{"FRG-71": "session-1"}}
	if _, err := Poll(context.Background(), decisionDeps(dec, &fakeElicitor{}, &fakeNotices{}), decisionConfig); err != nil {
		t.Fatal(err)
	}
	if len(dec.stopped) != 0 {
		t.Fatalf("stopped a run whose question was re-asked two hours ago: %+v", dec.stopped)
	}
}

// TestPollMeasuresTheDeadlineFromALatePost: a question that only reached the
// owner long after it was raised keeps the full window to answer it, rather than
// stopping the run on a clock that ran while the question sat unposted.
func TestPollMeasuresTheDeadlineFromALatePost(t *testing.T) {
	dec := &fakeDecisions{waiting: []WaitingRun{waitingRun(pollAt.Add(-30*time.Hour), pollAt.Add(-time.Hour), "", time.Time{})}}
	if _, err := Poll(context.Background(), decisionDeps(dec, &fakeElicitor{}, &fakeNotices{}), decisionConfig); err != nil {
		t.Fatal(err)
	}
	if len(dec.stopped) != 0 {
		t.Fatalf("stopped a run whose question was posted an hour ago: %+v", dec.stopped)
	}
}

func TestPollPostsAProvisionalDecisionOnceNamingTheOptionTaken(t *testing.T) {
	dec := &fakeDecisions{
		provisional: []ProvisionalRun{{RunID: "FRG-71", Decision: decisionFixture, Taken: "Reuse the existing flag", Index: 1}},
		sessions:    map[string]string{"FRG-71": "session-1"},
	}
	el := &fakeElicitor{}
	if _, err := Poll(context.Background(), decisionDeps(dec, el, &fakeNotices{}), decisionConfig); err != nil {
		t.Fatal(err)
	}
	if len(el.calls) != 1 || el.calls[0].taken != "Reuse the existing flag" || el.calls[0].sessionID != "session-1" {
		t.Fatalf("elicit calls = %+v", el.calls)
	}
	if len(dec.posted) != 1 || dec.posted[0].runID != "FRG-71" {
		t.Fatalf("posted = %+v", dec.posted)
	}
	if len(dec.answered) != 0 || len(dec.stopped) != 0 {
		t.Fatalf("answered %+v, stopped %+v", dec.answered, dec.stopped)
	}
}

func TestPollReportsAFailedSessionReadForAProvisionalDecision(t *testing.T) {
	dec := &fakeDecisions{
		provisional: []ProvisionalRun{{RunID: "FRG-71", Decision: decisionFixture, Taken: "Reuse the existing flag", Index: 1}},
		sessionErr:  errors.New("session lookup refused"),
	}
	el := &fakeElicitor{}
	if _, err := Poll(context.Background(), decisionDeps(dec, el, &fakeNotices{}), decisionConfig); err == nil {
		t.Fatal("the failed session read was not reported")
	}
	if len(el.calls) != 0 || len(dec.posted) != 0 {
		t.Fatalf("posted after a failed session read: elicit %+v, posted %+v", el.calls, dec.posted)
	}
}

func TestPollLeavesAProvisionalDecisionUnpostedWhenTheElicitFails(t *testing.T) {
	dec := &fakeDecisions{
		provisional: []ProvisionalRun{{RunID: "FRG-71", Decision: decisionFixture, Taken: "Reuse the existing flag", Index: 1}},
		sessions:    map[string]string{"FRG-71": "session-1"},
	}
	el := &fakeElicitor{err: errors.New("linear refused")}
	if _, err := Poll(context.Background(), decisionDeps(dec, el, &fakeNotices{}), decisionConfig); err != nil {
		t.Fatalf("a refused post is retried next poll, not an error: %v", err)
	}
	if len(dec.posted) != 0 {
		t.Fatalf("stamped a decision whose post was refused: %+v", dec.posted)
	}
}

func TestPollReportsAProvisionalDecisionItCouldNotStampPosted(t *testing.T) {
	dec := &fakeDecisions{
		provisional:          []ProvisionalRun{{RunID: "FRG-71", Decision: decisionFixture, Taken: "Reuse the existing flag", Index: 1}},
		sessions:             map[string]string{"FRG-71": "session-1"},
		provisionalPostedErr: errors.New("write refused"),
	}
	if _, err := Poll(context.Background(), decisionDeps(dec, &fakeElicitor{}, &fakeNotices{}), decisionConfig); err == nil {
		t.Fatal("the failed stamp was not reported")
	}
}

func TestPollLeavesAProvisionalDecisionUnpostedWithoutASession(t *testing.T) {
	dec := &fakeDecisions{provisional: []ProvisionalRun{{RunID: "FRG-71", Decision: decisionFixture, Taken: "Reuse the existing flag", Index: 1}}}
	el := &fakeElicitor{}
	if _, err := Poll(context.Background(), decisionDeps(dec, el, &fakeNotices{}), decisionConfig); err != nil {
		t.Fatal(err)
	}
	if len(el.calls) != 0 || len(dec.posted) != 0 {
		t.Fatalf("posted with no session: elicit %+v, posted %+v", el.calls, dec.posted)
	}
}

// repliedRun is a run whose provisional decision took the first option and
// carries reply.
func repliedRun(reply string) ProvisionalRun {
	return ProvisionalRun{RunID: "run-71", Decision: decisionFixture, Taken: "Reuse the existing flag", Index: 1, Reply: reply, RepliedAt: pollAt}
}

func TestPollConfirmsAProvisionalDecisionWhenTheReplyNamesTheOptionTaken(t *testing.T) {
	dec := &fakeDecisions{replied: []ProvisionalRun{repliedRun("reuse")}}
	if _, err := Poll(context.Background(), decisionDeps(dec, &fakeElicitor{}, &fakeNotices{}), decisionConfig); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(dec.confirmed, []string{"run-71"}) || len(dec.superseded) != 0 {
		t.Fatalf("confirmed %v, superseded %+v", dec.confirmed, dec.superseded)
	}
}

func TestPollSupersedesAProvisionalDecisionWhenTheReplyNamesAnotherOption(t *testing.T) {
	dec := &fakeDecisions{replied: []ProvisionalRun{repliedRun("Add a new flag")}}
	if _, err := Poll(context.Background(), decisionDeps(dec, &fakeElicitor{}, &fakeNotices{}), decisionConfig); err != nil {
		t.Fatal(err)
	}
	if len(dec.superseded) != 1 || dec.superseded[0].answer != "Add a new flag" || len(dec.confirmed) != 0 {
		t.Fatalf("superseded %+v, confirmed %v", dec.superseded, dec.confirmed)
	}
}

func TestPollChangesNothingForAProvisionalReplyMatchingNoOption(t *testing.T) {
	dec := &fakeDecisions{replied: []ProvisionalRun{repliedRun("hmm, not sure")}}
	if _, err := Poll(context.Background(), decisionDeps(dec, &fakeElicitor{}, &fakeNotices{}), decisionConfig); err != nil {
		t.Fatalf("an unmatched reply is not an error: %v", err)
	}
	if len(dec.confirmed) != 0 || len(dec.superseded) != 0 {
		t.Fatalf("acted on an unmatched reply: confirmed %v, superseded %+v", dec.confirmed, dec.superseded)
	}
	if !slices.Equal(dec.cleared, []string{"run-71"}) {
		t.Fatalf("cleared %v, want the unmatched reply cleared once so the next poll stays quiet", dec.cleared)
	}
}

func TestPollRetriesASupersedeOfARunStillInFlightNextPoll(t *testing.T) {
	dec := &fakeDecisions{replied: []ProvisionalRun{repliedRun("new")}, supersedeErr: fmt.Errorf("superseding: %w", ErrRunNotEnded)}
	if _, err := Poll(context.Background(), decisionDeps(dec, &fakeElicitor{}, &fakeNotices{}), decisionConfig); err != nil {
		t.Fatalf("a run in flight is retried, not an error: %v", err)
	}
	if len(dec.confirmed) != 0 {
		t.Fatalf("confirmed %v", dec.confirmed)
	}
}

func TestPollReportsAFailedSupersedeAndAFailedConfirm(t *testing.T) {
	for name, dec := range map[string]*fakeDecisions{
		"supersede": {replied: []ProvisionalRun{repliedRun("new")}, supersedeErr: errors.New("write refused")},
		"confirm":   {replied: []ProvisionalRun{repliedRun("reuse")}, confirmErr: errors.New("write refused")},
		"list":      {repliedErr: errors.New("query refused")},
	} {
		if _, err := Poll(context.Background(), decisionDeps(dec, &fakeElicitor{}, &fakeNotices{}), decisionConfig); err == nil {
			t.Fatalf("%s: the failure was not reported", name)
		}
	}
}

func TestPollLeavesAGuidedRunsReplyToTheWaitingPath(t *testing.T) {
	dec := &fakeDecisions{waiting: []WaitingRun{waitingRun(pollAt, pollAt.Add(-time.Hour), "new", pollAt.Add(-time.Minute))}}
	if _, err := Poll(context.Background(), decisionDeps(dec, &fakeElicitor{}, &fakeNotices{}), decisionConfig); err != nil {
		t.Fatal(err)
	}
	if len(dec.answered) != 1 || dec.answered[0].answer != "Add a new flag" || len(dec.confirmed) != 0 || len(dec.superseded) != 0 {
		t.Fatalf("answered %+v, confirmed %v, superseded %+v", dec.answered, dec.confirmed, dec.superseded)
	}
}
