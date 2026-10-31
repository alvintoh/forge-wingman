package store

import (
	"context"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

// bookedReservations keys each claim the way a claim books its reservation.
func bookedReservations(claims ...reservationEntry) map[string]reservationEntry {
	reservations := make(map[string]reservationEntry, len(claims))
	for _, claim := range claims {
		reservations[reservationKey(claim.TicketID, claim.Attempt, claim.Stage)] = claim
	}
	return reservations
}

func TestRunningListsOneEntryPerReservationWithItsRowsTitle(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	plan, single, build := fresh("running-plan"), fresh("running-single"), fresh("running-build")
	untitled := fresh("running-untitled")
	forget(t, client, plan, single, build, untitled)
	for id, title := range map[string]string{
		plan: "Plan the widget", single: "Build the widget", build: "Build the gadget",
	} {
		row := map[string]any{
			ticketIDField: id, repoField: "octo/scratch", stateField: stateClaimed, ticketTitleField: title,
		}
		if _, err := client.Collection(runsCollection).Doc(id).Set(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	// A row claimed before the claim recorded a title serves an empty one.
	if _, err := client.Collection(runsCollection).Doc(untitled).Set(ctx, map[string]any{
		ticketIDField: untitled, repoField: "octo/scratch", stateField: stateClaimed,
	}); err != nil {
		t.Fatal(err)
	}
	seedLedger(t, client, 4, bookedReservations(
		reservationEntry{TicketID: plan, Repo: "octo/scratch", Stage: dispatcher.StagePlan, ClaimedAt: queueAt},
		reservationEntry{TicketID: single, Repo: "octo/scratch", ClaimedAt: queueAt.Add(time.Minute)},
		reservationEntry{TicketID: build, Repo: "octo/scratch", Stage: dispatcher.StageBuild, ClaimedAt: queueAt.Add(2 * time.Minute)},
		reservationEntry{TicketID: untitled, Repo: "octo/scratch", Stage: dispatcher.StageBuild, ClaimedAt: queueAt.Add(3 * time.Minute)},
	))
	runs, err := q.Running(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Running{
		plan:     {Ticket: plan, Repo: "octo/scratch", Stage: dispatcher.StagePlan, ClaimedAt: queueAt, Title: "Plan the widget"},
		single:   {Ticket: single, Repo: "octo/scratch", ClaimedAt: queueAt.Add(time.Minute), Title: "Build the widget"},
		build:    {Ticket: build, Repo: "octo/scratch", Stage: dispatcher.StageBuild, ClaimedAt: queueAt.Add(2 * time.Minute), Title: "Build the gadget"},
		untitled: {Ticket: untitled, Repo: "octo/scratch", Stage: dispatcher.StageBuild, ClaimedAt: queueAt.Add(3 * time.Minute)},
	}
	if len(runs) != len(want) {
		t.Fatalf("runs = %+v, want one entry per reservation", runs)
	}
	for _, run := range runs {
		entry, ok := want[run.Ticket]
		if !ok {
			t.Errorf("unexpected entry %+v", run)
			continue
		}
		if run.Repo != entry.Repo || run.Stage != entry.Stage || run.Title != entry.Title || !run.ClaimedAt.Equal(entry.ClaimedAt) {
			t.Errorf("entry %s = %+v, want %+v", run.Ticket, run, entry)
		}
	}
}

func TestRunningServesAnEmptyLedgerAsNoEntries(t *testing.T) {
	q, client := queue(t)
	resetLedger(t, client, 1)
	runs, err := q.Running(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if runs == nil || len(runs) != 0 {
		t.Fatalf("runs = %v, want an empty list", runs)
	}
}

func TestRunningServesAnEmptyTitleForAReservationWithNoRow(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	gone := fresh("running-gone")
	// A reservation the ledger holds for a row that is not there, which a suite
	// run interrupted before its cleanup could also leave behind.
	if _, err := client.Collection(runsCollection).Doc(gone).Delete(ctx); err != nil {
		t.Fatal(err)
	}
	seedLedger(t, client, 1, bookedReservations(
		reservationEntry{TicketID: gone, Repo: "octo/scratch", Stage: dispatcher.StageBuild, ClaimedAt: queueAt},
	))
	runs, err := q.Running(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want the one reservation", runs)
	}
	got := runs[0]
	if got.Ticket != gone || got.Repo != "octo/scratch" || got.Stage != dispatcher.StageBuild || got.Title != "" || !got.ClaimedAt.Equal(queueAt) {
		t.Fatalf("entry = %+v, want the reservation with an empty title", got)
	}
}

func TestTryClaimRecordsTheTicketsTitle(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("running-claim"), 1)
	forget(t, client, run.RunID)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	// A row queued before the claim recorded a title, which only the title the
	// poll read can supply.
	if _, err := client.Collection(runsCollection).Doc(run.RunID).Update(ctx,
		[]firestore.Update{{Path: ticketTitleField, Value: firestore.Delete}}); err != nil {
		t.Fatal(err)
	}
	resetLedger(t, client, 1)
	facts := openFacts
	facts.Title = "Add the running view"
	if ok, binding, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, facts, dispatcher.Reservation{}); err != nil || !ok {
		t.Fatalf("claim = %v, %q, %v", ok, binding, err)
	}
	snap, err := client.Collection(runsCollection).Doc(run.RunID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := str(snap.Data(), ticketTitleField); got != "Add the running view" {
		t.Fatalf("title = %q, want the claim's own", got)
	}
}

func TestTryClaimKeepsATitleTheRowAlreadyHolds(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("running-keeps"), 1)
	forget(t, client, run.RunID)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	resetLedger(t, client, 1)
	// A title Linear holds now is not the one admission validated, so the claim
	// leaves the row's own: the runner refuses a title it cannot build.
	facts := openFacts
	facts.Title = "a title Linear holds now"
	if ok, binding, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, facts, dispatcher.Reservation{}); err != nil || !ok {
		t.Fatalf("claim = %v, %q, %v", ok, binding, err)
	}
	snap, err := client.Collection(runsCollection).Doc(run.RunID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := str(snap.Data(), ticketTitleField); got != run.Ticket.Title {
		t.Fatalf("title = %q, want the row's own %q", got, run.Ticket.Title)
	}
}
