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
	"slices"
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

// fakeReplier records the replies the handler hands it.
type fakeReplier struct {
	mu       sync.Mutex
	err      error
	recorded bool
	calls    []replyCall
}

type replyCall struct {
	runID, sessionID, text string
	at                     time.Time
}

func (f *fakeReplier) Reply(_ context.Context, runID, sessionID, text string, at time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, replyCall{runID: runID, sessionID: sessionID, text: text, at: at})
	return f.recorded, f.err
}

// fakePriority records the priority writes the handler hands it, and the
// order of its calls against the waker's through log.
type fakePriority struct {
	err     error
	applied bool
	calls   []priorityCall
	log     *[]string
}

type priorityCall struct {
	runID string
	rank  int
}

func (f *fakePriority) UpdateQueuedPriority(_ context.Context, runID string, rank int) (bool, error) {
	f.calls = append(f.calls, priorityCall{runID: runID, rank: rank})
	if f.log != nil {
		*f.log = append(*f.log, "write")
	}
	return f.applied, f.err
}

// loggingWaker notes each wake in log.
type loggingWaker struct{ log *[]string }

func (w loggingWaker) Wake(context.Context) error {
	*w.log = append(*w.log, "wake")
	return nil
}

// fakeDriven answers from the Linear projects it drives, recording each one
// asked and the time it was asked at.
type fakeDriven struct {
	projects []string
	err      error
	asked    []string
	at       []time.Time
}

func (f *fakeDriven) DrivesLinearProject(_ context.Context, project string, at time.Time) (bool, error) {
	f.asked = append(f.asked, project)
	f.at = append(f.at, at)
	return slices.Contains(f.projects, project), f.err
}

// fakePhaseWakes keeps one claimed time as the store does, refusing a claim
// within the window of it.
type fakePhaseWakes struct {
	last     *time.Time
	err      error
	claims   int
	released []time.Time
}

func (f *fakePhaseWakes) Claim(_ context.Context, at time.Time, window time.Duration) (bool, error) {
	f.claims++
	if f.err != nil {
		return false, f.err
	}
	if f.last != nil && at.Sub(*f.last).Abs() < window {
		return false, nil
	}
	f.last = &at
	return true, nil
}

