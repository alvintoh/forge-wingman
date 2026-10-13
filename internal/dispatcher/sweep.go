package dispatcher

import (
	"context"
	"strconv"
	"time"
)

// The windows after which a claim that has reported nothing is abandoned. Each
// must exceed the sum of timeout-minutes across that stage's whole workflow
// chain in run.yml, never just the model job's, or the sweep abandons a run
// still live in check or pr. Build: ticket 10 + model 60 + check 30 + pr-meta 10
// + pr 10 + record 10 = 130. Plan: ticket 10 + model 60 + record-plan-stage 10
// = 80. Each adds 30 minutes for Actions queueing; detection lags by up to
// pollInterval on top.
const (
	buildAbandonWindow = 160 * time.Minute
	planAbandonWindow  = 110 * time.Minute

	abandonedClass     = "abandoned:"
	abandonedNoticeKey = "abandoned-"
)

// AbandonWindows is how long a claim of each stage may go unrecorded before the
// sweep abandons it. A single-stage claim, which plans and builds in one job,
// takes the build window.
type AbandonWindows struct {
	Plan  time.Duration
	Build time.Duration
}

// Abandoned is a claim the sweep flipped: a run whose dispatch reached GitHub
// and never reported back.
type Abandoned struct {
	RunID  string
	Ticket string
	Repo   string
	Stage  string
	// Attempt is how many times the ticket had been requeued when it was claimed.
	Attempt int
}

// Sweeper flips claims that outlived their stage's window to abandoned,
// dropping their ledger reservations, and reports each one it flipped. A claim
// whose run recorded an outcome is left alone, and a run already flipped is not
// reported again.
type Sweeper interface {
	SweepAbandoned(ctx context.Context, now time.Time, w AbandonWindows) ([]Abandoned, error)
}

// sweepAbandoned abandons every claim past its window, which also raises the
// run's notice in the same transaction. The notice is not counted toward the
// circuit breaker: a dead runner is not a credential or allowance fault.
func sweepAbandoned(ctx context.Context, d Deps) error {
	abandoned, err := d.Queue.SweepAbandoned(ctx, d.Now(), AbandonWindows{Plan: planAbandonWindow, Build: buildAbandonWindow})
	for _, a := range abandoned {
		d.Logger.Warn("runAbandoned", "run", a.RunID, "stage", a.Stage, "attempt", a.Attempt)
	}
	return err
}

// Notice is the notice the sweep raises for the run, keyed by run and attempt so
// a run is announced once. Its class names the run and, when it is a different
// string, the ticket, then the command that requeues the run; all of it is ids,
// never text a ticket wrote.
func (a Abandoned) Notice() Notice {
	class := abandonedClass + a.RunID
	if a.Ticket != a.RunID {
		class += " ticket " + a.Ticket
	}
	class += " requeue with: runner requeue -run-id " + a.RunID
	return Notice{ID: abandonedNoticeKey + a.RunID + "-" + strconv.Itoa(a.Attempt), Class: class, Link: a.link()}
}

// link is the repository's Actions page: a claimed row holds no URL of its own
// run, and its run_url is a stale earlier one.
func (a Abandoned) link() string {
	if a.Repo == "" {
		return ""
	}
	return "https://github.com/" + a.Repo + "/actions"
}
