package webhook

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

const (
	agentSessionEvent = "AgentSessionEvent"
	actionCreated     = "created"
	actionPrompted    = "prompted"
	maxBodyBytes      = 1 << 20
	requestBudget     = 4 * time.Second
	callTimeout       = 2 * time.Second
)

// sessionPattern is a Linear agent session id, which becomes a document id.
var sessionPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// Markers records the agent sessions the webhook has taken.
type Markers interface {
	// Mark creates the session's marker, reporting false when one already exists.
	Mark(ctx context.Context, sessionID, issue string, at time.Time) (bool, error)
	// Unmark removes the session's marker, so a retried delivery acts again.
	Unmark(ctx context.Context, sessionID string) error
}

// Acknowledger tells Linear the agent has seen a new session.
type Acknowledger interface {
	Acknowledge(ctx context.Context, sessionID string) error
}

// Waker starts one dispatcher poll.
type Waker interface {
	Wake(ctx context.Context) error
}

// Replier records the owner's answer to a run's pending decision, reporting
// whether it recorded one.
type Replier interface {
	Reply(ctx context.Context, runID, sessionID, text string, at time.Time) (bool, error)
}

// Handler answers Linear's webhook deliveries.
//
// Only a verified, fresh AgentSessionEvent acts. A created one marks the
// session, acknowledges it and wakes the dispatcher; a redelivery of a marked
// session answers 200 and does neither again. A prompted one — the owner
// answering a run's decision — records the reply and wakes the dispatcher,
// which does the matching and the resume itself; the handler decides nothing.
type Handler struct {
	Key      *Secret
	Markers  Markers
	Sessions Acknowledger
	Poll     Waker
	Replier  Replier
	Logger   *slog.Logger
	Now      func() time.Time
}

// event is the part of a delivery the handler reads.
type event struct {
	Type             string `json:"type"`
	Action           string `json:"action"`
	WebhookTimestamp int64  `json:"webhookTimestamp"`
	AgentSession     struct {
		ID    string `json:"id"`
		Issue struct {
			Identifier string `json:"identifier"`
		} `json:"issue"`
	} `json:"agentSession"`
	// AgentActivity carries the owner's reply text on a prompted event.
	AgentActivity struct {
		Body string `json:"body"`
	} `json:"agentActivity"`
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		h.Logger.Warn("webhookRejected", "reason", "unreadable-body", "err", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if !verify(h.signingKey(r.Context()), body, r.Header.Get(signatureHeader)) {
		h.Logger.Warn("webhookRejected", "reason", "bad-signature")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var ev event
	if err := json.Unmarshal(body, &ev); err != nil {
		h.Logger.Warn("webhookRejected", "reason", "malformed", "err", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	now := h.Now()
	if !fresh(ev.WebhookTimestamp, now) {
		h.Logger.Warn("webhookRejected", "reason", "stale", "webhookTimestamp", ev.WebhookTimestamp)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if ev.Type != agentSessionEvent {
		h.Logger.Info("webhookIgnored", "type", ev.Type, "action", ev.Action)
		w.WriteHeader(http.StatusOK)
		return
	}
	if ev.Action == actionPrompted {
		h.answerPrompted(w, r, ev, now)
		return
	}
	if ev.Action != actionCreated {
		h.Logger.Info("webhookIgnored", "type", ev.Type, "action", ev.Action)
		w.WriteHeader(http.StatusOK)
		return
	}
	session, issue := ev.AgentSession.ID, ev.AgentSession.Issue.Identifier
	if !sessionPattern.MatchString(session) {
		h.Logger.Warn("webhookRejected", "reason", "no-session", "keys", payloadKeys(body))
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), requestBudget)
	defer cancel()
	created, err := h.Markers.Mark(ctx, session, issue, now)
	if err != nil {
		h.Logger.Error("webhookFailed", "reason", "marker", "session", session, "issue", issue, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !created {
		h.Logger.Info("webhookDuplicate", "session", session, "issue", issue)
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := h.ackAndWake(ctx, session, issue); err != nil {
		h.Logger.Error("webhookWakeFailed", "session", session, "issue", issue, "err", err)
		h.unmark(ctx, session)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	h.Logger.Info("webhookWoke", "session", session, "issue", issue)
	w.WriteHeader(http.StatusOK)
}

// answerPrompted records the owner's reply to a run's decision and wakes the
// dispatcher, which does the matching, the resume and the deadline. The handler
// decides nothing itself: a reply is a fact, and the dispatcher is its one
// home. A failed record or wake answers 500 so Linear retries; both are
// idempotent, so a redelivery is harmless.
func (h Handler) answerPrompted(w http.ResponseWriter, r *http.Request, ev event, now time.Time) {
	session, runID := ev.AgentSession.ID, ev.AgentSession.Issue.Identifier
	if !runner.ValidRunID(runID) {
		h.Logger.Warn("webhookRejected", "reason", "no-run", "session", session)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestBudget)
	defer cancel()
	recorded, err := h.Replier.Reply(ctx, runID, session, ev.AgentActivity.Body, now)
	if err != nil {
		h.Logger.Error("webhookFailed", "reason", "reply", "run", runID, "session", session, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	callCtx, cancelCall := context.WithTimeout(ctx, callTimeout)
	defer cancelCall()
	if err := h.Poll.Wake(callCtx); err != nil {
		h.Logger.Error("webhookWakeFailed", "run", runID, "session", session, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	h.Logger.Info("webhookReplied", "run", runID, "session", session, "recorded", recorded)
	w.WriteHeader(http.StatusOK)
}

// signingKey is the signing secret, re-read while it has no value. Once it
// has one it is kept, so a rotated secret needs a new revision.
func (h Handler) signingKey(ctx context.Context) string {
	if key := h.Key.Value(); key != "" {
		return key
	}
	return h.Key.Reload(ctx)
}

// unmark removes a marker on its own deadline, since a wake that failed by
// timing out may have spent the request's.
func (h Handler) unmark(ctx context.Context, session string) {
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), callTimeout)
	defer cancel()
	if err := h.Markers.Unmark(callCtx, session); err != nil {
		h.Logger.Error("webhookUnmarkFailed", "session", session, "err", err)
	}
}

// ackAndWake runs the acknowledgement and the wake side by side, since neither
// needs the other and both must land inside Linear's five-second reply window.
// It returns the wake's error; a failed acknowledgement is only logged.
func (h Handler) ackAndWake(ctx context.Context, session, issue string) error {
	var (
		wg      sync.WaitGroup
		wakeErr error
	)
	wg.Go(func() {
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		if err := h.Sessions.Acknowledge(callCtx, session); err != nil {
			h.Logger.Error("webhookAckFailed", "session", session, "issue", issue, "err", err)
		}
	})
	wg.Go(func() {
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		wakeErr = h.Poll.Wake(callCtx)
	})
	wg.Wait()
	return wakeErr
}

// payloadKeys names the fields a delivery carries at the top level, in
// agentSession and in its issue, and none of their values.
func payloadKeys(body []byte) []string {
	var keys []string
	prefix := ""
	level := body
	for _, field := range []string{"agentSession", "issue", ""} {
		var m map[string]json.RawMessage
		if json.Unmarshal(level, &m) != nil {
			break
		}
		for k := range m {
			keys = append(keys, prefix+k)
		}
		if field == "" {
			break
		}
		level = m[field]
		prefix += field + "."
	}
	slices.Sort(keys)
	return keys
}
