package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// projects returns a Projects over the emulator's client.
func projects(t *testing.T) (*Projects, *firestore.Client) {
	t.Helper()
	_, client := queue(t)
	return NewProjects(client), client
}

// cleanupProjects removes the repositories a test listed: the emulator outlives
// one run of the suite, so a document an interrupted run left behind must not
// read as this test's own.
func cleanupProjects(t *testing.T, client *firestore.Client, repositories ...string) {
	t.Helper()
	t.Cleanup(func() {
		for _, repo := range repositories {
			_, _ = client.Collection(projectsCollection).Doc(projectDocID(repo)).Delete(context.Background())
		}
	})
}

func TestProjectsAddStoresTheProjectItsRepositoryAndWhen(t *testing.T) {
	p, client := projects(t)
	ctx := context.Background()
	repo := "Octo/" + fresh("Widget")
	cleanupProjects(t, client, repo)
	at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	if err := p.Add(ctx, "Roadmap", repo, at); err != nil {
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
	if stored.LinearProject != "Roadmap" || stored.Repository != repo || !stored.AddedAt.Equal(at) {
		t.Fatalf("stored %+v, want Roadmap, %s and %v", stored, repo, at)
	}
}

func TestProjectsAddRefusesAListedRepositoryUnderAnotherCase(t *testing.T) {
	p, client := projects(t)
	ctx := context.Background()
	repo := "Octo/" + fresh("Widget")
	cleanupProjects(t, client, repo)
	at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	if err := p.Add(ctx, "Roadmap", repo, at); err != nil {
		t.Fatal(err)
	}
	again := strings.ToLower(repo)
	err := p.Add(ctx, "Other", again, at.Add(time.Hour))
	if !errors.Is(err, ErrRepositoryListed) || !strings.Contains(err.Error(), again) {
		t.Fatalf("err = %v, want ErrRepositoryListed naming %s", err, again)
	}
	snap, err := client.Collection(projectsCollection).Doc(projectDocID(repo)).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var stored projectDoc
	if err := snap.DataTo(&stored); err != nil {
		t.Fatal(err)
	}
	if stored.LinearProject != "Roadmap" || !stored.AddedAt.Equal(at) {
		t.Fatalf("stored %+v after the refused add, want the first project and time", stored)
	}
}

func TestProjectsRemoveDeletesTheListedProject(t *testing.T) {
	p, client := projects(t)
	ctx := context.Background()
	repo := "Octo/" + fresh("Widget")
	cleanupProjects(t, client, repo)
	at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	if err := p.Add(ctx, "Roadmap", repo, at); err != nil {
		t.Fatal(err)
	}
	if removed, err := p.Remove(ctx, strings.ToLower(repo)); err != nil || !removed {
		t.Fatalf("Remove = %v, %v, want true and no error", removed, err)
	}
	if _, err := client.Collection(projectsCollection).Doc(projectDocID(repo)).Get(ctx); status.Code(err) != codes.NotFound {
		t.Fatalf("%s reads after its remove: %v", repo, err)
	}
}

func TestProjectsRemoveOfAnUnlistedRepositorySaysSo(t *testing.T) {
	p, client := projects(t)
	ctx := context.Background()
	repo := "Octo/" + fresh("Absent")
	cleanupProjects(t, client, repo)
	removed, err := p.Remove(ctx, repo)
	if removed || err != nil {
		t.Fatalf("Remove = %v, %v, want false and no error", removed, err)
	}
	if _, err := client.Collection(projectsCollection).Doc(projectDocID(repo)).Get(ctx); status.Code(err) != codes.NotFound {
		t.Fatalf("a refused remove wrote %s: %v", repo, err)
	}
}

func TestProjectsRepositoriesReturnsEveryListedRepository(t *testing.T) {
	p, client := projects(t)
	ctx := context.Background()
	first := "Octo/" + fresh("Widget")
	second := "Octo/" + fresh("Gadget")
	cleanupProjects(t, client, first, second)
	at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	for _, repo := range []string{first, second} {
		if err := p.Add(ctx, "Roadmap", repo, at); err != nil {
			t.Fatal(err)
		}
	}
	got, err := p.Repositories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{first, second} {
		if !slices.Contains(got, want) {
			t.Fatalf("Repositories = %v, want it to include %s", got, want)
		}
	}
}
