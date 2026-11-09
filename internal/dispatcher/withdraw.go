package dispatcher

import "context"

// withdrawClosed withdraws every run still waiting for a claim whose ticket
// Linear holds completed or canceled, so a ticket closed after it was queued is
// never built. A Linear or store read that fails withdraws nothing this poll,
// and a run claimed, waiting on the owner or settled is left as it is, whatever
// its ticket's state.
func withdrawClosed(ctx context.Context, d Deps) {
	tickets, err := d.Queue.UnclaimedTickets(ctx)
	if err != nil {
		d.Logger.Warn("unclaimedTicketsUnread", "err", err.Error())
		return
	}
	if len(tickets) == 0 {
		return
	}
	types, err := d.TicketStates.TicketStateTypes(ctx, tickets)
	if err != nil {
		d.Logger.Warn("ticketStatesUnread", "err", err.Error())
		return
	}
	closed := make(map[string]string, len(tickets))
	for _, ticket := range tickets {
		if stateType := types[ticket]; stateType == stateTypeCompleted || stateType == stateTypeCanceled {
			closed[ticket] = stateType
		}
	}
	if len(closed) == 0 {
		return
	}
	if _, err := d.Queue.WithdrawRuns(ctx, closed, d.Now()); err != nil {
		d.Logger.Warn("runsNotWithdrawn", "tickets", len(closed), "err", err.Error())
	}
}

// requeueReopened returns the run of an already-queued ticket to the queue
// when that run was withdrawn: Delegated lists only open tickets, so a withdrawn
// run's ticket in the list was reopened and delegated again. Any other run is
// logged as already queued.
func requeueReopened(ctx context.Context, d Deps, q Queued, withdrawn bool) error {
	if withdrawn {
		requeued, err := d.Queue.RequeueReopened(ctx, q.RunID, d.Now())
		if err != nil {
			return err
		}
		if requeued {
			d.Logger.Info("withdrawnRunRequeued", "run", q.RunID)
			return nil
		}
	}
	d.Logger.Info("ticketAlreadyQueued", "run", q.RunID, "repo", q.Repo, "priority", q.Priority)
	return nil
}
