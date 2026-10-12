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

// fakePeakModeStore holds the mode a subcommand reads and the one it sets.
type fakePeakModeStore struct {
	mode    dispatcher.PeakMode
	setAt   time.Time
	readErr error
	setErr  error
}

func (s *fakePeakModeStore) Mode(context.Context) (dispatcher.PeakMode, error) {
	return s.mode, s.readErr
}

func (s *fakePeakModeStore) Set(_ context.Context, mode dispatcher.PeakMode, at time.Time) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.mode, s.setAt = mode, at
	return nil
}

func TestPeakModePrintsTheCurrentModeWhenNoneIsNamed(t *testing.T) {
	var out bytes.Buffer
	store := &fakePeakModeStore{mode: dispatcher.PeakAlways}
	if err := runPeakMode(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), &out, store, "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "always\n" || !store.setAt.IsZero() {
		t.Fatalf("printed %q, set at %v, want always printed and nothing written", out.String(), store.setAt)
	}
}

func TestPeakModeSetsTheNamedModeAndPrintsNothing(t *testing.T) {
	var out, logs bytes.Buffer
	at := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	store := &fakePeakModeStore{mode: dispatcher.PeakOffPeakOnly}
	if err := runPeakMode(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), &out, store, dispatcher.PeakAlways, at); err != nil {
		t.Fatal(err)
	}
	if store.mode != "always" || !store.setAt.Equal(at) || out.Len() != 0 || !strings.Contains(logs.String(), "msg=peakModeSet mode=always") {
		t.Fatalf("stored %q at %v, printed %q, log %q, want always set at %v and logged", store.mode, store.setAt, out.String(), logs.String(), at)
	}
}

func TestPeakModeReturnsTheStoreErrors(t *testing.T) {
	boom := errors.New("firestore is unreachable")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := runPeakMode(context.Background(), logger, io.Discard, &fakePeakModeStore{readErr: boom}, "", time.Time{}); !errors.Is(err, boom) {
		t.Errorf("read err = %v, want the store's", err)
	}
	if err := runPeakMode(context.Background(), logger, io.Discard, &fakePeakModeStore{setErr: boom}, dispatcher.PeakAlways, time.Time{}); !errors.Is(err, boom) {
		t.Errorf("set err = %v, want the store's", err)
	}
}

func TestPeakModeRefusesBeforeOpeningFirestore(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "WINGMAN_ACCOUNT": "octo", "GITHUB_REPOSITORY_OWNER": "octo"}
	mismatched := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "WINGMAN_ACCOUNT": "work-account", "GITHUB_REPOSITORY_OWNER": "octo"}
	for name, tt := range map[string]struct {
		env  map[string]string
		args []string
		want string
	}{
		"a mismatched identity":    {mismatched, []string{"peak-mode", "-mode", "always"}, runner.ErrIdentityMismatch.Error()},
		"an unknown mode":          {base, []string{"peak-mode", "-mode", "sometimes"}, `-mode "sometimes" is not off-peak-only or always`},
		"a mode in the wrong case": {base, []string{"peak-mode", "-mode", "Always"}, `-mode "Always" is not`},
	} {
		t.Run(name, func(t *testing.T) {
			err := run(context.Background(), logger, tt.args, func(k string) string { return tt.env[k] })
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}
