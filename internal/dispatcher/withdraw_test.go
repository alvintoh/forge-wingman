package dispatcher

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
)

// TestPollWithdrawsAWaitingRunWhoseTicketClosed: a run queued for a ticket the
// owner has since completed is withdrawn at the poll's own moment, before the
// claim walk reads the queue.
func TestPollWithdrawsAWaitingRunWhoseTicketClosed(t *testing.T) {
	q := &fakeQueue{unclaimed: []string{"FRG-9"}}
	states := &fakeTicketStates{stateTypes: map[string]string{"FRG-9": stateTypeCompleted}}
	d := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	d.TicketStates = states
	if _, err := Poll(context.Background(), d, buildConfig); err != nil {
		t.Fatal(err)
	}
	if got := q.withdrawn; len(got) != 1 || got["FRG-9"] != stateTypeCompleted {
		t.Fatalf("withdrawn = %v, want the queued run of a completed ticket", got)
	}
	if !q.withdrawAt.Equal(pollAt) {
		t.Fatalf("withdrawn at %v, want the poll's own moment %v", q.withdrawAt, pollAt)
	}
}

// TestPollAsksAboutTheWaitingRunsAlone: only a run still waiting for a claim can
// be withdrawn, so a ticket whose run is claimed, waiting or settled is neither
// read nor touched, whatever its state.
func TestPollAsksAboutTheWaitingRunsAlone(t *testing.T) {
	q := &fakeQueue{unclaimed: []string{"FRG-1", "FRG-2"}}
	states := &fakeTicketStates{stateTypes: map[string]string{
		"FRG-1": "started",
		"FRG-2": stateTypeCanceled,
		"FRG-3": stateTypeCompleted,
	}}
	d := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	d.TicketStates = states
	if _, err := Poll(context.Background(), d, buildConfig); err != nil {
		t.Fatal(err)
	}
	if got := q.withdrawn; len(got) != 1 || got["FRG-2"] != stateTypeCanceled {
		t.Fatalf("withdrawn = %v, want only the canceled ticket's run", got)
	}
	if len(states.stateReads) != 1 || !slices.Equal(states.stateReads[0], []string{"FRG-1", "FRG-2"}) {
		t.Fatalf("state reads = %v, want the waiting runs alone", states.stateReads)
	}
}

// TestPollAsksLinearNothingWhenNoRunWaits: an empty queue costs no Linear read.
func TestPollAsksLinearNothingWhenNoRunWaits(t *testing.T) {
	q := &fakeQueue{}
	states := &fakeTicketStates{stateTypes: map[string]string{"FRG-9": stateTypeCompleted}}
	d := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	d.TicketStates = states
	if _, err := Poll(context.Background(), d, buildConfig); err != nil {
		t.Fatal(err)
	}
	if len(states.stateReads) != 0 || len(q.withdrawn) != 0 {
		t.Fatalf("state reads = %v, withdrawn = %v, want nothing asked of Linear for an empty queue",
			states.stateReads, q.withdrawn)
	}
}

// TestPollWithdrawsNothingWhenTheTicketReadFails (AC4): a Linear read that fails
// withdraws nothing on that poll, logs once, and never fails the poll itself.
func TestPollWithdrawsNothingWhenTheTicketReadFails(t *testing.T) {
	q := &fakeQueue{unclaimed: []string{"FRG-9"}}
	var logs bytes.Buffer
	d := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	d.TicketStates = &fakeTicketStates{
		stateTypes:    map[string]string{"FRG-9": stateTypeCompleted},
		stateTypesErr: errFirestore,
	}
	d.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	if _, err := Poll(context.Background(), d, buildConfig); err != nil {
		t.Fatalf("err = %v, want the poll to carry on without the read", err)
	}
	if len(q.withdrawn) != 0 {
		t.Fatalf("withdrawn = %v, want nothing withdrawn on a failed read", q.withdrawn)
	}
	if got := strings.Count(logs.String(), "msg=ticketStatesUnread"); got != 1 {
		t.Fatalf("log = %q, want one ticketStatesUnread", logs.String())
	}
	if !strings.Contains(logs.String(), `err="`+errFirestore.Error()+`"`) {
		t.Fatalf("log = %q, want the reason as its own field", logs.String())
	}
}

