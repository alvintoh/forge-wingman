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

// fakeAutonomyStore holds the evidence a subcommand reads and the level it sets.
type fakeAutonomyStore struct {
	ev     dispatcher.Evidence
	evErr  error
	setErr error
	level  dispatcher.Autonomy
	setAt  time.Time
	// earned is what was stored, staled whether a stale mark was written, and
	// earnedErr fails the store.
	earned    dispatcher.Earned
	staled    bool
	earnedErr error
}

func (s *fakeAutonomyStore) Evidence(context.Context) (dispatcher.Evidence, error) {
	return s.ev, s.evErr
}

func (s *fakeAutonomyStore) SetEarned(_ context.Context, e dispatcher.Earned) error {
	if s.earnedErr != nil {
		return s.earnedErr
	}
	s.earned = e
	return nil
}

func (s *fakeAutonomyStore) MarkStale(context.Context) error {
	s.staled = true
	return nil
}

func (s *fakeAutonomyStore) Level(context.Context) (dispatcher.Autonomy, error) {
	return s.ev.Level, nil
}

func (s *fakeAutonomyStore) Set(_ context.Context, level dispatcher.Autonomy, at time.Time) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.level, s.setAt = level, at
	return nil
}

// fakeHistory records the changes appended to it.
type fakeHistory struct {
	changes []dispatcher.Change
	err     error
}

func (h *fakeHistory) Append(_ context.Context, c dispatcher.Change) error {
	if h.err != nil {
		return h.err
	}
	h.changes = append(h.changes, c)
	return nil
}

// measured is n merged agent PRs of repo that each measured agentLines over
// ownerLines and auto-merged.
func measured(repo string, n int, agentLines, ownerLines int64) []dispatcher.Sample {
	out := make([]dispatcher.Sample, n)
	for i := range out {
		out[i] = dispatcher.Sample{
			Repo:       repo,
			Rewrite:    dispatcher.Rewrite{AgentLines: agentLines, OwnerLines: ownerLines},
			AutoMerged: true,
		}
	}
	return out
}

func discard(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestAutonomyPrintsTheLevelTheRateAndEachCondition(t *testing.T) {
	var out bytes.Buffer
	store := &fakeAutonomyStore{ev: dispatcher.Evidence{
		Level:    3,
		Trailing: measured("octo/scratch", 10, 100, 1),
		CD:       map[string]bool{"octo/scratch": true},
	}}
	if err := runAutonomy(context.Background(), discard(t), &out, store, &fakeHistory{}, 0, false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	printed := out.String()
	for _, want := range []string{
		"autonomy 3: the product delegates and you merge",
		"trailing 10 PRs: 1.0% rewrite rate, 10 owner lines of 1000 agent lines over 10 PRs",
		"advance to level 4: held",
		"a rewrite rate of 1.0% over 10 of 10 PRs, 5% or less: held",
		"CD recorded for octo/scratch: held",
		"advance to level 5: not held",
		"0 reverts over 10 of 20 sampled PRs, zero expected: not held",
		"levels 0 to 3: no condition to hold",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("printed %q, want it to contain %q", printed, want)
		}
	}
	if !store.setAt.IsZero() {
		t.Errorf("a printed run set the level at %v, want nothing written", store.setAt)
	}
}

func TestAutonomyPrintsTheNumbersALevelFailedOn(t *testing.T) {
	// 10 owner lines of 100 agent lines each is 10% over the trailing window.
	var out bytes.Buffer
	store := &fakeAutonomyStore{ev: dispatcher.Evidence{
		Level:    3,
		Trailing: measured("octo/scratch", 10, 100, 10),
		CD:       map[string]bool{"octo/scratch": true},
	}}
	if err := runAutonomy(context.Background(), discard(t), &out, store, &fakeHistory{}, 0, false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	printed := out.String()
	for _, want := range []string{
		"advance to level 4: not held",
		"a rewrite rate of 10.0% over 10 of 10 PRs, 5% or less: not held",
		"level 3 held (level 3 is set): held",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("printed %q, want it to contain %q", printed, want)
		}
	}
}

func TestAutonomySetsTheNamedLevelAndPrintsNothing(t *testing.T) {
	var out, logs bytes.Buffer
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	store := &fakeAutonomyStore{ev: dispatcher.Evidence{Level: 3}}
	if err := runAutonomy(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), &out, store, &fakeHistory{}, 0, true, at); err != nil {
		t.Fatal(err)
	}
	if store.level != 0 || !store.setAt.Equal(at) || out.Len() != 0 || !strings.Contains(logs.String(), "msg=autonomySet level=0") {
		t.Fatalf("stored %d at %v, printed %q, log %q, want 0 set at %v and logged", store.level, store.setAt, out.String(), logs.String(), at)
	}
}

