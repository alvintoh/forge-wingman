package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// fakeProjectStore holds the projects a subcommand lists and removes.
type fakeProjectStore struct {
	listed    map[string]string
	addedAt   time.Time
	removed   []string
	addErr    error
	removeErr error
}

func (s *fakeProjectStore) Add(_ context.Context, linearProject, repository string, at time.Time) error {
	if s.addErr != nil {
		return s.addErr
	}
	if s.listed == nil {
		s.listed = map[string]string{}
	}
	s.listed[repository] = linearProject
	s.addedAt = at
	return nil
}

func (s *fakeProjectStore) Remove(_ context.Context, repository string) (bool, error) {
	if s.removeErr != nil {
		return false, s.removeErr
	}
	if _, ok := s.listed[repository]; !ok {
		return false, nil
	}
	delete(s.listed, repository)
	s.removed = append(s.removed, repository)
	return true, nil
}

func TestProjectAddStoresTheProjectAndSaysWhatItStored(t *testing.T) {
	at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	var logs bytes.Buffer
	s := &fakeProjectStore{}
	if err := runProjectAdd(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), s, "Roadmap", "Octo/Widget", at); err != nil {
		t.Fatal(err)
	}
	if s.listed["Octo/Widget"] != "Roadmap" || !s.addedAt.Equal(at) {
		t.Fatalf("listed %v at %v, want Roadmap under Octo/Widget at %v", s.listed, s.addedAt, at)
	}
	for _, want := range []string{"msg=projectAdded", "linear_project=Roadmap", "repo=Octo/Widget"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("log = %q, want it to contain %q", logs.String(), want)
		}
	}
}

func TestProjectAddReturnsTheStoresRefusal(t *testing.T) {
	boom := fmt.Errorf("%w: Octo/Widget", store.ErrRepositoryListed)
	err := runProjectAdd(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), &fakeProjectStore{addErr: boom}, "Roadmap", "Octo/Widget", time.Time{})
	if !errors.Is(err, store.ErrRepositoryListed) {
		t.Fatalf("err = %v, want the store's refusal", err)
	}
}

func TestProjectRemoveUnlistsAndSaysWhatItRemoved(t *testing.T) {
	var logs bytes.Buffer
	s := &fakeProjectStore{listed: map[string]string{"Octo/Widget": "Roadmap"}}
	if err := runProjectRemove(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), s, "Octo/Widget"); err != nil {
		t.Fatal(err)
	}
	if len(s.removed) != 1 || s.removed[0] != "Octo/Widget" {
		t.Fatalf("removed %v, want Octo/Widget", s.removed)
	}
	for _, want := range []string{"msg=projectRemoved", "repo=Octo/Widget"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("log = %q, want it to contain %q", logs.String(), want)
		}
	}
}

func TestProjectRemoveOfAnUnlistedRepositorySaysSoAndChangesNothing(t *testing.T) {
	var logs bytes.Buffer
	s := &fakeProjectStore{}
	if err := runProjectRemove(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), s, "Octo/Widget"); err != nil {
		t.Fatal(err)
	}
	if len(s.removed) != 0 {
		t.Fatalf("removed %v, want nothing", s.removed)
	}
	for _, want := range []string{"msg=projectNotListed", "repo=Octo/Widget"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("log = %q, want it to contain %q", logs.String(), want)
		}
	}
}

func TestProjectRemoveReturnsTheStoresError(t *testing.T) {
	boom := errors.New("firestore is unreachable")
	err := runProjectRemove(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), &fakeProjectStore{removeErr: boom}, "Octo/Widget")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the store's", err)
	}
}

func TestProjectsRefuseAWrongIdentityABadRepositoryAndStrayArguments(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "WINGMAN_ACCOUNT": "octo", "GITHUB_REPOSITORY_OWNER": "octo"}
	mismatched := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "WINGMAN_ACCOUNT": "work-account", "GITHUB_REPOSITORY_OWNER": "octo"}
	for name, tt := range map[string]struct {
		env  map[string]string
		args []string
		want string
	}{
		"adding under a mismatched identity":   {mismatched, []string{"project-add", "-linear-project", "Roadmap", "-repo", "octo/widget"}, runner.ErrIdentityMismatch.Error()},
		"removing under a mismatched identity": {mismatched, []string{"project-remove", "-repo", "octo/widget"}, runner.ErrIdentityMismatch.Error()},
		"a repository without an owner":        {base, []string{"project-add", "-linear-project", "Roadmap", "-repo", "widget"}, `-repo "widget" is not owner/name`},
		"a repository with two slashes":        {base, []string{"project-add", "-linear-project", "Roadmap", "-repo", "a/b/c"}, `-repo "a/b/c" is not owner/name`},
		"a missing Linear project":             {base, []string{"project-add", "-repo", "octo/widget"}, "-linear-project is required"},
		"an argument after the flags":          {base, []string{"project-add", "-linear-project", "Roadmap", "-repo", "octo/widget", "extra"}, `unexpected argument "extra"`},
		"an unknown flag after the flags":      {base, []string{"project-add", "-linear-project", "Roadmap", "-repo", "octo/widget", "-nope"}, "flag provided but not defined: -nope"},
		"removing a bad repository":            {base, []string{"project-remove", "-repo", "widget"}, `-repo "widget" is not owner/name`},
		"removing with a stray argument":       {base, []string{"project-remove", "-repo", "octo/widget", "extra"}, `unexpected argument "extra"`},
		"removing with an unknown flag":        {base, []string{"project-remove", "-repo", "octo/widget", "-nope"}, "flag provided but not defined: -nope"},
	} {
		t.Run(name, func(t *testing.T) {
			err := run(context.Background(), logger, tt.args, func(k string) string { return tt.env[k] })
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}