func (f *fakePhaseWakes) Release(ctx context.Context, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.released = append(f.released, at)
	if f.last != nil && f.last.Equal(at) {
		f.last = nil
	}
	return nil
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
		Replier: &fakeReplier{recorded: true}, Priority: &fakePriority{applied: true},
		Driven: &fakeDriven{projects: []string{"Roadmap"}}, PhaseWakes: &fakePhaseWakes{},
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
		{name: "prompted", body: delivery("prompted", "session-1", receivedAt), want: 200, woken: true},
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

// replyDelivery is a prompted payload carrying the owner's reply text.
func replyDelivery(session, issue, body string, sentAt time.Time) []byte {
	escaped := strings.NewReplacer(`"`, `\"`, "\n", `\n`).Replace(body)
	return []byte(`{"type":"AgentSessionEvent","action":"prompted","webhookTimestamp":` +
		strconv.FormatInt(sentAt.UnixMilli(), 10) + `,"agentSession":{"id":"` + session +
		`","issue":{"id":"issue-uuid-1","identifier":"` + issue + `"}},"agentActivity":{"id":"act-1","body":"` + escaped + `"}}`)
}

// TestPromptedRecordsTheReplyAndWakes covers AC3: the webhook records the
// reply as a fact and wakes the dispatcher, deciding nothing itself.
func TestPromptedRecordsTheReplyAndWakes(t *testing.T) {
	markers, acker, waker := &fakeMarkers{}, &fakeAcker{}, &fakeWaker{}
	replier := &fakeReplier{recorded: true}
	h := handler(markers, acker, waker)
	h.Replier = replier
	body := replyDelivery("session-1", "ABC-22", "reuse the existing flag", receivedAt)
	if got := deliver(h, body, testSecret); got != 200 {
		t.Fatalf("status = %d, want 200", got)
	}
	if len(replier.calls) != 1 {
		t.Fatalf("reply calls = %d, want 1", len(replier.calls))
	}
	got := replier.calls[0]
	if got.runID != "ABC-22" || got.sessionID != "session-1" || got.text != "reuse the existing flag" || !got.at.Equal(receivedAt) {
		t.Fatalf("reply = %+v", got)
	}
	if waker.woken != 1 {
		t.Fatalf("woken = %d, want 1", waker.woken)
	}
	if len(markers.marked) != 0 || len(acker.sessions) != 0 {
		t.Fatalf("a prompted event marked %v and acked %v", markers.marked, acker.sessions)
	}
}

// TestPromptedReadsTheReplyFromContentBody: the vendor describes the replied
// text differently on its two surfaces — the Agent Interaction docs put it at
// `agentActivity.body`, the GraphQL AgentActivity type carries it at
// `content.body` — so a payload shaped the second way still records its reply
// rather than an empty one that matches no option.
func TestPromptedReadsTheReplyFromContentBody(t *testing.T) {
	replier := &fakeReplier{recorded: true}
	h := handler(&fakeMarkers{}, &fakeAcker{}, &fakeWaker{})
	h.Replier = replier
	body := []byte(`{"type":"AgentSessionEvent","action":"prompted","webhookTimestamp":` +
		strconv.FormatInt(receivedAt.UnixMilli(), 10) + `,"agentSession":{"id":"session-1",` +
		`"issue":{"id":"issue-uuid-1","identifier":"ABC-22"}},` +
		`"agentActivity":{"id":"act-1","content":{"type":"prompt","body":"use the new flag"}}}`)
	if got := deliver(h, body, testSecret); got != 200 {
		t.Fatalf("status = %d, want 200", got)
	}
	if len(replier.calls) != 1 || replier.calls[0].text != "use the new flag" {
		t.Fatalf("reply calls = %+v", replier.calls)
	}
}

// TestPromptedRecordsNothingWhenTheRunWaitsOnNoDecision covers AC4: a reply the
// store records nothing for (no pending decision, or a mismatched session) is
// still answered, and still wakes the dispatcher.
func TestPromptedRecordsNothingWhenTheRunWaitsOnNoDecision(t *testing.T) {
	replier := &fakeReplier{recorded: false}
	h := handler(&fakeMarkers{}, &fakeAcker{}, &fakeWaker{})
	h.Replier = replier
	if got := deliver(h, replyDelivery("session-1", "ABC-22", "hello", receivedAt), testSecret); got != 200 {
		t.Fatalf("status = %d, want 200", got)
	}
	if len(replier.calls) != 1 {
		t.Fatalf("reply calls = %d, want 1", len(replier.calls))
	}
}

func TestPromptedRejectsAnUnusableRunID(t *testing.T) {
	replier := &fakeReplier{recorded: true}
	h := handler(&fakeMarkers{}, &fakeAcker{}, &fakeWaker{})
	h.Replier = replier
	if got := deliver(h, replyDelivery("session-1", "not/a/run", "x", receivedAt), testSecret); got != 400 {
		t.Fatalf("status = %d, want 400", got)
	}
	if len(replier.calls) != 0 {
		t.Fatalf("recorded %v for an unusable run id", replier.calls)
	}
}

func TestPromptedFailsWhenTheReplyCannotBeRecorded(t *testing.T) {
	h := handler(&fakeMarkers{}, &fakeAcker{}, &fakeWaker{})
	h.Replier = &fakeReplier{err: errors.New("unavailable")}
	if got := deliver(h, replyDelivery("session-1", "ABC-22", "x", receivedAt), testSecret); got != 500 {
		t.Fatalf("status = %d, want 500", got)
	}
}

func TestPromptedFailsWhenTheWakeFails(t *testing.T) {
	h := handler(&fakeMarkers{}, &fakeAcker{}, &fakeWaker{err: errors.New("unavailable")})
	if got := deliver(h, replyDelivery("session-1", "ABC-22", "x", receivedAt), testSecret); got != 500 {
		t.Fatalf("status = %d, want 500", got)
	}
}

// issueUpdate is an Issue update payload; data and updatedFrom are the raw JSON
// of each, as Linear's data change events carry them.
func issueUpdate(action, data, updatedFrom string) []byte {
	return issueUpdateAt(receivedAt, action, data, updatedFrom)
}

// issueUpdateAt is issueUpdate sent at sentAt.
func issueUpdateAt(sentAt time.Time, action, data, updatedFrom string) []byte {
	body := `{"type":"Issue","action":"` + action + `","webhookTimestamp":` +
		strconv.FormatInt(sentAt.UnixMilli(), 10) + `,"data":` + data
	if updatedFrom != "" {
		body += `,"updatedFrom":` + updatedFrom
	}
	return []byte(body + `}`)
}

func TestIssueUpdates(t *testing.T) {
	const issue = `{"id":"issue-uuid-1","identifier":"ABC-22","priority":1}`
	for _, tc := range []struct {
		name    string
		body    []byte
		applied bool
		err     error
		wakeErr error
		want    int
		calls   []priorityCall
		woken   int
	}{
		{name: "priority edit on a queued run wakes", body: issueUpdate("update", issue, `{"priority":3}`), applied: true, want: 200, calls: []priorityCall{{"ABC-22", 1}}, woken: 1},
		{name: "no priority becomes urgent", body: issueUpdate("update", issue, `{"priority":0}`), applied: true, want: 200, calls: []priorityCall{{"ABC-22", 1}}, woken: 1},
		{name: "urgent becomes no priority", body: issueUpdate("update", `{"identifier":"ABC-22","priority":0}`, `{"priority":1}`), applied: true, want: 200, calls: []priorityCall{{"ABC-22", 5}}, woken: 1},
		{name: "run not queued", body: issueUpdate("update", issue, `{"priority":3}`), want: 200, calls: []priorityCall{{"ABC-22", 1}}},
		{name: "another field changed", body: issueUpdate("update", issue, `{"title":"old"}`), applied: true, want: 200},
		{name: "no updatedFrom", body: issueUpdate("update", issue, ""), applied: true, want: 200},
		{name: "updatedFrom not an object", body: issueUpdate("update", issue, `"x"`), applied: true, want: 200},
		{name: "data not an object", body: issueUpdate("update", `"x"`, `{"priority":3}`), applied: true, want: 200},
		{name: "fractional priority", body: issueUpdate("update", issue, `{"priority":2.5}`), applied: true, want: 200},
		{name: "priority out of range", body: issueUpdate("update", `{"identifier":"ABC-22","priority":9}`, `{"priority":3}`), applied: true, want: 200},
		{name: "same rank", body: issueUpdate("update", issue, `{"priority":1}`), applied: true, want: 200},
		{name: "unusable identifier", body: issueUpdate("update", `{"identifier":"a/b","priority":1}`, `{"priority":3}`), applied: true, want: 200},
		{name: "not an update", body: issueUpdate("create", issue, `{"priority":3}`), applied: true, want: 200},
		{name: "not an Issue", body: []byte(strings.Replace(string(issueUpdate("update", issue, `{"priority":3}`)), `"Issue"`, `"Comment"`, 1)), applied: true, want: 200},
		{name: "write fails", body: issueUpdate("update", issue, `{"priority":3}`), err: errors.New("unavailable"), want: 500, calls: []priorityCall{{"ABC-22", 1}}},
		{name: "wake fails", body: issueUpdate("update", issue, `{"priority":3}`), applied: true, wakeErr: errors.New("403"), want: 500, calls: []priorityCall{{"ABC-22", 1}}, woken: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			priority, waker := &fakePriority{applied: tc.applied, err: tc.err}, &fakeWaker{err: tc.wakeErr}
			h := handler(&fakeMarkers{}, &fakeAcker{}, waker)
			h.Priority = priority
			if got := deliver(h, tc.body, testSecret); got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
			if !slices.Equal(priority.calls, tc.calls) {
				t.Fatalf("priority writes = %v, want %v", priority.calls, tc.calls)
			}
			if waker.woken != tc.woken {
				t.Fatalf("woken = %d, want %d", waker.woken, tc.woken)
			}
		})
	}
}

