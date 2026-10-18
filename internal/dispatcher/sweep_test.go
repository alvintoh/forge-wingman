package dispatcher

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// The sums of timeout-minutes across each stage's workflow chain in run.yml.
const (
	buildChainTimeout = 160 * time.Minute
	planChainTimeout  = 110 * time.Minute
)

func TestAbandonWindowsOutlastTheWholeWorkflowChain(t *testing.T) {
	if buildAbandonWindow <= buildChainTimeout {
		t.Fatalf("build window %v must exceed the build chain's %v", buildAbandonWindow, buildChainTimeout)
	}
	if planAbandonWindow <= planChainTimeout {
		t.Fatalf("plan window %v must exceed the plan chain's %v", planAbandonWindow, planChainTimeout)
	}
}

func TestPollSweepsWithTheStageWindowsAndPostsTheNoticeTheSweepRaised(t *testing.T) {
	run := Abandoned{RunID: "run-18", Ticket: "FRG-18", Repo: "octo/scratch", Stage: StagePlan, Attempt: 2}
	want := Notice{ID: "abandoned-run-18-2", Class: "abandoned:run-18 ticket FRG-18 requeue with: runner requeue -run-id run-18", Link: "https://github.com/octo/scratch/actions"}
	if got := run.Notice(); got != want {
		t.Fatalf("notice = %+v, want %+v", got, want)
	}
	q := &fakeQueue{abandoned: []Abandoned{run}}
	notices, poster := &fakeNotices{pending: []Notice{run.Notice()}}, &fakePoster{}
	d := noticeDeps(notices, poster, new(int))
	d.Queue = q
	if _, err := Poll(context.Background(), d, buildConfig); err != nil {
		t.Fatal(err)
	}
	if len(q.sweeps) != 1 || q.sweeps[0].at != pollAt ||
		q.sweeps[0].windows != (AbandonWindows{Plan: planAbandonWindow, Build: buildAbandonWindow}) {
		t.Fatalf("sweeps = %+v", q.sweeps)
	}
	if len(poster.posts) != 1 || poster.posts[0].Text() != "abandoned:run-18 ticket FRG-18 requeue with: runner requeue -run-id run-18 https://github.com/octo/scratch/actions" {
		t.Fatalf("posts = %+v, want the sweep's notice delivered", poster.posts)
	}
	if len(notices.raised) != 0 {
		t.Fatalf("the dispatcher raised %d notices itself, want the store's transaction to", len(notices.raised))
	}
}

func TestPollSweepsWhenAdmissionFails(t *testing.T) {
	q := &fakeQueue{}
	d := pollDeps(fakeSource{err: errFirestore}, q, &fakeWorkflow{})
	if _, err := Poll(context.Background(), d, buildConfig); !errors.Is(err, errFirestore) {
		t.Fatalf("err = %v, want the source failure", err)
	}
	if len(q.sweeps) != 1 {
		t.Fatalf("sweeps = %d, want the sweep to run past the admit error", len(q.sweeps))
	}
}

func TestPollReportsAFailedSweep(t *testing.T) {
	sweepErr := errors.New("sweep failed")
	d := pollDeps(fakeSource{}, &fakeQueue{sweepErr: sweepErr}, &fakeWorkflow{})
	if _, err := Poll(context.Background(), d, buildConfig); !errors.Is(err, sweepErr) {
		t.Fatalf("err = %v, want the sweep failure", err)
	}
}

func TestAbandonedLinkIsTheRepositoryActionsPage(t *testing.T) {
	for name, tt := range map[string]struct {
		a    Abandoned
		want string
	}{
		"the repository": {Abandoned{Repo: "o/r"}, "https://github.com/o/r/actions"},
		"no repository":  {Abandoned{}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tt.a.link(); got != tt.want {
				t.Fatalf("link = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAbandonedNoticeNamesTheRunOnceWhenTheTicketIsTheSameString(t *testing.T) {
	same := Abandoned{RunID: "FRG-18", Ticket: "FRG-18", Repo: "o/r"}.Notice()
	if same.Class != "abandoned:FRG-18 requeue with: runner requeue -run-id FRG-18" {
		t.Fatalf("class = %q, want the shared id written once", same.Class)
	}
	if text := (Abandoned{RunID: "run-18", Ticket: "FRG-18"}).Notice().Text(); !strings.Contains(text, "run-18") || !strings.Contains(text, "FRG-18") {
		t.Fatalf("text = %q, want the run id and the ticket", text)
	}
}

func TestAbandonedNoticeTextIsIdsAndTheRequeueCommandForTheRunID(t *testing.T) {
	a := Abandoned{RunID: "run-18", Ticket: "FRG-18", Repo: "o/r", Stage: StageBuild, Attempt: 2}
	want := "abandoned:run-18 ticket FRG-18 requeue with: runner requeue -run-id run-18 https://github.com/o/r/actions"
	if got := a.Notice().Text(); got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}

// TestPollPutsAnAbandonedTicketBackWhereItWas: a run abandoned past its window
// will report nothing more, so its ticket shows the state it held before the
// claim again rather than the In Progress that claim set.
func TestPollPutsAnAbandonedTicketBackWhereItWas(t *testing.T) {
	run := Abandoned{RunID: "run-18", Ticket: "FRG-18", Repo: "octo/scratch", Stage: StagePlan, Attempt: 2, TicketState: "state-todo"}
	q := &fakeQueue{abandoned: []Abandoned{run}}
	tickets := &fakeTicketStates{}
	d := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	d.TicketStates = tickets
	if _, err := Poll(context.Background(), d, buildConfig); err != nil {
		t.Fatal(err)
	}
	if len(tickets.restored) != 1 || tickets.restored[0] != (restoreCall{ticket: "FRG-18", state: "state-todo"}) {
		t.Fatalf("restored = %+v, want the abandoned ticket put back where it was", tickets.restored)
	}
}

// TestPollSweepsDespiteAFailedTicketRestore: a failed Linear write is logged
// once, with the reason as its own field, and never fails the poll.
func TestPollSweepsDespiteAFailedTicketRestore(t *testing.T) {
	run := Abandoned{RunID: "run-18", Ticket: "FRG-18", Repo: "octo/scratch", Stage: StagePlan, TicketState: "state-todo"}
	q := &fakeQueue{abandoned: []Abandoned{run}}
	var logs bytes.Buffer
	d := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	d.TicketStates = &fakeTicketStates{restoreErr: errors.New("linear said no")}
	d.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	if _, err := Poll(context.Background(), d, buildConfig); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(logs.String(), "msg=ticketStateNotRestored"); got != 1 {
		t.Fatalf("log = %q, want one ticketStateNotRestored", logs.String())
	}
	if !strings.Contains(logs.String(), `err="linear said no"`) {
		t.Fatalf("log = %q, want the reason as its own field", logs.String())
	}
}
