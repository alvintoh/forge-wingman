package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
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

// stubCancels answers a cancel as the store does: each known run is cancelled
// unless the error it holds refuses it, and an unknown run is absent.
type stubCancels struct {
	runs    map[string]error
	cancels []string
}

func (s *stubCancels) RecordCancel(_ context.Context, id, by string, _ time.Time) error {
	err, ok := s.runs[id]
	if !ok {
		return runner.ErrRecordNotFound
	}
	if err != nil {
		return err
	}
	s.cancels = append(s.cancels, id+"@"+by)
	return nil
}

// waiting is a run whose PR waits on the owner, which a cancel records.
func waiting() map[string]error {
	return map[string]error{"r1": nil}
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
	cancels := &stubCancels{runs: waiting()}
	rec := postCancel(t, Deps{Cancels: cancels}, "r1", "application/json", cancelBody, "owner@example.com")
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"status\":\"cancelled\"}\n" {
		t.Fatalf("answer = %d %s, want the cancel recorded", rec.Code, rec.Body.String())
	}
	if want := []string{"r1@owner@example.com"}; !slices.Equal(cancels.cancels, want) {
		t.Fatalf("cancels = %v, want %v", cancels.cancels, want)
	}
}

func TestCancelRecordsTheAssertionsOwnerNotTheBodys(t *testing.T) {
	cancels := &stubCancels{runs: waiting()}
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
	cancels := &stubCancels{runs: waiting()}
	rec := postCancel(t, Deps{Cancels: cancels}, "r1", "application/json",
		`{"by":"owner@example.com"}`, "")
	if rec.Code != http.StatusUnauthorized || len(cancels.cancels) != 0 {
		t.Fatalf("answer = %d %s, cancels %v, want a 401 and no cancel", rec.Code, rec.Body.String(), cancels.cancels)
	}
}

func TestCancelWithABlankOwnerIsRefusedAndWritesNothing(t *testing.T) {
	cancels := &stubCancels{runs: waiting()}
	d := Deps{Cancels: cancels, SPA: fstest.MapFS{}, Logger: discardLogger()}
	req := withEmail(httptest.NewRequest(http.MethodPost, "/api/runs/r1/cancel", strings.NewReader(cancelBody)), "")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	newMux(d).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || len(cancels.cancels) != 0 {
		t.Fatalf("answer = %d %s, cancels %v, want a 401 and no cancel", rec.Code, rec.Body.String(), cancels.cancels)
	}
}

func TestCancelWithoutAJSONBodyIsRefused(t *testing.T) {
	cancels := &stubCancels{runs: waiting()}
	rec := postCancel(t, Deps{Cancels: cancels}, "r1", "application/x-www-form-urlencoded", "", "owner@example.com")
	if rec.Code != http.StatusUnsupportedMediaType || len(cancels.cancels) != 0 {
		t.Fatalf("answer = %d %s, cancels %v, want a 415 and no cancel", rec.Code, rec.Body.String(), cancels.cancels)
	}
}

func TestCancelOfARunThatStoppedWaitingChangesNothing(t *testing.T) {
	for name, tt := range map[string]struct {
		refusal error
		want    string
	}{
		"already cancelled":             {store.ErrRunCancelled, "already-cancelled"},
		"already merged":                {store.ErrRunMerged, "already-merged"},
		"wrapped as the store wraps it": {fmt.Errorf("recording run r1's cancel: %w", store.ErrRunMerged), "already-merged"},
	} {
		t.Run(name, func(t *testing.T) {
			cancels := &stubCancels{runs: map[string]error{"r1": tt.refusal}}
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
	cancels := &stubCancels{runs: map[string]error{"r1": store.ErrRunNotWaiting}}
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

func TestCancelReportsAStoreItCannotWrite(t *testing.T) {
	var log strings.Builder
	cancels := &stubCancels{runs: map[string]error{"r1": errors.New("firestore unreachable")}}
	rec := postCancel(t, Deps{Cancels: cancels, Logger: slog.New(slog.NewTextHandler(&log, nil))},
		"r1", "application/json", cancelBody, "owner@example.com")
	if rec.Code != http.StatusInternalServerError || !strings.Contains(log.String(), "ownerCancelNotRecorded") {
		t.Fatalf("answer = %d %s, log = %q, want a 500 naming ownerCancelNotRecorded", rec.Code, rec.Body.String(), log.String())
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