func TestIssueUpdateRejectsABadSignature(t *testing.T) {
	priority := &fakePriority{applied: true}
	h := handler(&fakeMarkers{}, &fakeAcker{}, &fakeWaker{})
	h.Priority = priority
	body := issueUpdate("update", `{"identifier":"ABC-22","priority":1}`, `{"priority":3}`)
	if got := deliver(h, body, "other"); got != 401 {
		t.Fatalf("status = %d, want 401", got)
	}
	if len(priority.calls) != 0 {
		t.Fatalf("wrote %v for an unsigned delivery", priority.calls)
	}
}

func TestIssueUpdateRejectsAStaleDelivery(t *testing.T) {
	priority := &fakePriority{applied: true}
	h := handler(&fakeMarkers{}, &fakeAcker{}, &fakeWaker{})
	h.Priority = priority
	h.Now = func() time.Time { return receivedAt.Add(time.Hour) }
	body := issueUpdate("update", `{"identifier":"ABC-22","priority":1}`, `{"priority":3}`)
	if got := deliver(h, body, testSecret); got != 401 {
		t.Fatalf("status = %d, want 401", got)
	}
	if len(priority.calls) != 0 {
		t.Fatalf("wrote %v for a stale delivery", priority.calls)
	}
}

func TestIssueUpdateWritesThePriorityBeforeTheWake(t *testing.T) {
	var log []string
	h := handler(&fakeMarkers{}, &fakeAcker{}, loggingWaker{&log})
	h.Priority = &fakePriority{applied: true, log: &log}
	body := issueUpdate("update", `{"identifier":"ABC-22","priority":1}`, `{"priority":3}`)
	if got := deliver(h, body, testSecret); got != 200 {
		t.Fatalf("status = %d, want 200", got)
	}
	if !slices.Equal(log, []string{"write", "wake"}) {
		t.Fatalf("order = %v, want the write before the wake", log)
	}
}

