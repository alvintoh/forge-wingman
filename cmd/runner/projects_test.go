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

// fakePhaseStore holds the pause each project has, as the pause subcommands
// leave it.
type fakePhaseStore struct {
	paused map[string]bool
	setAt  time.Time
	calls  int
	setErr error
}

func (s *fakePhaseStore) Set(_ context.Context, repository string, paused bool, at time.Time) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.calls++
	if s.paused == nil {
		s.paused = map[string]bool{}
	}
	s.paused[repository] = paused
	s.setAt = at
	return nil
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

func TestProjectStopPausesTheProjectAndSaysSo(t *testing.T) {
	at := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	var logs bytes.Buffer
	s := &fakePhaseStore{}
	if err := runProjectPause(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), s, "Octo/Widget", true, at); err != nil {
		t.Fatal(err)
	}
	if paused, ok := s.paused["Octo/Widget"]; !ok || !paused || !s.setAt.Equal(at) {
		t.Fatalf("paused %v at %v, want Octo/Widget paused at %v", s.paused, s.setAt, at)
	}
	for _, want := range []string{"msg=projectPauseSet", "repo=Octo/Widget", "paused=true"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("log = %q, want it to contain %q", logs.String(), want)
		}
	}
}

func TestProjectResumeClearsThePauseAndSaysSo(t *testing.T) {
	var logs bytes.Buffer
	s := &fakePhaseStore{paused: map[string]bool{"Octo/Widget": true}}
	if err := runProjectPause(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), s, "Octo/Widget", false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if s.paused["Octo/Widget"] {
		t.Fatalf("paused %v, want Octo/Widget running", s.paused)
	}
	for _, want := range []string{"msg=projectPauseSet", "repo=Octo/Widget", "paused=false"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("log = %q, want it to contain %q", logs.String(), want)
		}
	}
}

// TestProjectPauseOfAnUnlistedRepositoryIsRefused asserts a repository no
// project lists is refused and the store writes nothing.
func TestProjectPauseOfAnUnlistedRepositoryIsRefused(t *testing.T) {
	s := &fakePhaseStore{setErr: fmt.Errorf("%w: Octo/Widget", store.ErrRepositoryNotListed)}
	err := runProjectPause(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), s, "Octo/Widget", true, time.Time{})
	if !errors.Is(err, store.ErrRepositoryNotListed) {
		t.Fatalf("err = %v, want the store's refusal", err)
	}
	if s.calls != 0 || len(s.paused) != 0 {
		t.Fatalf("wrote %v over %d calls, want nothing", s.paused, s.calls)
	}
}

// TestStoppingAStoppedProjectAndResumingARunningOneChangeNothing asserts the
// write sets a value and does not toggle it, so repeating either command is a
// success that leaves the pause alone.
func TestStoppingAStoppedProjectAndResumingARunningOneChangeNothing(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &fakePhaseStore{}
	for range 2 {
		if err := runProjectPause(context.Background(), logger, s, "Octo/Widget", true, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	if !s.paused["Octo/Widget"] {
		t.Fatalf("paused %v, want Octo/Widget stopped after stopping it twice", s.paused)
	}
	for range 2 {
		if err := runProjectPause(context.Background(), logger, s, "Octo/Widget", false, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	if s.paused["Octo/Widget"] {
		t.Fatalf("paused %v, want Octo/Widget running after resuming it twice", s.paused)
	}
}

// TestProjectPauseReportsAPauseStoreItCannotOpen asserts the command reports a
// store it could not open instead of carrying on with none.
func TestProjectPauseReportsAPauseStoreItCannotOpen(t *testing.T) {
	boom := errors.New("no credentials")
	saved := openPhases
	openPhases = func(context.Context, string) (phaseStore, func() error, error) { return nil, nil, boom }
	t.Cleanup(func() { openPhases = saved })
	e := env{project: "p", identity: runner.Identity{Account: "octo", Owner: "octo"}}
	err := projectStop(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), e, []string{"-repo", "Octo/Widget"})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the open's error", err)
	}
}

// TestUsageListsTheProjectPauseCommands asserts the usage the binary prints
// with no argument names both pause commands, so a terminal user finds them.
func TestUsageListsTheProjectPauseCommands(t *testing.T) {
	err := run(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil, func(string) string { return "" })
	if err == nil {
		t.Fatal("no arguments should print the usage")
	}
	for _, want := range []string{"project-stop", "project-resume"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("usage = %q, want it to name %q", err.Error(), want)
		}
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
		"stopping under a mismatched identity": {mismatched, []string{"project-stop", "-repo", "octo/widget"}, runner.ErrIdentityMismatch.Error()},
		"resuming under a mismatched identity": {mismatched, []string{"project-resume", "-repo", "octo/widget"}, runner.ErrIdentityMismatch.Error()},
		"stopping a bad repository":            {base, []string{"project-stop", "-repo", "widget"}, `-repo "widget" is not owner/name`},
		"resuming with a stray argument":       {base, []string{"project-resume", "-repo", "octo/widget", "extra"}, `unexpected argument "extra"`},
		"resuming with an unknown flag":        {base, []string{"project-resume", "-repo", "octo/widget", "-nope"}, "flag provided but not defined: -nope"},
	} {
		t.Run(name, func(t *testing.T) {
			err := run(context.Background(), logger, tt.args, func(k string) string { return tt.env[k] })
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}
