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

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// fakeAutoMergeSwitchStore holds the switch a subcommand reads and the one it sets.
type fakeAutoMergeSwitchStore struct {
	enabled bool
	setAt   time.Time
	readErr error
	setErr  error
}

func (s *fakeAutoMergeSwitchStore) Enabled(context.Context) (bool, error) {
	return s.enabled, s.readErr
}

func (s *fakeAutoMergeSwitchStore) Set(_ context.Context, enabled bool, at time.Time) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.enabled, s.setAt = enabled, at
	return nil
}

func TestAutoMergeSwitchPrintsTheCurrentStateWhenNoModeIsNamed(t *testing.T) {
	for enabled, want := range map[bool]string{true: "on\n", false: "off\n"} {
		var out bytes.Buffer
		store := &fakeAutoMergeSwitchStore{enabled: enabled}
		if err := runAutoMergeSwitch(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), &out, store, "", time.Time{}); err != nil {
			t.Fatal(err)
		}
		if out.String() != want || !store.setAt.IsZero() {
			t.Fatalf("printed %q, set at %v, want %q printed and nothing written", out.String(), store.setAt, want)
		}
	}
}

func TestAutoMergeSwitchSetsTheNamedModeAndPrintsNothing(t *testing.T) {
	at := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	for mode, want := range map[string]bool{"off": false, "on": true} {
		var out, logs bytes.Buffer
		store := &fakeAutoMergeSwitchStore{enabled: !want}
		if err := runAutoMergeSwitch(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), &out, store, mode, at); err != nil {
			t.Fatal(err)
		}
		if store.enabled != want || !store.setAt.Equal(at) || out.Len() != 0 || !strings.Contains(logs.String(), "msg=autoMergeSwitchSet mode="+mode) {
			t.Fatalf("mode %q: stored %v at %v, printed %q, log %q, want %v set at %v and logged", mode, store.enabled, store.setAt, out.String(), logs.String(), want, at)
		}
	}
}

func TestAutoMergeSwitchReturnsTheStoreErrors(t *testing.T) {
	boom := errors.New("firestore is unreachable")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := runAutoMergeSwitch(context.Background(), logger, io.Discard, &fakeAutoMergeSwitchStore{readErr: boom}, "", time.Time{}); !errors.Is(err, boom) {
		t.Errorf("read err = %v, want the store's", err)
	}
	if err := runAutoMergeSwitch(context.Background(), logger, io.Discard, &fakeAutoMergeSwitchStore{setErr: boom}, "off", time.Time{}); !errors.Is(err, boom) {
		t.Errorf("set err = %v, want the store's", err)
	}
}

func TestAutoMergeSwitchRefusesAWrongIdentityABadModeAndStrayArguments(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "WINGMAN_ACCOUNT": "octo", "GITHUB_REPOSITORY_OWNER": "octo"}
	mismatched := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "WINGMAN_ACCOUNT": "work-account", "GITHUB_REPOSITORY_OWNER": "octo"}
	for name, tt := range map[string]struct {
		env  map[string]string
		args []string
		want string
	}{
		"a mismatched identity":    {mismatched, []string{"auto-merge", "-mode", "off"}, runner.ErrIdentityMismatch.Error()},
		"an unknown mode":          {base, []string{"auto-merge", "-mode", "maybe"}, `-mode "maybe" is not on or off`},
		"a mode in the wrong case": {base, []string{"auto-merge", "-mode", "Off"}, `-mode "Off" is not`},
		"a bare word, not a flag":  {base, []string{"auto-merge", "off"}, `unexpected argument "off"`},
		"an argument after a mode": {base, []string{"auto-merge", "-mode", "off", "extra"}, `unexpected argument "extra"`},
		"an empty mode":            {base, []string{"auto-merge", "-mode="}, `-mode "" is not`},
	} {
		t.Run(name, func(t *testing.T) {
			err := run(context.Background(), logger, tt.args, func(k string) string { return tt.env[k] })
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}
