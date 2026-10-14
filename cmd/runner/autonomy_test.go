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

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// fakeAutonomyStore holds the level a subcommand reads and the one it sets.
type fakeAutonomyStore struct {
	level   dispatcher.Autonomy
	setAt   time.Time
	readErr error
	setErr  error
}

func (s *fakeAutonomyStore) Level(context.Context) (dispatcher.Autonomy, error) {
	return s.level, s.readErr
}

func (s *fakeAutonomyStore) Set(_ context.Context, level dispatcher.Autonomy, at time.Time) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.level, s.setAt = level, at
	return nil
}

func TestAutonomyPrintsTheLevelAndWhatItAllows(t *testing.T) {
	var out bytes.Buffer
	store := &fakeAutonomyStore{level: 2}
	if err := runAutonomy(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), &out, store, 0, false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if want := "autonomy 2: the product delegates up to 3 runs a day and you merge\n"; out.String() != want || !store.setAt.IsZero() {
		t.Fatalf("printed %q, set at %v, want %q and nothing written", out.String(), store.setAt, want)
	}
}

func TestAutonomySetsTheNamedLevelAndPrintsNothing(t *testing.T) {
	var out, logs bytes.Buffer
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	store := &fakeAutonomyStore{level: 3}
	if err := runAutonomy(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), &out, store, 0, true, at); err != nil {
		t.Fatal(err)
	}
	if store.level != 0 || !store.setAt.Equal(at) || out.Len() != 0 || !strings.Contains(logs.String(), "msg=autonomySet level=0") {
		t.Fatalf("stored %d at %v, printed %q, log %q, want 0 set at %v and logged", store.level, store.setAt, out.String(), logs.String(), at)
	}
}

func TestAutonomyReturnsTheStoreErrors(t *testing.T) {
	boom := errors.New("firestore is unreachable")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := runAutonomy(context.Background(), logger, io.Discard, &fakeAutonomyStore{readErr: boom}, 0, false, time.Time{}); !errors.Is(err, boom) {
		t.Errorf("read err = %v, want the store's", err)
	}
	if err := runAutonomy(context.Background(), logger, io.Discard, &fakeAutonomyStore{setErr: boom}, 1, true, time.Time{}); !errors.Is(err, boom) {
		t.Errorf("set err = %v, want the store's", err)
	}
}

func TestAutonomyRefusesBeforeOpeningFirestore(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "WINGMAN_ACCOUNT": "octo", "GITHUB_REPOSITORY_OWNER": "octo"}
	mismatched := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "WINGMAN_ACCOUNT": "work-account", "GITHUB_REPOSITORY_OWNER": "octo"}
	for name, tt := range map[string]struct {
		env  map[string]string
		args []string
		want string
	}{
		"a mismatched identity": {mismatched, []string{"autonomy", "-level", "1"}, runner.ErrIdentityMismatch.Error()},
		"level 4":               {base, []string{"autonomy", "-level", "4"}, "level 4 needs level 3 held"},
		"level 5":               {base, []string{"autonomy", "-level", "5"}, "level 5 needs level 4 held"},
		"a level above 5":       {base, []string{"autonomy", "-level", "6"}, dispatcher.ErrUnknownAutonomy.Error()},
		"a negative level":      {base, []string{"autonomy", "-level", "-1"}, dispatcher.ErrUnknownAutonomy.Error()},
	} {
		t.Run(name, func(t *testing.T) {
			err := run(context.Background(), logger, tt.args, func(k string) string { return tt.env[k] })
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}
