package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// TestWithdrawRunsWithdrawsOnlyTheRunsStillWaitingForAClaim: a run queued or
// planned for a closed ticket is withdrawn with the reason and the moment, and a
// run claimed, waiting, stopped or abandoned is left as it is (AC1, AC3).
func TestWithdrawRunsWithdrawsOnlyTheRunsStillWaitingForAClaim(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	prefix := fresh(fmt.Sprintf("withdraw-%d", time.Now().UnixNano()))
	id := func(name string) string { return prefix + "-" + name }
	rows := map[string]map[string]any{}
	rows["queued"] = map[string]any{stateField: stateQueued}
	rows["planned"] = map[string]any{stateField: statePlanned}
	rows["claimed"] = map[string]any{stateField: stateClaimed}
	rows["waiting"] = map[string]any{stateField: stateWaiting}
	rows["stopped"] = map[string]any{
		stateField:      stateStopped,
		outcomeField:    string(runner.OutcomeStopped),
		stopReasonField: string(runner.StopWaitingOnOwner),
	}
	rows["abandoned"] = map[string]any{stateField: stateAbandoned}
	reasons := map[string]string{}
	for name, row := range rows {
		row[ticketIDField] = id(name)
		row[updatedAtField] = queueAt
		reasons[id(name)] = "completed"
		seedRow(t, client, id(name), row)
	}
	var logged bytes.Buffer
	q = q.WithLogger(slog.New(slog.NewTextHandler(&logged, nil)))

	withdrawn, err := q.WithdrawRuns(ctx, reasons, queueAt.Add(time.Minute))
	if err != nil || withdrawn != 2 {
		t.Fatalf("withdrawn = %d, %v, want the queued and planned runs alone", withdrawn, err)
	}
	for _, name := range []string{"queued", "planned"} {
		row := readRow(t, client, id(name))
		if row[stateField] != stateWithdrawn {
			t.Errorf("%s run state = %v, want %v", name, row[stateField], stateWithdrawn)
		}
		if at, _ := row[withdrawnAtField].(time.Time); !at.Equal(queueAt.Add(time.Minute)) {
			t.Errorf("%s run withdrawn at %v, want the poll's own moment", name, at)
		}
		if row[withdrawnReasonField] != "completed" {
			t.Errorf("%s run reason = %v, want the state type that closed the ticket", name, row[withdrawnReasonField])
		}
	}
	for _, name := range []string{"claimed", "waiting", "stopped", "abandoned"} {
		row := readRow(t, client, id(name))
		if row[stateField] != rows[name][stateField] {
			t.Errorf("%s run state = %v, want %v left as it was", name, row[stateField], rows[name][stateField])
		}
		if v, ok := row[withdrawnAtField]; ok {
			t.Errorf("%s run withdrawn at %v, want no withdrawal", name, v)
		}
	}
	if got := strings.Count(logged.String(), "msg=runWithdrawn"); got != 2 {
		t.Fatalf("log = %q, want one runWithdrawn per withdrawn run", logged.String())
	}
	for _, name := range []string{"queued", "planned"} {
		if !strings.Contains(logged.String(), "run="+id(name)) || !strings.Contains(logged.String(), "state_type=completed") {
			t.Errorf("log = %q, want %s's run and the ticket's state type as separate attributes", logged.String(), name)
		}
	}
	if withdrawn, err := q.WithdrawRuns(ctx, reasons, queueAt.Add(2*time.Minute)); err != nil || withdrawn != 0 {
		t.Fatalf("second poll withdrew %d, %v, want nothing written again", withdrawn, err)
	}
}