func TestAutonomySetsALevelWhoseConditionsHold(t *testing.T) {
	var logs bytes.Buffer
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	store := &fakeAutonomyStore{ev: dispatcher.Evidence{
		Level:    3,
		Trailing: measured("octo/scratch", 10, 100, 1),
		CD:       map[string]bool{"octo/scratch": true},
	}}
	if err := runAutonomy(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), io.Discard, store, &fakeHistory{}, 4, true, at); err != nil {
		t.Fatal(err)
	}
	if store.level != 4 || !store.setAt.Equal(at) || !strings.Contains(logs.String(), "msg=autonomySet level=4") {
		t.Fatalf("stored %d at %v, log %q, want level 4 set and logged", store.level, store.setAt, logs.String())
	}
}

func TestAutonomyRefusesALevelItsConditionsDoNotHoldWithTheNumbers(t *testing.T) {
	store := &fakeAutonomyStore{ev: dispatcher.Evidence{
		Level:    3,
		Trailing: measured("octo/scratch", 10, 100, 10),
		CD:       map[string]bool{"octo/scratch": true},
	}}
	err := runAutonomy(context.Background(), discard(t), io.Discard, store, &fakeHistory{}, 4, true, time.Now())
	if err == nil || !strings.Contains(err.Error(), "a rewrite rate of 10.0% over 10 of 10 PRs, 5% or less") {
		t.Fatalf("err = %v, want the numbers the condition failed on", err)
	}
	if !store.setAt.IsZero() {
		t.Fatalf("a refused level was written at %v", store.setAt)
	}
}

func TestAutonomyReturnsTheStoreErrors(t *testing.T) {
	boom := errors.New("firestore is unreachable")
	logger := discard(t)
	if err := runAutonomy(context.Background(), logger, io.Discard, &fakeAutonomyStore{evErr: boom}, &fakeHistory{}, 0, false, time.Time{}); !errors.Is(err, boom) {
		t.Errorf("read err = %v, want the store's", err)
	}
	if err := runAutonomy(context.Background(), logger, io.Discard, &fakeAutonomyStore{evErr: boom}, &fakeHistory{}, 4, true, time.Time{}); !errors.Is(err, boom) {
		t.Errorf("evidence err = %v, want the store's", err)
	}
	if err := runAutonomy(context.Background(), logger, io.Discard, &fakeAutonomyStore{setErr: boom}, &fakeHistory{}, 1, true, time.Time{}); !errors.Is(err, boom) {
		t.Errorf("set err = %v, want the store's", err)
	}
}

func TestAutonomyRefusesALevelThatNamesNoneBeforeOpeningFirestore(t *testing.T) {
	logger := discard(t)
	base := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "WINGMAN_ACCOUNT": "octo", "GITHUB_REPOSITORY_OWNER": "octo"}
	mismatched := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "WINGMAN_ACCOUNT": "work-account", "GITHUB_REPOSITORY_OWNER": "octo"}
	for name, tt := range map[string]struct {
		env  map[string]string
		args []string
		want string
	}{
		"a mismatched identity": {mismatched, []string{"autonomy", "-level", "1"}, runner.ErrIdentityMismatch.Error()},
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

func TestAutonomySetAppendsTheChangeToTheHistory(t *testing.T) {
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	store := &fakeAutonomyStore{ev: dispatcher.Evidence{Level: 3, Trailing: measured("octo/scratch", 10, 100, 1)}}
	hist := &fakeHistory{}
	if err := runAutonomy(context.Background(), discard(t), io.Discard, store, hist, 2, true, at); err != nil {
		t.Fatal(err)
	}
	c := hist.changes
	if len(c) != 1 || c[0].From != 3 || c[0].To != 2 || c[0].Cause != dispatcher.ChangeOwnerSet || !c[0].At.Equal(at) {
		t.Fatalf("history = %+v, want one owner-set change from 3 to 2 at %v", c, at)
	}
	if len(c[0].Numbers.PRs) != 10 || c[0].Numbers.AgentLines != 1000 || c[0].Numbers.OwnerLines != 10 {
		t.Fatalf("numbers = %+v, want the evidence at set time", c[0].Numbers)
	}
}

func TestAutonomyRefusedLevelLeavesNoHistory(t *testing.T) {
	store := &fakeAutonomyStore{ev: dispatcher.Evidence{Level: 3}}
	hist := &fakeHistory{}
	if err := runAutonomy(context.Background(), discard(t), io.Discard, store, hist, 4, true, time.Now()); err == nil {
		t.Fatal("want level 4 refused")
	}
	if len(hist.changes) != 0 {
		t.Fatalf("history = %+v, want nothing for a refused level", hist.changes)
	}
}

func TestAutonomySetReportsAHistoryThatCannotBeWritten(t *testing.T) {
	store := &fakeAutonomyStore{ev: dispatcher.Evidence{Level: 3}}
	boom := errors.New("firestore is unreachable")
	err := runAutonomy(context.Background(), discard(t), io.Discard, store, &fakeHistory{err: boom}, 2, true, time.Now())
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the history's", err)
	}
}

