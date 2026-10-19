package dispatcher

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

func TestTransientExhaustedNoticeNamesTheRunItsClassAndTheWayOn(t *testing.T) {
	stop := TransientStop{RunID: "run-18", Ticket: "FRG-18", Repo: "octo/scratch", Class: runner.TransientLostRunner, Attempts: 2}
	want := Notice{
		ID:    "transient-exhausted-run-18-2",
		Class: "transient-exhausted:run-18 ticket FRG-18 lost-runner after 2 automatic requeues; requeue with: runner requeue -run-id run-18",
		Link:  "https://github.com/octo/scratch/actions",
	}
	if got := stop.ExhaustedNotice(); got != want {
		t.Fatalf("notice = %+v, want %+v", got, want)
	}
	same := TransientStop{RunID: "FRG-18", Ticket: "FRG-18", Class: runner.TransientLostRunner, Attempts: 2}
	if got, want := same.ExhaustedNotice().Class, "transient-exhausted:FRG-18 lost-runner after 2 automatic requeues; requeue with: runner requeue -run-id FRG-18"; got != want {
		t.Fatalf("class = %q, want the shared id written once: %q", got, want)
	}
}

// TestPollRequeuesARunWithNoLevelSet is AC2: the requeue reads no autonomy
// level, so a run that stopped on a listed fault returns to the queue for the
// class it stopped on even when the level cannot be read.
func TestPollRequeuesARunWithNoLevelSet(t *testing.T) {
	stop := TransientStop{RunID: "FRG-9", Ticket: "FRG-9", Class: runner.TransientModuleProxy}
	q := &fakeQueue{transientStops: []TransientStop{stop}}
	notices := &fakeNotices{}
	d := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	d.Autonomies = &fakeAutonomies{err: errFirestore}
	d.Notices = notices
	if _, err := Poll(context.Background(), d, buildConfig); err != nil {
		t.Fatal(err)
	}
	if len(q.requeued) != 1 || q.requeued[0].RunID != "FRG-9" || q.requeued[0].Class != runner.TransientModuleProxy {
		t.Fatalf("requeued = %+v, want the stopped run returned from an unreadable level", q.requeued)
	}
	if len(notices.raised) != 0 {
		t.Fatalf("raised %+v, want no notice while the run has an attempt left", notices.raised)
	}
}

func TestPollNeverRequeuesAStopWhoseClassIsNotListed(t *testing.T) {
	q := &fakeQueue{transientStops: []TransientStop{{RunID: "FRG-9", Class: "something-else"}}}
	notices := &fakeNotices{}
	d := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	d.Notices = notices
	if _, err := Poll(context.Background(), d, buildConfig); err != nil {
		t.Fatal(err)
	}
	if len(q.requeued) != 0 {
		t.Fatalf("requeued %+v for a class the list does not carry", q.requeued)
	}
	if len(notices.raised) != 0 {
		t.Fatalf("raised %+v, want an unlisted stop left alone", notices.raised)
	}
}

func TestPollLeavesARunThatHasUsedEveryAutomaticRequeue(t *testing.T) {
	stop := TransientStop{RunID: "FRG-9", Ticket: "FRG-9", Repo: "octo/scratch", Class: runner.TransientLostRunner, Attempts: DefaultTransientRequeues}
	q := &fakeQueue{transientStops: []TransientStop{stop}}
	notices := &fakeNotices{}
	d := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	d.Notices = notices
	if _, err := Poll(context.Background(), d, buildConfig); err != nil {
		t.Fatal(err)
	}
	if len(q.requeued) != 0 {
		t.Fatalf("requeued %+v past the limit", q.requeued)
	}
	if len(notices.raised) != 1 || notices.raised[0] != stop.ExhaustedNotice() {
		t.Fatalf("raised = %+v, want the exhausted run announced once", notices.raised)
	}
}

func TestPollReportsAFailedTransientListing(t *testing.T) {
	q := &fakeQueue{transientErr: errFirestore}
	d := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	if _, err := Poll(context.Background(), d, buildConfig); !errors.Is(err, errFirestore) {
		t.Fatalf("err = %v, want the failed listing reported", err)
	}
}

func TestPollReportsAFailedAutomaticRequeue(t *testing.T) {
	q := &fakeQueue{transientStops: []TransientStop{{RunID: "FRG-9", Class: runner.TransientModuleProxy}}, requeueTransientErr: errFirestore}
	d := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	if _, err := Poll(context.Background(), d, buildConfig); !errors.Is(err, errFirestore) {
		t.Fatalf("err = %v, want the failed requeue reported", err)
	}
}

// TestPollPutsARequeuedTicketBackWhereItWas: the requeue returns the run to the
// queue, so its ticket shows the state it held before the claim again rather
// than the In Progress that claim set.
func TestPollPutsARequeuedTicketBackWhereItWas(t *testing.T) {
	q := &fakeQueue{
		transientStops: []TransientStop{{RunID: "FRG-9", Ticket: "FRG-9", Class: runner.TransientModuleProxy}},
		ticketStates:   map[string]string{"FRG-9": "state-todo"},
	}
	tickets := &fakeTicketStates{}
	d := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	d.TicketStates = tickets
	if _, err := Poll(context.Background(), d, buildConfig); err != nil {
		t.Fatal(err)
	}
	if len(tickets.restored) != 1 || tickets.restored[0] != (restoreCall{ticket: "FRG-9", state: "state-todo"}) {
		t.Fatalf("restored = %+v, want the requeued ticket put back where it was", tickets.restored)
	}
}

// TestPollRequeuesDespiteAFailedTicketRestore: a failed Linear write is logged
// once, with the reason as its own field, and never withholds the requeue.
func TestPollRequeuesDespiteAFailedTicketRestore(t *testing.T) {
	q := &fakeQueue{
		transientStops: []TransientStop{{RunID: "FRG-9", Ticket: "FRG-9", Class: runner.TransientModuleProxy}},
		ticketStates:   map[string]string{"FRG-9": "state-todo"},
	}
	var logs bytes.Buffer
	d := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	d.TicketStates = &fakeTicketStates{restoreErr: errors.New("linear said no")}
	d.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	if _, err := Poll(context.Background(), d, buildConfig); err != nil {
		t.Fatal(err)
	}
	if len(q.requeued) != 1 {
		t.Fatalf("requeued = %+v, want the run back in the queue anyway", q.requeued)
	}
	if got := strings.Count(logs.String(), "msg=ticketStateNotRestored"); got != 1 {
		t.Fatalf("log = %q, want one ticketStateNotRestored", logs.String())
	}
	if !strings.Contains(logs.String(), `err="linear said no"`) {
		t.Fatalf("log = %q, want the reason as its own field", logs.String())
	}
}
