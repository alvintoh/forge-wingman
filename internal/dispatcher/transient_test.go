package dispatcher

import (
	"context"
	"errors"
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

func TestPollRequeuesARunThatStoppedOnAListedFault(t *testing.T) {
	stop := TransientStop{RunID: "FRG-9", Ticket: "FRG-9", Class: runner.TransientModuleProxy}
	q := &fakeQueue{transientStops: []TransientStop{stop}}
	notices := &fakeNotices{}
	d := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	d.Autonomies = &fakeAutonomies{level: AutonomyRequeues}
	d.Notices = notices
	if _, err := Poll(context.Background(), d, buildConfig); err != nil {
		t.Fatal(err)
	}
	if len(q.requeued) != 1 || q.requeued[0].RunID != "FRG-9" || q.requeued[0].Class != runner.TransientModuleProxy {
		t.Fatalf("requeued = %+v, want the stopped run returned to the queue for its class", q.requeued)
	}
	if len(notices.raised) != 0 {
		t.Fatalf("raised %+v, want no notice while the run has an attempt left", notices.raised)
	}
}

func TestPollRequeuesNothingAtLevelZero(t *testing.T) {
	q := &fakeQueue{transientStops: []TransientStop{{RunID: "FRG-9", Class: runner.TransientModuleProxy}}}
	d := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	if _, err := Poll(context.Background(), d, buildConfig); err != nil {
		t.Fatal(err)
	}
	if len(q.requeued) != 0 {
		t.Fatalf("requeued %+v at level 0, want every retry left to the owner", q.requeued)
	}
}

func TestPollNeverRequeuesAStopWhoseClassIsNotListed(t *testing.T) {
	q := &fakeQueue{transientStops: []TransientStop{{RunID: "FRG-9", Class: "something-else"}}}
	notices := &fakeNotices{}
	d := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	d.Autonomies = &fakeAutonomies{level: AutonomyRequeues}
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
	d.Autonomies = &fakeAutonomies{level: AutonomyRequeues}
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
	d.Autonomies = &fakeAutonomies{level: AutonomyRequeues}
	if _, err := Poll(context.Background(), d, buildConfig); !errors.Is(err, errFirestore) {
		t.Fatalf("err = %v, want the failed listing reported", err)
	}
}

func TestPollReportsAFailedAutomaticRequeue(t *testing.T) {
	q := &fakeQueue{transientStops: []TransientStop{{RunID: "FRG-9", Class: runner.TransientModuleProxy}}, requeueTransientErr: errFirestore}
	d := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	d.Autonomies = &fakeAutonomies{level: AutonomyRequeues}
	if _, err := Poll(context.Background(), d, buildConfig); !errors.Is(err, errFirestore) {
		t.Fatalf("err = %v, want the failed requeue reported", err)
	}
}
