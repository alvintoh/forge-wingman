package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/webhook"
)

func clientOf(c *http.Client) func(context.Context) (*http.Client, error) {
	return func(context.Context) (*http.Client, error) { return c, nil }
}

func TestWakeAfterRecordPostsOnceAndLogsSuccess(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var logs bytes.Buffer
	requests := &wakeRequests{}
	wakeAfterRecord(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), clientOf(srv.Client()), requests, srv.URL, "run-1")

	if got := posts.Load(); got != 1 || requests.n != 1 {
		t.Fatalf("posts = %d, wake requests = %d, want 1 of each", got, requests.n)
	}
	if !strings.Contains(logs.String(), "msg=dispatcherWoken") {
		t.Fatalf("logs = %q, want dispatcherWoken", logs.String())
	}
}

func TestWakeAfterRecordLogsAFailedWake(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	var logs bytes.Buffer
	wakeAfterRecord(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), clientOf(srv.Client()), &wakeRequests{}, srv.URL, "run-1")

	out := logs.String()
	if !strings.Contains(out, "level=WARN msg=dispatcherWakeFailed run=run-1") || !strings.Contains(out, "500") {
		t.Fatalf("logs = %q, want a dispatcherWakeFailed warning naming the 500", out)
	}
}

func TestWakeAfterRecordSkipsAnEmptyURIWithoutBuildingCredentials(t *testing.T) {
	built := false
	newClient := func(context.Context) (*http.Client, error) {
		built = true
		return nil, errors.New("must not be called")
	}

	var logs bytes.Buffer
	wakeAfterRecord(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), newClient, &wakeRequests{}, "", "run-1")

	if built {
		t.Fatal("credentials were built for an empty URI")
	}
	if !strings.Contains(logs.String(), "msg=dispatcherWakeSkipped") {
		t.Fatalf("logs = %q, want dispatcherWakeSkipped", logs.String())
	}
}

func TestWakeAfterRecordLogsMissingCredentialsAndSendsNothing(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	noCredentials := func(context.Context) (*http.Client, error) { return nil, errors.New("no default credentials") }

	var logs bytes.Buffer
	wakeAfterRecord(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), noCredentials, &wakeRequests{}, srv.URL, "run-1")

	if got := requests.Load(); got != 0 {
		t.Fatalf("requests = %d, want 0", got)
	}
	out := logs.String()
	if !strings.Contains(out, "level=WARN msg=dispatcherWakeFailed") || !strings.Contains(out, "no default credentials") {
		t.Fatalf("logs = %q, want a dispatcherWakeFailed warning naming the credentials error", out)
	}
}

func TestWakeAfterRecordStopsWaitingAtTheBound(t *testing.T) {
	saved := wakeTimeout
	wakeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { wakeTimeout = saved })

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)

	var logs bytes.Buffer
	start := time.Now()
	wakeAfterRecord(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), clientOf(srv.Client()), &wakeRequests{}, srv.URL, "run-1")

	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("wake took %s, want it to stop near %s", elapsed, wakeTimeout)
	}
	if !strings.Contains(logs.String(), "msg=dispatcherWakeFailed") {
		t.Fatalf("logs = %q, want dispatcherWakeFailed", logs.String())
	}
}

// wakeRequests counts the wake requests a wake records, failing them with err.
type wakeRequests struct {
	n   int
	err error
}

func (w *wakeRequests) RequestWake(context.Context) error {
	w.n++
	return w.err
}

// TestWakeAfterRecordStartsTheExecutionWhenTheRecordFails keeps a Firestore
// blip on the wake record from losing the finished run's wake.
func TestWakeAfterRecordStartsTheExecutionWhenTheRecordFails(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var logs bytes.Buffer
	wakeAfterRecord(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), clientOf(srv.Client()),
		&wakeRequests{err: errors.New("unavailable")}, srv.URL, "run-1")

	out := logs.String()
	if posts.Load() != 1 || !strings.Contains(out, "msg=dispatcherWakeNotRecorded") || !strings.Contains(out, "msg=dispatcherWoken") {
		t.Fatalf("posts = %d, logs = %q, want one post logged as woken and not recorded", posts.Load(), out)
	}
}

// stubWaker answers a wake with err.
type stubWaker struct{ err error }

func (s stubWaker) Wake(context.Context) error { return s.err }

// TestWakeAfterPlanDoesNotFailASettledPlanOnAnUnrecordedWake: the execution
// started, so the plan stage succeeds and logs the missing record; any other
// wake failure still fails the stage.
func TestWakeAfterPlanDoesNotFailASettledPlanOnAnUnrecordedWake(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	unrecorded := fmt.Errorf("%w: unavailable", webhook.ErrWakeNotRecorded)
	if err := wakeAfterPlan(context.Background(), logger, stubWaker{err: unrecorded}, "run-1"); err != nil {
		t.Fatalf("err = %v, want none for an execution that started", err)
	}
	if !strings.Contains(logs.String(), "msg=dispatcherWakeNotRecorded") {
		t.Fatalf("logs = %q, want dispatcherWakeNotRecorded", logs.String())
	}
	if err := wakeAfterPlan(context.Background(), logger, stubWaker{err: errors.New("403")}, "run-1"); err == nil {
		t.Fatal("a failed start was not returned")
	}
}
