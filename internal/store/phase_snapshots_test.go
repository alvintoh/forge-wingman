package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

func TestPhaseSnapshotsRoundTripKeyedByLowerCasedRepository(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	repo := "Octo/" + fresh("Widget")
	t.Cleanup(func() {
		_, _ = client.Collection(phaseSnapshotCollection).Doc(projectDocID(repo)).Delete(context.Background())
	})
	at := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	want := dispatcher.PhaseSnapshot{
		Repository: repo, Current: "P2", At: at,
		Phases:   []dispatcher.PhaseProgress{{Name: "P1", Done: 2, Total: 2}, {Name: "P2", Total: 3}},
		InFlight: []dispatcher.PhaseTicket{{ID: "FRG-3", Title: "Surface merges", Stage: dispatcher.PhaseStageBuilding}},
		Queued:   1, QueuedBlocked: 1, Eligible: []string{"FRG-4"}, EligibleBlocked: 1,
	}
	s := NewPhaseSnapshots(client)
	if err := s.RecordSnapshots(ctx, []dispatcher.PhaseSnapshot{want}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Snapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	read, ok := got[repoKey(repo)]
	if !ok || read.Current != "P2" || !read.At.Equal(at) || !slices.Equal(read.Phases, want.Phases) ||
		!slices.Equal(read.InFlight, want.InFlight) || read.Queued != 1 || read.QueuedBlocked != 1 || read.EligibleBlocked != 1 || !slices.Equal(read.Eligible, want.Eligible) {
		t.Fatalf("read %+v (found %v), want %+v", read, ok, want)
	}
}

func TestReachesReadsWhatRecordReachWrote(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	repo := "Octo/" + fresh("Widget")
	t.Cleanup(func() { _, _ = client.Collection(reachCollection).Doc(projectDocID(repo)).Delete(context.Background()) })
	at := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	r := NewReaches(client)
	if err := r.RecordReach(ctx, []dispatcher.RepoReach{{Repository: repo, Reason: dispatcher.ReachForbidden, Status: 403, CheckedAt: at}}); err != nil {
		t.Fatal(err)
	}
	got, err := r.Reads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	read := got[repoKey(repo)]
	if read.Reachable || read.Reason != dispatcher.ReachForbidden || read.Status != 403 || !read.CheckedAt.Equal(at) {
		t.Fatalf("read %+v, want a forbidden 403 read at %v", read, at)
	}
}

func TestPhasesStartRequestIsRecordedAndOnlyThatRequestIsResolved(t *testing.T) {
	p, client := projects(t)
	ctx := context.Background()
	repo := "Octo/" + fresh("Widget")
	cleanupProjects(t, client, repo)
	phases := NewPhases(client)
	if err := phases.RequestStart(ctx, repo, "P1", phaseAt); !errors.Is(err, ErrRepositoryNotListed) {
		t.Fatalf("a start on an unlisted repository = %v, want ErrRepositoryNotListed", err)
	}
	if err := p.Add(ctx, "Roadmap", repo, phaseAt.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	listed := func() dispatcher.PhaseProject {
		t.Helper()
		all, err := phases.Listed(ctx)
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(all, func(p dispatcher.PhaseProject) bool { return p.Repository == repo })
		if i < 0 {
			t.Fatalf("%s is not listed", repo)
		}
		return all[i]
	}
	if err := phases.RequestStart(ctx, repo, "P1", phaseAt); err != nil {
		t.Fatal(err)
	}
	if got := listed(); got.LinearProject != "Roadmap" || got.StartPhase != "P1" || !got.StartRequestedAt.Equal(phaseAt) {
		t.Fatalf("listed %+v, want the request beside the project's own fields", got)
	}

	// A request made after the one being settled is left pending.
	if err := phases.ResolveStart(ctx, repo, phaseAt.Add(-time.Minute), "phase-moved"); err != nil {
		t.Fatal(err)
	}
	if got := listed(); got.StartPhase != "P1" || got.StartRefusal != "" {
		t.Fatalf("listed %+v, want a newer request untouched", got)
	}
	if err := phases.ResolveStart(ctx, repo, phaseAt, "phase-moved"); err != nil {
		t.Fatal(err)
	}
	if got := listed(); !got.StartRequestedAt.IsZero() || got.StartPhase != "" || got.StartRefusal != "phase-moved" {
		t.Fatalf("listed %+v, want the request cleared and its refusal recorded", got)
	}
}

func TestTicketRunsReadsEndedQueuedAndInFlightAndLastActivity(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	repo := "octo/" + fresh("activity")
	queued, stopped, running, absent := fresh("held"), fresh("stopped"), fresh("running"), fresh("absent")
	settled := fresh("settled")
	forget(t, client, queued, stopped, running, settled)
	for _, id := range []string{queued, stopped, running, settled} {
		run := queuedRun(id, 1)
		run.Repo = repo
		if err := q.Enqueue(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.Collection(runsCollection).Doc(stopped).Update(ctx, []firestore.Update{{Path: stateField, Value: stateStopped}}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Collection(runsCollection).Doc(settled).Update(ctx, []firestore.Update{{Path: outcomeField, Value: "failed"}}); err != nil {
		t.Fatal(err)
	}
	newest := queueAt.Add(time.Hour)
	if _, err := client.Collection(runsCollection).Doc(running).Update(ctx, []firestore.Update{
		{Path: stateField, Value: stateClaimed}, {Path: updatedAtField, Value: newest}}); err != nil {
		t.Fatal(err)
	}
	seedLedger(t, client, 2, map[string]reservationEntry{running: {Repo: repo, TicketID: running, Stage: dispatcher.StagePlan}})

	runs, err := q.TicketRuns(ctx, []string{queued, stopped, running, settled, absent})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]dispatcher.TicketRun{
		queued:  {Queued: true},
		stopped: {Ended: true},
		settled: {Ended: true},
		running: {Stage: dispatcher.PhaseStagePlanning},
	}
	if len(runs) != len(want) {
		t.Fatalf("runs = %v, want %v (a ticket with no row absent)", runs, want)
	}
	for id, w := range want {
		if runs[id] != w {
			t.Errorf("run of %s = %+v, want %+v", id, runs[id], w)
		}
	}

	if at, err := q.LastActivity(ctx, repo); err != nil || !at.Equal(newest) {
		t.Errorf("LastActivity = %v, %v, want the newest row's %v", at, err, newest)
	}
	if at, err := q.LastActivity(ctx, "octo/"+fresh("quiet")); err != nil || !at.IsZero() {
		t.Errorf("LastActivity of a repository with no run = %v, %v, want zero", at, err)
	}
}
