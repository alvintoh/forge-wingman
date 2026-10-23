package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// stubMeasured is a MeasuredReader that answers with the merges it holds, or the
// error it was given.
type stubMeasured struct {
	merges []dispatcher.Sample
	err    error
	// limit is the cap the last read asked for.
	limit int
}

func (s *stubMeasured) Recent(_ context.Context, limit int) ([]dispatcher.Sample, error) {
	s.limit = limit
	if s.err != nil {
		return nil, s.err
	}
	return s.merges, nil
}

func getAutonomy(t *testing.T, reader *stubMeasured) *httptest.ResponseRecorder {
	t.Helper()
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Measured: reader})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/autonomy", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	return rec
}

// stubAutoMerge is an AutoMergeStore that answers with the switch it holds,
// recording what a write set it to.
type stubAutoMerge struct {
	enabled bool
	readErr error
	setErr  error
	// setAt is when the last write happened, zero while none did.
	setAt time.Time
}

func (s *stubAutoMerge) Enabled(context.Context) (bool, error) {
	if s.readErr != nil {
		return false, s.readErr
	}
	return s.enabled, nil
}

func (s *stubAutoMerge) Set(_ context.Context, enabled bool, at time.Time) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.enabled, s.setAt = enabled, at
	return nil
}

// measuredAt is when the merges the auto-merge tests read merged.
func measuredAt() time.Time { return time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC) }
