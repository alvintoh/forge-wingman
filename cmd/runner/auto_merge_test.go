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
	"github.com/alvintoh/forge-wingman/internal/store"
)

// fakeAutoMergeSwitchStore holds the settings a subcommand reads and sets.
type fakeAutoMergeSwitchStore struct {
	setting store.AutoMergeSetting
	setAt   time.Time
	readErr error
	setErr  error
}

func (s *fakeAutoMergeSwitchStore) Read(context.Context) (store.AutoMergeSetting, error) {
	if s.readErr != nil {
		return store.AutoMergeSetting{}, s.readErr
	}
	return s.setting, nil
}

func (s *fakeAutoMergeSwitchStore) SetEnabled(_ context.Context, enabled bool, at time.Time) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.setting.Enabled, s.setAt = enabled, at
	if enabled {
		s.setting.Escaped = dispatcher.MergeEscape{}
	}
	return nil
}

func (s *fakeAutoMergeSwitchStore) SetBar(_ context.Context, percent int, at time.Time) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.setting.BarPercent, s.setAt = percent, at
	return nil
}

func TestAutoMergeSwitchPrintsTheCurrentSettingsWhenNothingIsNamed(t *testing.T) {
	for name, tt := range map[string]struct {
		setting store.AutoMergeSetting
		want    string
	}{
		"on at the default bar": {store.AutoMergeSetting{Enabled: true, BarPercent: 20}, "on, bar 20%\n"},
		"off at a set bar":      {store.AutoMergeSetting{Enabled: false, BarPercent: 35}, "off, bar 35%\n"},
		"an escaped PR": {
			store.AutoMergeSetting{Enabled: true, BarPercent: 20,
				Escaped: dispatcher.MergeEscape{Repo: "octo/scratch", PR: 341}},
			"on, bar 20%, escaped octo/scratch#341 until switched on again\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			s := &fakeAutoMergeSwitchStore{setting: tt.setting}
			if err := runAutoMergeSwitch(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), &out, s, "", false, 0, false, time.Time{}); err != nil {
				t.Fatal(err)
			}
			if out.String() != tt.want || !s.setAt.IsZero() {
				t.Fatalf("printed %q, set at %v, want %q printed and nothing written", out.String(), s.setAt, tt.want)
			}
		})
	}
}

func TestAutoMergeSwitchSetsTheNamedModeAndBarAndPrintsNothing(t *testing.T) {
	at := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	for name, tt := range map[string]struct {
		mode      string
		modeGiven bool
		bar       int
		barGiven  bool
		want      store.AutoMergeSetting
		logs      []string
	}{
		"off":       {"off", true, 0, false, store.AutoMergeSetting{Enabled: false, BarPercent: 20}, []string{"msg=autoMergeSwitchSet mode=off"}},
		"on":        {"on", true, 0, false, store.AutoMergeSetting{Enabled: true, BarPercent: 20}, []string{"msg=autoMergeSwitchSet mode=on"}},
		"a 35% bar": {"", false, 35, true, store.AutoMergeSetting{Enabled: true, BarPercent: 35}, []string{"msg=autoMergeBarSet bar=35%"}},
		"both":      {"off", true, 40, true, store.AutoMergeSetting{Enabled: false, BarPercent: 40}, []string{"msg=autoMergeSwitchSet mode=off", "msg=autoMergeBarSet bar=40%"}},
	} {
		t.Run(name, func(t *testing.T) {
			var out, logs bytes.Buffer
			// A mode named starts the stub on the other switch, so the write is
			// what moves it; a bar alone leaves the switch as it was.
			enabled := tt.want.Enabled
			if tt.modeGiven {
				enabled = !enabled
			}
			s := &fakeAutoMergeSwitchStore{setting: store.AutoMergeSetting{Enabled: enabled, BarPercent: 20}}
			if err := runAutoMergeSwitch(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), &out, s, tt.mode, tt.modeGiven, tt.bar, tt.barGiven, at); err != nil {
				t.Fatal(err)
			}
			if s.setting != tt.want || !s.setAt.Equal(at) || out.Len() != 0 {
				t.Fatalf("stored %+v at %v, printed %q, want %+v set at %v and nothing printed", s.setting, s.setAt, out.String(), tt.want, at)
			}
			for _, want := range tt.logs {
				if !strings.Contains(logs.String(), want) {
					t.Fatalf("log %q, want it to contain %q", logs.String(), want)
				}
			}
		})
	}
}

func TestAutoMergeSwitchReturnsTheStoreErrors(t *testing.T) {
	boom := errors.New("firestore is unreachable")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := runAutoMergeSwitch(context.Background(), logger, io.Discard, &fakeAutoMergeSwitchStore{readErr: boom}, "", false, 0, false, time.Time{}); !errors.Is(err, boom) {
		t.Errorf("read err = %v, want the store's", err)
	}
	if err := runAutoMergeSwitch(context.Background(), logger, io.Discard, &fakeAutoMergeSwitchStore{setErr: boom}, "off", true, 0, false, time.Time{}); !errors.Is(err, boom) {
		t.Errorf("set err = %v, want the store's", err)
	}
	if err := runAutoMergeSwitch(context.Background(), logger, io.Discard, &fakeAutoMergeSwitchStore{setErr: boom}, "", false, 30, true, time.Time{}); !errors.Is(err, boom) {
		t.Errorf("bar err = %v, want the store's", err)
	}
}

func TestAutoMergeSwitchRefusesAWrongIdentityABadModeABadBarAndStrayArguments(t *testing.T) {
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
		"a bar of zero":            {base, []string{"auto-merge", "-bar", "0"}, `-bar 0 is not a whole number from 1 to 100`},
		"a bar above 100":          {base, []string{"auto-merge", "-bar", "101"}, `-bar 101 is not a whole number from 1 to 100`},
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
