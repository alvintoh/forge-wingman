package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var phaseAt = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

func TestPhasesSetKeepsTheProjectsOtherFields(t *testing.T) {
	p, client := projects(t)
	ctx := context.Background()
	repo := "Octo/" + fresh("Widget")
	cleanupProjects(t, client, repo)
	added := phaseAt.Add(-time.Hour)
	if err := p.Add(ctx, "Roadmap", repo, added); err != nil {
		t.Fatal(err)
	}
	if err := NewPhases(client).Set(ctx, repo, true, phaseAt); err != nil {
		t.Fatal(err)
	}
	snap, err := client.Collection(projectsCollection).Doc(projectDocID(repo)).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var stored projectDoc
	if err := snap.DataTo(&stored); err != nil {
		t.Fatal(err)
	}
	if stored.LinearProject != "Roadmap" || stored.Repository != repo || !stored.AddedAt.Equal(added) {
		t.Fatalf("stored %+v, want the project's own fields as added", stored)
	}
	data := snap.Data()
	if paused, _ := data[pausedField].(bool); !paused {
		t.Fatalf("paused = %v, want true", data[pausedField])
	}
	if at, _ := data[pausedAtField].(time.Time); !at.Equal(phaseAt) {
		t.Fatalf("paused_at = %v, want %v", data[pausedAtField], phaseAt)
	}
}

func TestPhasesSetRefusesARepositoryNoProjectLists(t *testing.T) {
	_, client := projects(t)
	ctx := context.Background()
	repo := "Octo/" + fresh("Unlisted")
	cleanupProjects(t, client, repo)
	err := NewPhases(client).Set(ctx, repo, true, phaseAt)
	if !errors.Is(err, ErrRepositoryNotListed) {
		t.Fatalf("err = %v, want ErrRepositoryNotListed", err)
	}
	_, err = client.Collection(projectsCollection).Doc(projectDocID(repo)).Get(ctx)
	if status.Code(err) != codes.NotFound {
		t.Fatalf("Get after the refused set = %v, want the document absent", err)
	}
}

func TestPhasesPausedListsOnlyTheProjectsTheOwnerPaused(t *testing.T) {
	p, client := projects(t)
	ctx := context.Background()
	phases := NewPhases(client)
	stopped, running, resumed := "Octo/"+fresh("Stopped"), "Octo/"+fresh("Running"), "Octo/"+fresh("Resumed")
	cleanupProjects(t, client, stopped, running, resumed)
	for _, repo := range []string{stopped, running, resumed} {
		if err := p.Add(ctx, "Roadmap", repo, phaseAt); err != nil {
			t.Fatal(err)
		}
	}
	for repo, paused := range map[string]bool{stopped: true, resumed: false} {
		if err := phases.Set(ctx, repo, paused, phaseAt); err != nil {
			t.Fatal(err)
		}
	}
	got, err := phases.Paused(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got[strings.ToLower(stopped)] || got[strings.ToLower(running)] || got[strings.ToLower(resumed)] {
		t.Fatalf("paused = %v, want only %s", got, strings.ToLower(stopped))
	}
}
