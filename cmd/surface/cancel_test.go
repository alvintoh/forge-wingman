package main

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// stubCancels holds what a cancel reads of each run and records the cancels it
// is told of.
type stubCancels struct {
	runs      map[string]store.CancelTarget
	readErr   error
	recordErr error
	cancels   []string
}

func (s *stubCancels) CancelTarget(_ context.Context, id string) (store.CancelTarget, error) {
	if s.readErr != nil {
		return store.CancelTarget{}, s.readErr
	}
	target, ok := s.runs[id]
	if !ok {
		return store.CancelTarget{}, runner.ErrRecordNotFound
	}
	return target, nil
}

func (s *stubCancels) RecordCancel(_ context.Context, id, by string, _ time.Time) error {
	if s.recordErr != nil {
		return s.recordErr
	}
	s.cancels = append(s.cancels, id+"@"+by)
	return nil
}

// waitingTarget is what a cancel reads of a run whose PR waits on the owner.
func waitingTarget() store.CancelTarget {
	return store.CancelTarget{Outcome: runner.OutcomePROpened}
}

// postCancel sends a cancel for run as email, signed out when email is empty.
func postCancel(t *testing.T, d Deps, run, contentType, body, email string) *httptest.ResponseRecorder {
	t.Helper()
	d.SPA, d.Logger = fstest.MapFS{}, cmp.Or(d.Logger, discardLogger())
	req := httptest.NewRequest(http.MethodPost, "/api/runs/"+run+"/cancel", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if email != "" {
		req = withEmail(req, email)
	}
	rec := httptest.NewRecorder()
	newMux(d).ServeHTTP(rec, req)
	return rec
}

const cancelBody = `{}`

func TestCancelRecordsTheRunAndTheSignedInOwner(t *testing.T) {
	cancels := &stubCancels{runs: map[string]store.CancelTarget{"r1": waitingTarget()}}
	rec := postCancel(t, Deps{Cancels: cancels}, "r1", "application/json", cancelBody, "owner@example.com")
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"status\":\"cancelled\"}\n" {
		t.Fatalf("answer = %d %s, want the cancel recorded", rec.Code, rec.Body.String())
	}
	if want := []string{"r1@owner@example.com"}; !slices.Equal(cancels.cancels, want) {
		t.Fatalf("cancels = %v, want %v", cancels.cancels, want)
	}
}

func TestCancelRecordsTheAssertionsOwnerNotTheBodys(t *testing.T) {
	cancels := &stubCancels{runs: map[string]store.CancelTarget{"r1": waitingTarget()}}
	rec := postCancel(t, Deps{Cancels: cancels}, "r1", "application/json",
		`{"by":"someone@evil.example"}`, "owner@example.com")
	if rec.Code != http.StatusOK {
		t.Fatalf("answer = %d %s", rec.Code, rec.Body.String())
	}
	if want := []string{"r1@owner@example.com"}; !slices.Equal(cancels.cancels, want) {
		t.Fatalf("cancels = %v, want the assertion's address and not the body's", cancels.cancels)
	}
}

func TestCancelWithoutASignedInOwnerIsRefusedAndWritesNothing(t *testing.T) {
	cancels := &stubCancels{runs: map[string]store.CancelTarget{"r1": waitingTarget()}}
	rec := postCancel(t, Deps{Cancels: cancels}, "r1", "application/json",
		`{"by":"owner@example.com"}`, "")
	if rec.Code != http.StatusUnauthorized || len(cancels.cancels) != 0 {
		t.Fatalf("answer = %d %s, cancels %v, want a 401 and no cancel", rec.Code, rec.Body.String(), cancels.cancels)
	}
}

func TestCancelWithoutAJSONBodyIsRefused(t *testing.T) {
	cancels := &stubCancels{runs: map[string]store.CancelTarget{"r1": waitingTarget()}}
	rec := postCancel(t, Deps{Cancels: cancels}, "r1", "application/x-www-form-urlencoded", "", "owner@example.com")
	if rec.Code != http.StatusUnsupportedMediaType || len(cancels.cancels) != 0 {
		t.Fatalf("answer = %d %s, cancels %v, want a 415 and no cancel", rec.Code, rec.Body.String(), cancels.cancels)
	}
}

// TestCancelOfARunThatStoppedWaitingChangesNothing pins what a cancel answers a
// run that no longer waits. A merge wins over a cancel, because the run history
// draws the run as merged either way.
func TestCancelOfARunThatStoppedWaitingChangesNothing(t *testing.T) {
	for name, tt := range map[string]struct {
		edit func(*store.CancelTarget)
		want string
	}{
		"already cancelled":     {func(r *store.CancelTarget) { r.CancelledByOwnerAt = measuredAt() }, "already-cancelled"},
		"already merged":        {func(r *store.CancelTarget) { r.Merged = true }, "already-merged"},
		"merged after a cancel": {func(r *store.CancelTarget) { r.CancelledByOwnerAt, r.Merged = measuredAt(), true }, "already-merged"},
	} {
		t.Run(name, func(t *testing.T) {
			target := waitingTarget()
			tt.edit(&target)
			cancels := &stubCancels{runs: map[string]store.CancelTarget{"r1": target}}
			answer := postCancel(t, Deps{Cancels: cancels}, "r1", "application/json", cancelBody, "owner@example.com")
			if answer.Code != http.StatusOK || answer.Body.String() != "{\"status\":\""+tt.want+"\"}\n" {
				t.Fatalf("answer = %d %s, want %s", answer.Code, answer.Body.String(), tt.want)
			}
			if len(cancels.cancels) != 0 {
				t.Fatalf("cancels = %v, want none recorded", cancels.cancels)
			}
		})
	}
}

func TestCancelOfARunThatIsNotWaitingIsRefused(t *testing.T) {
	target := waitingTarget()
	target.Outcome = runner.OutcomeBuilt
	cancels := &stubCancels{runs: map[string]store.CancelTarget{"r1": target}}
	answer := postCancel(t, Deps{Cancels: cancels}, "r1", "application/json", cancelBody, "owner@example.com")
	if answer.Code != http.StatusConflict || answer.Body.String() != "{\"reason\":\"not-waiting\"}\n" {
		t.Fatalf("answer = %d %s, want a not-waiting refusal", answer.Code, answer.Body.String())
	}
	if len(cancels.cancels) != 0 {
		t.Fatalf("cancels = %v, want none recorded", cancels.cancels)
	}
}

func TestCancelOfAnUnknownRunIsNotFound(t *testing.T) {
	rec := postCancel(t, Deps{Cancels: &stubCancels{}}, "absent", "application/json", cancelBody, "owner@example.com")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("answer = %d %s, want a 404", rec.Code, rec.Body.String())
	}
}

