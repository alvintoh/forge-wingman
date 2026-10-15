package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// fakeCDStore holds the CD flag a subcommand reads and the one it records.
type fakeCDStore struct {
	deploying bool
	readErr   error
	setErr    error
	repo      string
	set       bool
	at        time.Time
}

func (s *fakeCDStore) Deploying(_ context.Context, repo string) (bool, error) {
	s.repo = repo
	return s.deploying, s.readErr
}

func (s *fakeCDStore) Set(_ context.Context, repo string, deploying bool, at time.Time) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.repo, s.deploying, s.set, s.at = repo, deploying, true, at
	return nil
}

func TestCDPrintsTheRecordedFlag(t *testing.T) {
	var out bytes.Buffer
	store := &fakeCDStore{deploying: true}
	if err := runCD(context.Background(), discard(t), &out, store, "octo/scratch", false, false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if want := "octo/scratch deploys on merge: true\n"; out.String() != want || store.set {
		t.Fatalf("printed %q, set %v, want %q and nothing written", out.String(), store.set, want)
	}
}

func TestCDRecordsTheNamedFlag(t *testing.T) {
	var out, logs bytes.Buffer
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	store := &fakeCDStore{}
	if err := runCD(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), &out, store, "octo/scratch", true, true, at); err != nil {
		t.Fatal(err)
	}
	if store.repo != "octo/scratch" || !store.deploying || !store.at.Equal(at) || out.Len() != 0 {
		t.Fatalf("recorded %s = %v at %v, printed %q, want the flag set at %v", store.repo, store.deploying, store.at, out.String(), at)
	}
	if !strings.Contains(logs.String(), "msg=cdRecorded repo=octo/scratch deploying=true") {
		t.Fatalf("log = %q, want cdRecorded", logs.String())
	}
}

func TestCDRecordsAFlagThatIsNotDeploying(t *testing.T) {
	store := &fakeCDStore{}
	if err := runCD(context.Background(), discard(t), io.Discard, store, "octo/scratch", false, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !store.set || store.deploying {
		t.Fatalf("set = %v, deploying = %v, want the false flag recorded", store.set, store.deploying)
	}
}

func TestCDReturnsTheStoreErrors(t *testing.T) {
	boom := errors.New("firestore is unreachable")
	if err := runCD(context.Background(), discard(t), io.Discard, &fakeCDStore{readErr: boom}, "octo/scratch", false, false, time.Time{}); !errors.Is(err, boom) {
		t.Errorf("read err = %v, want the store's", err)
	}
	if err := runCD(context.Background(), discard(t), io.Discard, &fakeCDStore{setErr: boom}, "octo/scratch", true, true, time.Time{}); !errors.Is(err, boom) {
		t.Errorf("set err = %v, want the store's", err)
	}
}

func TestCDRequiresARepositoryBeforeOpeningFirestore(t *testing.T) {
	env := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "WINGMAN_ACCOUNT": "octo", "GITHUB_REPOSITORY_OWNER": "octo"}
	err := run(context.Background(), discard(t), []string{"cd"}, func(k string) string { return env[k] })
	if err == nil || !strings.Contains(err.Error(), "-repo is required") {
		t.Fatalf("err = %v, want the missing repository named", err)
	}
}
