package dispatcher

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The sums of timeout-minutes across each stage's workflow chain in run.yml.
const (
	buildChainTimeout = 130 * time.Minute
	planChainTimeout  = 80 * time.Minute
)

func TestAbandonWindowsOutlastTheWholeWorkflowChain(t *testing.T) {
	if buildAbandonWindow <= buildChainTimeout {
		t.Fatalf("build window %v must exceed the build chain's %v", buildAbandonWindow, buildChainTimeout)
	}
	if planAbandonWindow <= planChainTimeout {
		t.Fatalf("plan window %v must exceed the plan chain's %v", planAbandonWindow, planChainTimeout)
	}
}

func TestPollSweepsWithTheStageWindowsAndRaisesOneNoticePerAbandonedRun(t *testing.T) {
	q := &fakeQueue{abandoned: []Abandoned{{RunID: "run-18", Ticket: "FRG-18", Repo: "octo/scratch", Stage: StagePlan, Attempt: 2}}}
	notices := &fakeNotices{}
	d := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	d.Notices = notices
	if _, err := Poll(context.Background(), d, buildConfig); err != nil {
		t.Fatal(err)
	}
	if len(q.sweeps) != 1 || q.sweeps[0].at != pollAt ||
		q.sweeps[0].windows != (AbandonWindows{Plan: planAbandonWindow, Build: buildAbandonWindow}) {
		t.Fatalf("sweeps = %+v", q.sweeps)
	}
	want := Notice{ID: "abandoned-run-18-2", Class: "abandoned:FRG-18", Link: "https://github.com/octo/scratch/actions"}
	if len(notices.raised) != 1 || notices.raised[0] != want {
		t.Fatalf("raised = %+v, want %+v", notices.raised, want)
	}
	if _, err := Poll(context.Background(), d, buildConfig); err != nil {
		t.Fatal(err)
	}
	if len(notices.raised) != 1 {
		t.Fatalf("a second sweep raised %d notices, want still 1", len(notices.raised))
	}
}

func TestPollSweepsWhenAdmissionFails(t *testing.T) {
	q := &fakeQueue{abandoned: []Abandoned{{RunID: "run-18"}}}
	notices := &fakeNotices{}
	d := pollDeps(fakeSource{err: errFirestore}, q, &fakeWorkflow{})
	d.Notices = notices
	if _, err := Poll(context.Background(), d, buildConfig); !errors.Is(err, errFirestore) {
		t.Fatalf("err = %v, want the source failure", err)
	}
	if len(q.sweeps) != 1 || len(notices.raised) != 1 {
		t.Fatalf("sweeps = %d, raised = %d, want the sweep to run past the admit error", len(q.sweeps), len(notices.raised))
	}
}

func TestPollReportsAFailedSweepAndAFailedNotice(t *testing.T) {
	sweepErr, raiseErr := errors.New("sweep failed"), errors.New("raise failed")
	q := &fakeQueue{abandoned: []Abandoned{{RunID: "run-18"}}, sweepErr: sweepErr}
	d := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	d.Notices = &fakeNotices{raiseErr: raiseErr}
	_, err := Poll(context.Background(), d, buildConfig)
	if !errors.Is(err, sweepErr) || !errors.Is(err, raiseErr) {
		t.Fatalf("err = %v, want both failures", err)
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