// roadmapIssue is an issue of the driven Roadmap project, as Linear's Issue
// payload carries its project.
const roadmapIssue = `{"id":"issue-uuid-1","identifier":"ABC-22","priority":1,"stateId":"state-done",` +
	`"projectMilestoneId":"milestone-2","project":{"id":"project-1","name":"Roadmap","url":"https://linear.app/p"}}`

func TestIssueMovesRefreshThePhases(t *testing.T) {
	otherProject := strings.Replace(roadmapIssue, `"name":"Roadmap"`, `"name":"Elsewhere"`, 1)
	noProject := `{"id":"issue-uuid-1","identifier":"ABC-22","priority":1,"stateId":"state-done"}`
	for _, tc := range []struct {
		name       string
		body       []byte
		driven     *fakeDriven
		wakes      *fakePhaseWakes
		applied    bool
		wakeErr    error
		want       int
		asked      []string
		claims     int
		woken      int
		priorities int
		released   bool
	}{
		{name: "state change wakes", body: issueUpdate("update", roadmapIssue, `{"stateId":"state-started"}`), want: 200, asked: []string{"Roadmap"}, claims: 1, woken: 1},
		{name: "milestone change wakes", body: issueUpdate("update", roadmapIssue, `{"projectMilestoneId":"milestone-1"}`), want: 200, asked: []string{"Roadmap"}, claims: 1, woken: 1},
		{name: "milestone set from none wakes", body: issueUpdate("update", roadmapIssue, `{"projectMilestoneId":null}`), want: 200, asked: []string{"Roadmap"}, claims: 1, woken: 1},
		{name: "neither state nor milestone", body: issueUpdate("update", roadmapIssue, `{"title":"old"}`), want: 200},
		{name: "project not driven", body: issueUpdate("update", otherProject, `{"stateId":"state-started"}`), want: 200, asked: []string{"Elsewhere"}},
		{name: "no project", body: issueUpdate("update", noProject, `{"stateId":"state-started"}`), want: 200},
		{name: "driven projects unreadable", body: issueUpdate("update", roadmapIssue, `{"stateId":"state-started"}`), driven: &fakeDriven{err: errors.New("unavailable")}, want: 500, asked: []string{"Roadmap"}},
		{name: "claim fails", body: issueUpdate("update", roadmapIssue, `{"stateId":"state-started"}`), wakes: &fakePhaseWakes{err: errors.New("unavailable")}, want: 500, asked: []string{"Roadmap"}, claims: 1},
		{name: "woken within the minute", body: issueUpdate("update", roadmapIssue, `{"stateId":"state-started"}`), wakes: &fakePhaseWakes{last: &receivedAt}, want: 200, asked: []string{"Roadmap"}, claims: 1},
		{name: "wake fails releases", body: issueUpdate("update", roadmapIssue, `{"stateId":"state-started"}`), wakeErr: errors.New("403"), want: 500, asked: []string{"Roadmap"}, claims: 1, woken: 1, released: true},
		{name: "a priority wake serves the move", body: issueUpdate("update", roadmapIssue, `{"stateId":"state-started","priority":3}`), applied: true, want: 200, claims: 1, woken: 1, priorities: 1},
		{name: "a priority edit on no queued run still refreshes", body: issueUpdate("update", roadmapIssue, `{"stateId":"state-started","priority":3}`), want: 200, asked: []string{"Roadmap"}, claims: 1, woken: 1, priorities: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			driven, wakes := tc.driven, tc.wakes
			if driven == nil {
				driven = &fakeDriven{projects: []string{"Roadmap"}}
			}
			if wakes == nil {
				wakes = &fakePhaseWakes{}
			}
			priority, waker := &fakePriority{applied: tc.applied}, &fakeWaker{err: tc.wakeErr}
			h := handler(&fakeMarkers{}, &fakeAcker{}, waker)
			h.Driven, h.PhaseWakes, h.Priority = driven, wakes, priority
			if got := deliver(h, tc.body, testSecret); got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
			if !slices.Equal(driven.asked, tc.asked) {
				t.Errorf("asked about %v, want %v", driven.asked, tc.asked)
			}
			for _, at := range driven.at {
				if !at.Equal(receivedAt) {
					t.Errorf("asked at %v, want the delivery's receipt %v", at, receivedAt)
				}
			}
			if wakes.claims != tc.claims || waker.woken != tc.woken || len(priority.calls) != tc.priorities {
				t.Errorf("claims %d, woken %d, priority writes %d; want %d, %d, %d",
					wakes.claims, waker.woken, len(priority.calls), tc.claims, tc.woken, tc.priorities)
			}
			if got := len(wakes.released) > 0; got != tc.released {
				t.Errorf("released = %v, want %v", got, tc.released)
			}
			if tc.released && !wakes.released[0].Equal(receivedAt) {
				t.Errorf("released the claim at %v, want %v", wakes.released[0], receivedAt)
			}
		})
	}
}