// TestUnclaimedTicketsListsOnlyTheRunsWaitingForAClaim: the read a poll asks
// Linear about names the queued and the planned, and no run that already moved
// on.
func TestUnclaimedTicketsListsOnlyTheRunsWaitingForAClaim(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	prefix := fresh(fmt.Sprintf("unclaimed-%d", time.Now().UnixNano()))
	id := func(name string) string { return prefix + "-" + name }
	rows := map[string]string{}
	rows["queued"] = stateQueued
	rows["planned"] = statePlanned
	rows["claimed"] = stateClaimed
	rows["waiting"] = stateWaiting
	rows["stopped"] = stateStopped
	rows["withdrawn"] = stateWithdrawn
	for name, state := range rows {
		seedRow(t, client, id(name), map[string]any{
			stateField:     state,
			ticketIDField:  id(name),
			updatedAtField: queueAt,
		})
	}
	tickets, err := q.UnclaimedTickets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"queued", "planned"} {
		if !slices.Contains(tickets, id(name)) {
			t.Errorf("the %s run is not listed among the waiting runs", name)
		}
	}
	for _, name := range []string{"claimed", "waiting", "stopped", "withdrawn"} {
		if slices.Contains(tickets, id(name)) {
			t.Errorf("the %s run is listed among the waiting runs", name)
		}
	}
}

