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

// seedRunRow writes a run row in state, holding title unless it is empty.
func seedRunRow(t *testing.T, client *firestore.Client, id, state, title string) {
	t.Helper()
	row := map[string]any{ticketIDField: id, repoField: "octo/scratch", stateField: state}
	if title != "" {
		row[ticketTitleField] = title
	}
	if _, err := client.Collection(runsCollection).Doc(id).Set(context.Background(), row); err != nil {
		t.Fatal(err)
	}
}

func TestRunningListsOneEntryPerReservationWithItsRowsTitle(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	plan, single, build := fresh("running-plan"), fresh("running-single"), fresh("running-build")
	untitled := fresh("running-untitled")
	forget(t, client, plan, single, build, untitled)
	seedRunRow(t, client, plan, stateClaimed, "Plan the widget")
	seedRunRow(t, client, single, stateClaimed, "Build the widget")
	seedRunRow(t, client, build, stateClaimed, "Build the gadget")
	seedRunRow(t, client, untitled, stateClaimed, "")
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
		plan:     {Ticket: plan, Repo: "octo/scratch", Stage: "plan", ClaimedAt: queueAt, Title: "Plan the widget"},
		single:   {Ticket: single, Repo: "octo/scratch", ClaimedAt: queueAt.Add(time.Minute), Title: "Build the widget"},
		build:    {Ticket: build, Repo: "octo/scratch", Stage: "build", ClaimedAt: queueAt.Add(2 * time.Minute), Title: "Build the gadget"},
		untitled: {Ticket: untitled, Repo: "octo/scratch", Stage: "build", ClaimedAt: queueAt.Add(3 * time.Minute)},
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

func TestRunningLeavesOutARunWhosePlanHasSettled(t *testing.T) {
	q, client := queue(t)
	planned, building := fresh("running-planned"), fresh("running-claimed")
	forget(t, client, planned, building)
	seedRunRow(t, client, planned, statePlanned, "Plan the widget")
	seedRunRow(t, client, building, stateClaimed, "Build the gadget")
	// A planned run holds no reservation: its plan stage released it and its
	// build has not claimed one yet.
	seedLedger(t, client, 1, bookedReservations(
		reservationEntry{TicketID: building, Repo: "octo/scratch", Stage: dispatcher.StageBuild, ClaimedAt: queueAt},
	))
	runs, err := q.Running(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Ticket != building {
		t.Fatalf("runs = %+v, want only the run that holds a reservation, %s", runs, building)
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
	if got.Ticket != gone || got.Repo != "octo/scratch" || got.Stage != "build" || got.Title != "" || !got.ClaimedAt.Equal(queueAt) {
		t.Fatalf("entry = %+v, want the reservation with an empty title", got)
	}
}