// TestPollWithdrawsNothingWhenTheWaitingRunsCannotBeRead (AC4): a store read that
// fails withdraws nothing and asks Linear nothing, so a closed ticket is never
// inferred from a failed listing.
func TestPollWithdrawsNothingWhenTheWaitingRunsCannotBeRead(t *testing.T) {
	q := &fakeQueue{unclaimedErr: errFirestore}
	var logs bytes.Buffer
	states := &fakeTicketStates{}
	d := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	d.TicketStates = states
	d.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	if _, err := Poll(context.Background(), d, buildConfig); err != nil {
		t.Fatalf("err = %v, want the poll to carry on without the listing", err)
	}
	if len(states.stateReads) != 0 || len(q.withdrawn) != 0 {
		t.Fatalf("state reads = %v, withdrawn = %v, want nothing withdrawn", states.stateReads, q.withdrawn)
	}
	if got := strings.Count(logs.String(), "msg=unclaimedTicketsUnread"); got != 1 {
		t.Fatalf("log = %q, want one unclaimedTicketsUnread", logs.String())
	}
}

// TestPollReportsAFailedWithdrawalWithoutFailingThePoll: a write that fails is
// logged and leaves the poll running, as every other store warning does.
func TestPollReportsAFailedWithdrawalWithoutFailingThePoll(t *testing.T) {
	q := &fakeQueue{unclaimed: []string{"FRG-9"}, withdrawErr: errFirestore}
	var logs bytes.Buffer
	d := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	d.TicketStates = &fakeTicketStates{stateTypes: map[string]string{"FRG-9": stateTypeCompleted}}
	d.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	if _, err := Poll(context.Background(), d, buildConfig); err != nil {
		t.Fatalf("err = %v, want the poll to carry on", err)
	}
	if got := strings.Count(logs.String(), "msg=runsNotWithdrawn"); got != 1 {
		t.Fatalf("log = %q, want one runsNotWithdrawn", logs.String())
	}
}

// TestPollRequeuesAWithdrawnRunWhoseTicketIsDelegatedAgain: Delegated lists only
// open tickets, so a withdrawn run's ticket among them was reopened and its run
// goes back to the queue, while any other run already held is logged as already
// queued and left as it is (AC7).
func TestPollRequeuesAWithdrawnRunWhoseTicketIsDelegatedAgain(t *testing.T) {
	for name, tc := range map[string]struct {
		withdrawn    map[string]string
		wantReopened []string
		wantLog      string
		notLog       string
	}{
		"withdrawn":     {map[string]string{"FRG-18": stateTypeCompleted}, []string{"FRG-18"}, "withdrawnRunRequeued", "ticketAlreadyQueued"},
		"not withdrawn": {nil, nil, "ticketAlreadyQueued", "withdrawnRunRequeued"},
	} {
		t.Run(name, func(t *testing.T) {
			q := &fakeQueue{holds: map[string]bool{"FRG-18": true}, withdrawn: tc.withdrawn}
			var logged bytes.Buffer
			d := pollDeps(fakeSource{issues: []Issue{admitted("size:M", "repo:octo/scratch")}}, q, &fakeWorkflow{})
			d.Logger = slog.New(slog.NewTextHandler(&logged, nil))
			if _, err := Poll(context.Background(), d, buildConfig); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(q.reopened, tc.wantReopened) {
				t.Fatalf("reopened = %v, want %v", q.reopened, tc.wantReopened)
			}
			if !strings.Contains(logged.String(), tc.wantLog) || strings.Contains(logged.String(), tc.notLog) {
				t.Fatalf("log = %q, want %s and no %s", logged.String(), tc.wantLog, tc.notLog)
			}
		})
	}
}

// TestPollFailsWhenAReopenedTicketsRunCannotBeRequeued: the requeue's failure is
// the poll's, so the ticket is tried again next poll.
func TestPollFailsWhenAReopenedTicketsRunCannotBeRequeued(t *testing.T) {
	q := &fakeQueue{holds: map[string]bool{"FRG-18": true}, withdrawn: map[string]string{"FRG-18": stateTypeCompleted},
		reopenErr: errFirestore}
	d := pollDeps(fakeSource{issues: []Issue{admitted("size:M", "repo:octo/scratch")}}, q, &fakeWorkflow{})
	if _, err := Poll(context.Background(), d, buildConfig); !errors.Is(err, errFirestore) {
		t.Fatalf("err = %v, want the requeue's failure", err)
	}
}