// TestRequeueClearsTheWithdrawnMarkers: the operator's requeue is the way back
// from a withdrawal, so the row must not keep saying why and when it was
// withdrawn.
func TestRequeueClearsTheWithdrawnMarkers(t *testing.T) {
	q, client := queue(t)
	id := fresh(fmt.Sprintf("withdrawn-requeue-%d", time.Now().UnixNano()))
	row := map[string]any{stateField: stateWithdrawn}
	row[withdrawnAtField] = queueAt
	row[withdrawnReasonField] = "completed"
	row[reopenedAtField] = queueAt
	seedRow(t, client, id, row)

	if _, err := q.Requeue(context.Background(), id, queueAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	requeued := readRow(t, client, id)
	if requeued[stateField] != stateQueued {
		t.Fatalf("state = %v after a requeue, want %v", requeued[stateField], stateQueued)
	}
	if v, ok := requeued[withdrawnAtField]; ok {
		t.Fatalf("withdrawn at = %v after a requeue, want it cleared", v)
	}
	if v, ok := requeued[withdrawnReasonField]; ok {
		t.Fatalf("withdrawn reason = %v after a requeue, want it cleared", v)
	}
	if v, ok := requeued[reopenedAtField]; ok {
		t.Fatalf("reopened at = %v after a requeue, want it cleared", v)
	}
}

// TestRequeueReopenedReturnsOnlyAWithdrawnRun: a withdrawn run goes back to the
// queue with the moment its ticket was found reopened and its withdrawn markers
// cleared, and any other run is reported untouched (AC7).
func TestRequeueReopenedReturnsOnlyAWithdrawnRun(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	at := queueAt.Add(time.Minute)
	for name, tc := range map[string]struct {
		state, wantState string
		want             bool
	}{
		"withdrawn": {stateWithdrawn, stateQueued, true},
		"claimed":   {stateClaimed, stateClaimed, false},
	} {
		id := fresh(fmt.Sprintf("reopened-%s-%d", name, time.Now().UnixNano()))
		seedRow(t, client, id, map[string]any{
			stateField: tc.state, withdrawnAtField: queueAt, withdrawnReasonField: "completed",
		})
		requeued, err := q.RequeueReopened(ctx, id, at)
		if err != nil || requeued != tc.want {
			t.Fatalf("%s run: requeued = %v, %v, want %v", name, requeued, err, tc.want)
		}
		row := readRow(t, client, id)
		if row[stateField] != tc.wantState {
			t.Fatalf("%s run: state = %v, want %v", name, row[stateField], tc.wantState)
		}
		reopened, _ := row[reopenedAtField].(time.Time)
		if tc.want && (!reopened.Equal(at) || row[withdrawnReasonField] != nil) {
			t.Fatalf("%s run: reopened at %v, reason %v, want the moment recorded and the withdrawal cleared",
				name, reopened, row[withdrawnReasonField])
		}
		if !tc.want && !reopened.IsZero() {
			t.Fatalf("%s run: reopened at %v, want the row left as it was", name, reopened)
		}
	}
}

// TestExistsReportsAWithdrawnRun: admission tells a withdrawn run from any other
// held run by the read it already makes.
func TestExistsReportsAWithdrawnRun(t *testing.T) {
	q, client := queue(t)
	for name, tc := range map[string]struct {
		state               string
		exists, isWithdrawn bool
	}{
		"withdrawn": {stateWithdrawn, true, true},
		"queued":    {stateQueued, true, false},
		"absent":    {"", false, false},
	} {
		id := fresh(fmt.Sprintf("exists-%s-%d", name, time.Now().UnixNano()))
		if tc.state != "" {
			seedRow(t, client, id, map[string]any{stateField: tc.state})
		}
		exists, withdrawn, err := q.Exists(context.Background(), id)
		if err != nil || exists != tc.exists || withdrawn != tc.isWithdrawn {
			t.Fatalf("%s: exists %v, withdrawn %v, err %v", name, exists, withdrawn, err)
		}
	}
}

// TestRequeueAfterConflictLeavesAWithdrawnRunToAnOperator: the automatic requeue
// a rebase conflict asks for never returns a withdrawn run, which only the
// operator's own requeue takes further.
func TestRequeueAfterConflictLeavesAWithdrawnRunToAnOperator(t *testing.T) {
	q, client := queue(t)
	id := fresh(fmt.Sprintf("withdrawn-conflict-%d", time.Now().UnixNano()))
	seedRow(t, client, id, map[string]any{stateField: stateWithdrawn})

	if _, err := q.RequeueAfterConflict(context.Background(), id, queueAt.Add(time.Minute), 2); !errors.Is(err, ErrRunWithdrawn) {
		t.Fatalf("err = %v, want the withdrawn run left to an operator", err)
	}
	if state := readRow(t, client, id)[stateField]; state != stateWithdrawn {
		t.Fatalf("state = %v, want the withdrawn run untouched", state)
	}
}

// pollClosedTickets is the state read a poll makes: the state types a test gives
// a ticket, every other ticket left out as one Linear did not return.
type pollClosedTickets struct{ types map[string]string }

func (p pollClosedTickets) TicketStateTypes(_ context.Context, tickets []string) (map[string]string, error) {
	out := make(map[string]string, len(tickets))
	for _, ticket := range tickets {
		if stateType, ok := p.types[ticket]; ok {
			out[ticket] = stateType
		}
	}
	return out, nil
}

func (pollClosedTickets) StartTicket(context.Context, string) (string, error) { return "", nil }

func (pollClosedTickets) RestoreTicket(context.Context, string, string) error { return nil }

// TestAPollWithdrawsAQueuedRunWhoseTicketClosedBeforeClaimingIt: the withdrawal
// runs before the claim walk, so the poll that finds the closed ticket never
// dispatches the run, and its row records why and when (AC1).
func TestAPollWithdrawsAQueuedRunWhoseTicketClosedBeforeClaimingIt(t *testing.T) {
	q, client := queue(t)
	resetLedger(t, q.client, 1)
	id := pollCandidates(t, q, 1)[0]
	deps := pollReadDeps(q, 0)
	deps.TicketStates = pollClosedTickets{types: map[string]string{id: "completed"}}

	res, err := dispatcher.Poll(context.Background(), deps, pollReadConfig())
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(res.Dispatched, id) {
		t.Fatalf("dispatched %v, want the withdrawn run never claimed", res.Dispatched)
	}
	row := readRow(t, client, id)
	if row[stateField] != stateWithdrawn {
		t.Fatalf("state = %v, want %v", row[stateField], stateWithdrawn)
	}
	if at, _ := row[withdrawnAtField].(time.Time); !at.Equal(pollAt) {
		t.Fatalf("withdrawn at %v, want the poll's own moment %v", at, pollAt)
	}
	if reason := row[withdrawnReasonField]; reason != "completed" {
		t.Fatalf("reason = %v, want the state type that closed the ticket", reason)
	}
}