func TestIssueMovesWithinAMinuteWakeOnce(t *testing.T) {
	waker := &fakeWaker{}
	h := handler(&fakeMarkers{}, &fakeAcker{}, waker)
	for _, step := range []struct {
		after time.Duration
		woken int
	}{
		{0, 1},
		{20 * time.Second, 1},
		{59 * time.Second, 1},
		{time.Minute, 2},
	} {
		at := receivedAt.Add(step.after)
		h.Now = func() time.Time { return at }
		if got := deliver(h, issueUpdateAt(at, "update", roadmapIssue, `{"stateId":"state-started"}`), testSecret); got != 200 {
			t.Fatalf("after %v: status %d", step.after, got)
		}
		if waker.woken != step.woken {
			t.Fatalf("after %v: woken %d, want %d", step.after, waker.woken, step.woken)
		}
	}
}

func TestARetriedMoveWakesAfterAFailedWake(t *testing.T) {
	waker := &fakeWaker{err: errors.New("unavailable")}
	h := handler(&fakeMarkers{}, &fakeAcker{}, waker)
	body := issueUpdate("update", roadmapIssue, `{"stateId":"state-started"}`)
	if got := deliver(h, body, testSecret); got != 500 {
		t.Fatalf("first delivery = %d, want 500", got)
	}
	waker.err = nil
	if got := deliver(h, body, testSecret); got != 200 || waker.woken != 2 {
		t.Fatalf("retry = %d after %d wakes, want 200 after 2", got, waker.woken)
	}
}

func TestAMoveJustAfterAPriorityWakeDoesNotWakeAgain(t *testing.T) {
	waker := &fakeWaker{}
	h := handler(&fakeMarkers{}, &fakeAcker{}, waker)
	h.Priority = &fakePriority{applied: true}
	for _, step := range []struct {
		after       time.Duration
		updatedFrom string
	}{
		{0, `{"stateId":"state-started","priority":3}`},
		{10 * time.Second, `{"stateId":"state-review"}`},
	} {
		at := receivedAt.Add(step.after)
		h.Now = func() time.Time { return at }
		if got := deliver(h, issueUpdateAt(at, "update", roadmapIssue, step.updatedFrom), testSecret); got != 200 {
			t.Fatalf("after %v: status %d", step.after, got)
		}
	}
	if waker.woken != 1 {
		t.Fatalf("woken %d, want 1", waker.woken)
	}
}