func TestCancelWithoutAStoreIsUnavailable(t *testing.T) {
	rec := postCancel(t, Deps{}, "r1", "application/json", cancelBody, "owner@example.com")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("answer = %d %s, want a 503", rec.Code, rec.Body.String())
	}
}

func TestCancelReportsAStoreItCannotReadOrWrite(t *testing.T) {
	for name, tt := range map[string]struct {
		cancels *stubCancels
		line    string
	}{
		"a record it cannot read":  {&stubCancels{readErr: errors.New("firestore unreachable")}, "ownerCancelRunNotRead"},
		"a cancel it cannot write": {&stubCancels{runs: map[string]store.CancelTarget{"r1": waitingTarget()}, recordErr: errors.New("firestore unreachable")}, "ownerCancelNotRecorded"},
	} {
		t.Run(name, func(t *testing.T) {
			var log strings.Builder
			rec := postCancel(t, Deps{Cancels: tt.cancels, Logger: slog.New(slog.NewTextHandler(&log, nil))},
				"r1", "application/json", cancelBody, "owner@example.com")
			if rec.Code != http.StatusInternalServerError || !strings.Contains(log.String(), tt.line) {
				t.Fatalf("answer = %d %s, log = %q, want a 500 naming %q", rec.Code, rec.Body.String(), log.String(), tt.line)
			}
		})
	}
}

// TestCancelStatusesAreTheStringsThePageSwitchesOn pins the literals the row's
// client switches on, which no compiler checks against the endpoint.
func TestCancelStatusesAreTheStringsThePageSwitchesOn(t *testing.T) {
	for got, want := range map[string]string{
		statusCancelled:        "cancelled",
		statusAlreadyCancelled: "already-cancelled",
		statusAlreadyMerged:    "already-merged",
		refusedNotWaiting:      "not-waiting",
	} {
		if got != want {
			t.Errorf("status %q, want %q", got, want)
		}
	}
}