func TestAutonomyRecomputesAndStoresTheEarnedLevel(t *testing.T) {
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	for name, set := range map[string]bool{"printing": false, "setting": true} {
		t.Run(name, func(t *testing.T) {
			store := &fakeAutonomyStore{ev: dispatcher.Evidence{
				Level: 3, Trailing: measured("octo/scratch", 10, 100, 1), CD: map[string]bool{"octo/scratch": true},
			}}
			if err := runAutonomy(context.Background(), discard(t), io.Discard, store, &fakeHistory{}, 3, set, at); err != nil {
				t.Fatal(err)
			}
			if store.earned.Level != 4 || !store.earned.ComputedAt.Equal(at) || store.earned.Numbers.OwnerLines != 10 {
				t.Fatalf("earned = %+v, want level 4 computed at %v with its numbers", store.earned, at)
			}
		})
	}
}

func TestAutonomyMarksTheSnapshotStaleWhenItCannotStoreIt(t *testing.T) {
	store := &fakeAutonomyStore{earnedErr: errors.New("firestore is unreachable")}
	err := runAutonomy(context.Background(), discard(t), io.Discard, store, &fakeHistory{}, 4, true, time.Now())
	if err == nil || !store.staled || store.level != 0 {
		t.Fatalf("err = %v, staled = %v, level = %d, want a failure, a stale mark and no level set", err, store.staled, store.level)
	}
}

func TestAutonomySetsALevelWithNoConditionWhenTheEvidenceCannotBeRead(t *testing.T) {
	boom := errors.New("firestore is unreachable")
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	for name, mk := range map[string]func() *fakeAutonomyStore{
		"evidence": func() *fakeAutonomyStore { return &fakeAutonomyStore{ev: dispatcher.Evidence{Level: 3}, evErr: boom} },
		"earned": func() *fakeAutonomyStore {
			return &fakeAutonomyStore{ev: dispatcher.Evidence{Level: 3}, earnedErr: boom}
		},
	} {
		t.Run(name, func(t *testing.T) {
			for level := dispatcher.Autonomy(0); level <= 3; level++ {
				store, hist := mk(), &fakeHistory{}
				if err := runAutonomy(context.Background(), discard(t), io.Discard, store, hist, level, true, at); err != nil {
					t.Fatalf("set %d: %v, want the owner's override to need no measurement", level, err)
				}
				if store.level != level || len(hist.changes) != 1 || hist.changes[0].To != level {
					t.Fatalf("level = %d, history = %+v, want %d set and recorded", store.level, hist.changes, level)
				}
				if name == "evidence" && !hist.changes[0].Numbers.Unavailable {
					t.Fatalf("numbers = %+v, want the history to say the evidence was unavailable", hist.changes[0].Numbers)
				}
			}
			for _, level := range []dispatcher.Autonomy{4, 5} {
				store, hist := mk(), &fakeHistory{}
				if err := runAutonomy(context.Background(), discard(t), io.Discard, store, hist, level, true, at); err == nil {
					t.Fatalf("set %d held, want a level with a condition refused without evidence", level)
				}
				if store.level != 0 || len(hist.changes) != 0 {
					t.Fatalf("level = %d, history = %+v, want nothing written for a refused level", store.level, hist.changes)
				}
			}
		})
	}
}
