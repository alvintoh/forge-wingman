package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const testSecret = "whsec-test"

var receivedAt = time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC)

type fakeMarkers struct {
	held     map[string]bool
	err      error
	marked   []string
	unmarked []string
}

func (f *fakeMarkers) Mark(_ context.Context, sessionID, _ string, _ time.Time) (bool, error) {
	f.marked = append(f.marked, sessionID)
	if f.err != nil {
		return false, f.err
	}
	if f.held[sessionID] {
		return false, nil
	}
	if f.held == nil {
		f.held = map[string]bool{}
	}
	f.held[sessionID] = true
	return true, nil
}

func (f *fakeMarkers) Unmark(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.unmarked = append(f.unmarked, sessionID)
	delete(f.held, sessionID)
	return nil
}

type fakeAcker struct {
	mu       sync.Mutex
	err      error
	sessions []string
}

func (f *fakeAcker) Acknowledge(_ context.Context, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions = append(f.sessions, sessionID)
	return f.err
}

type fakeWaker struct {
	mu    sync.Mutex
	err   error
	woken int
}

func (f *fakeWaker) Wake(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.woken++
	return f.err
}

// fixedSecret holds a value that never needs re-reading.
func fixedSecret(value string) *Secret {
	return NewSecret("test", value, func(context.Context) (string, error) { return "", nil },
		func() time.Time { return receivedAt }, slog.New(slog.DiscardHandler))
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// delivery is a payload for session on issue ABC-22, sent at sentAt.
func delivery(action, session string, sentAt time.Time) []byte {
	return []byte(`{"type":"AgentSessionEvent","action":"` + action + `","webhookTimestamp":` +
		strconv.FormatInt(sentAt.UnixMilli(), 10) + `,"agentSession":{"id":"` + session +
		`","issue":{"id":"issue-uuid-1","identifier":"ABC-22","title":"Private title"}}}`)
}

// deliver posts body to h signed with secret, returning the status.
func deliver(h Handler, body []byte, secret string) int {
	req := httptest.NewRequest(http.MethodPost, "/linear", bytes.NewReader(body))
	req.Header.Set(signatureHeader, sign(secret, body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func handler(markers Markers, acker Acknowledger, waker Waker) Handler {
	return Handler{
		Key: fixedSecret(testSecret), Markers: markers, Sessions: acker, Poll: waker,
		Logger: slog.New(slog.DiscardHandler), Now: func() time.Time { return receivedAt },
	}
}

func TestHandler(t *testing.T) {
	created := delivery("created", "session-1", receivedAt)
	for _, tc := range []struct {
		name      string
		body      []byte
		signature string
		secret    string
		markers   fakeMarkers
		ackErr    error
		wakeErr   error
		want      int
		marked    []string
		unmarked  bool
		acked     bool
		woken     bool
	}{
		{name: "created wakes", body: created, want: 200, marked: []string{"session-1"}, acked: true, woken: true},
		{name: "bad signature", body: created, signature: sign("other", created), want: 401},
		{name: "unsigned", body: created, signature: "-", want: 401},
		{name: "empty secret", body: created, secret: "-", signature: sign("", created), want: 401},
		{name: "stale", body: delivery("created", "session-1", receivedAt.Add(-61*time.Second)), want: 401},
		{name: "from the future", body: delivery("created", "session-1", receivedAt.Add(61*time.Second)), want: 401},
		{name: "a minute old", body: delivery("created", "session-1", receivedAt.Add(-maxSkew)), want: 200, marked: []string{"session-1"}, acked: true, woken: true},
		{name: "a minute ahead", body: delivery("created", "session-1", receivedAt.Add(maxSkew)), want: 200, marked: []string{"session-1"}, acked: true, woken: true},
		{name: "over the size limit", body: append(created, bytes.Repeat([]byte(" "), maxBodyBytes)...), want: 400},
		{name: "no timestamp", body: []byte(`{"type":"AgentSessionEvent","action":"created"}`), want: 401},
		{name: "malformed", body: []byte(`{"type":`), want: 400},
		{name: "prompted", body: delivery("prompted", "session-1", receivedAt), want: 200},
		{name: "another event", body: bytes.Replace(created, []byte("AgentSessionEvent"), []byte("Issue"), 1), want: 200},
		{name: "no session", body: delivery("created", "", receivedAt), want: 400},
		{name: "session not a safe id", body: delivery("created", "a/b", receivedAt), want: 400},
		{name: "session too long", body: delivery("created", strings.Repeat("a", 65), receivedAt), want: 400},
		{name: "redelivered", body: created, markers: fakeMarkers{held: map[string]bool{"session-1": true}}, want: 200, marked: []string{"session-1"}},
		{name: "marker fails", body: created, markers: fakeMarkers{err: errors.New("unavailable")}, want: 500, marked: []string{"session-1"}},
		{name: "ack fails still wakes", body: created, ackErr: errors.New("refused"), want: 200, marked: []string{"session-1"}, acked: true, woken: true},
		{name: "wake fails", body: created, wakeErr: errors.New("403"), want: 500, marked: []string{"session-1"}, unmarked: true, acked: true, woken: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			signature := sign(testSecret, tc.body)
			if tc.signature == "-" {
				signature = ""
			} else if tc.signature != "" {
				signature = tc.signature
			}
			markers := tc.markers
			acker, waker := &fakeAcker{err: tc.ackErr}, &fakeWaker{err: tc.wakeErr}
			h := handler(&markers, acker, waker)
			if tc.secret == "-" {
				h.Key = fixedSecret("")
			}
			req := httptest.NewRequest(http.MethodPost, "/linear", bytes.NewReader(tc.body))
			req.Header.Set(signatureHeader, signature)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
			if strings.Join(markers.marked, ",") != strings.Join(tc.marked, ",") {
				t.Errorf("marked = %v, want %v", markers.marked, tc.marked)
			}
			if got := len(markers.unmarked) > 0; got != tc.unmarked {
				t.Errorf("unmarked = %v, want %v", got, tc.unmarked)
			}
			if got := len(acker.sessions) > 0; got != tc.acked {
				t.Errorf("acked = %v, want %v", got, tc.acked)
			}
			if tc.acked && acker.sessions[0] != "session-1" {
				t.Errorf("acked session %q, want session-1", acker.sessions[0])
			}
			if got := waker.woken > 0; got != tc.woken {
				t.Errorf("woken = %v, want %v", got, tc.woken)
			}
		})
	}
}

func TestSessionsOnOneIssue(t *testing.T) {
	markers, acker, waker := &fakeMarkers{}, &fakeAcker{}, &fakeWaker{}
	h := handler(markers, acker, waker)
	for _, step := range []struct {
		session string
		woken   int
	}{
		{"session-1", 1},
		{"session-1", 1},
		{"session-2", 2},
	} {
		if got := deliver(h, delivery("created", step.session, receivedAt), testSecret); got != 200 {
			t.Fatalf("%s: status %d", step.session, got)
		}
		if waker.woken != step.woken || len(acker.sessions) != step.woken {
			t.Fatalf("%s: woken %d, acked %v, want %d each", step.session, waker.woken, acker.sessions, step.woken)
		}
	}
}

func TestHandlerLogsKeysNotValuesWhenTheSessionIsAbsent(t *testing.T) {
	var logs bytes.Buffer
	h := handler(&fakeMarkers{}, &fakeAcker{}, &fakeWaker{})
	h.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	deliver(h, delivery("created", "", receivedAt), testSecret)

	out := logs.String()
	if !strings.Contains(out, `"agentSession.issue.title"`) || !strings.Contains(out, `"webhookTimestamp"`) {
		t.Errorf("logs lack the payload keys: %s", out)
	}
	if strings.Contains(out, "Private title") || strings.Contains(out, "issue-uuid-1") {
		t.Errorf("logs carry payload values: %s", out)
	}
}

func TestRetryAfterAFailedWakeWakesAgain(t *testing.T) {
	markers, waker := &fakeMarkers{}, &fakeWaker{err: errors.New("unavailable")}
	h := handler(markers, &fakeAcker{}, waker)
	body := delivery("created", "session-1", receivedAt)
	if got := deliver(h, body, testSecret); got != 500 {
		t.Fatalf("first delivery = %d, want 500", got)
	}
	waker.err = nil
	if got := deliver(h, body, testSecret); got != 200 {
		t.Fatalf("retry = %d, want 200", got)
	}
	if len(markers.marked) != 2 || !markers.held["session-1"] || waker.woken != 2 {
		t.Fatalf("marked %v, held %v, woken %d", markers.marked, markers.held, waker.woken)
	}
}

func TestSigningKeyReloadsWhileEmpty(t *testing.T) {
	now := receivedAt
	var reads int
	stored := ""
	h := handler(&fakeMarkers{}, &fakeAcker{}, &fakeWaker{})
	h.Key = NewSecret("test", "", func(context.Context) (string, error) {
		reads++
		return stored, nil
	}, func() time.Time { return now }, slog.New(slog.DiscardHandler))
	h.Now = func() time.Time { return now }

	if got := deliver(h, delivery("created", "session-1", now), testSecret); got != 401 || reads != 1 {
		t.Fatalf("empty: status %d after %d reads, want 401 after 1", got, reads)
	}
	stored = testSecret
	now = now.Add(reloadEvery - time.Second)
	if got := deliver(h, delivery("created", "session-2", now), testSecret); got != 401 || reads != 1 {
		t.Fatalf("throttled: status %d after %d reads, want 401 after 1", got, reads)
	}
	now = now.Add(time.Second)
	if got := deliver(h, delivery("created", "session-3", now), testSecret); got != 200 || reads != 2 {
		t.Fatalf("reloaded: status %d after %d reads, want 200 after 2", got, reads)
	}
	now = now.Add(time.Hour)
	if got := deliver(h, delivery("created", "session-4", now), testSecret); got != 200 || reads != 2 {
		t.Fatalf("kept: status %d after %d reads, want 200 after 2", got, reads)
	}
}

// cancelingWaker fails after the caller has gone, as a wake that times out does.
type cancelingWaker struct{ cancel context.CancelFunc }

func (w cancelingWaker) Wake(context.Context) error {
	w.cancel()
	return context.Canceled
}

func TestAFailedWakeUnmarksAfterTheRequestIsGone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	markers := &fakeMarkers{}
	h := handler(markers, &fakeAcker{}, cancelingWaker{cancel})
	body := delivery("created", "session-1", receivedAt)
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/linear", bytes.NewReader(body))
	req.Header.Set(signatureHeader, sign(testSecret, body))
	h.ServeHTTP(httptest.NewRecorder(), req)
	if markers.held["session-1"] {
		t.Fatal("the marker survived a failed wake")
	}
}
