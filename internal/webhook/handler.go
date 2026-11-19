package webhook

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

const (
	agentSessionEvent = "AgentSessionEvent"
	issueEvent        = "Issue"
	actionUpdate      = "update"
	actionCreated     = "created"
	actionPrompted    = "prompted"
	maxBodyBytes      = 1 << 20
	requestBudget     = 4 * time.Second
	callTimeout       = 2 * time.Second
	// phaseWakeWindow is how long one phase wake covers the edits after it. It
	// depends on a dispatcher job taking longer than the window to reach its
	// Linear read (about 2 minutes measured, gen2 start-up included), so an edit
	// inside the window is still in the poll the first one started. Revisit it if
	// the job's start-up gets faster than the window.
	phaseWakeWindow = time.Minute
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

// Reprioritizer applies an edited Linear priority to a run waiting for a claim,
// reporting whether the run holds that rank afterwards.
type Reprioritizer interface {
	UpdateQueuedPriority(ctx context.Context, runID string, rank int) (bool, error)
}

// DrivenProjects reports whether the dispatcher drives a Linear project, as
// its polls read at at.
type DrivenProjects interface {
	DrivesLinearProject(ctx context.Context, linearProject string, at time.Time) (bool, error)
}

// PhaseWakes debounces the wakes that refresh the phase snapshots across every
// instance of the webhook.
type PhaseWakes interface {
	// Claim records a wake at at, reporting false when one was recorded within
	// window of it.
	Claim(ctx context.Context, at time.Time, window time.Duration) (bool, error)
	// Release removes the wake claimed at at, so a retried delivery wakes again.
	Release(ctx context.Context, at time.Time) error
}

// Handler answers Linear's webhook deliveries.
//
// Only a verified, fresh AgentSessionEvent acts. A created one marks the
// session, acknowledges it and wakes the dispatcher; a redelivery of a marked
// session answers 200 and does neither again. A prompted one — the owner
// answering a run's decision — records the reply and wakes the dispatcher,
// which does the matching and the resume itself; the handler decides nothing.
// An Issue update that changes the priority is written to the issue's waiting
// run and wakes the dispatcher. One that moves an issue of a driven project to
// another state or milestone wakes the dispatcher, at most once a minute, so the
// poll rewrites the phase snapshots. Every other Issue update answers 200 and
// does nothing.
type Handler struct {
	Key        *Secret
	Markers    Markers
	Sessions   Acknowledger
	Poll       Waker
	Replier    Replier
	Priority   Reprioritizer
	Driven     DrivenProjects
	PhaseWakes PhaseWakes
	Logger     *slog.Logger
	Now        func() time.Time
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
		// Body is where Linear's Agent Interaction docs place a prompted reply
		// ("the agentActivity.body field of the webhook payload").
		Body string `json:"body"`
		// Content.Body is the same text where the GraphQL AgentActivity type
		// carries it. Read as a fallback because the vendor's two surfaces
		// describe this differently, and an empty reply would be recorded as an
		// answer that matches nothing.
		Content struct {
			Body string `json:"body"`
		} `json:"content"`
	} `json:"agentActivity"`
	// Data and UpdatedFrom stay raw: their shape is the entity's, and a
	// payload this handler cannot read must not fail the delivery.
	Data        json.RawMessage `json:"data"`
	UpdatedFrom json.RawMessage `json:"updatedFrom"`
}

// issueChange is the part of an Issue update the handler reads: the issue's
// identifier, project and current priority, its previous priority, and whether
// the update moved it to another state or milestone.
type issueChange struct {
	Identifier   string
	Project      string
	Priority     *float64
	PrevPriority *float64
	Moved        bool
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
	if ev.Type == issueEvent && ev.Action == actionUpdate {
		h.issueUpdated(w, r, ev, now)
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
	reply := ev.AgentActivity.Body
	if reply == "" {
		reply = ev.AgentActivity.Content.Body
	}
	recorded, err := h.Replier.Reply(ctx, runID, session, reply, now)
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

// issueUpdated answers an Issue update: a priority edit is written to the
// issue's waiting run and wakes the dispatcher, and a move to another state or
// milestone refreshes the phases. A priority wake also serves a move the same
// update carries. A failed step answers 500 so Linear retries.
func (h Handler) issueUpdated(w http.ResponseWriter, r *http.Request, ev event, now time.Time) {
	change := readIssueChange(ev)
	ctx, cancel := context.WithTimeout(r.Context(), requestBudget)
	defer cancel()
	woke, err := h.reprioritize(ctx, change)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	switch {
	case woke:
		if change.Moved {
			h.notePhaseWake(ctx, change, now)
		}
	case change.Moved:
		if err := h.refreshPhases(ctx, change, now); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	default:
		h.Logger.Info("webhookIgnored", "type", ev.Type, "action", ev.Action)
	}
	w.WriteHeader(http.StatusOK)
}

// reprioritize writes an edited priority to the issue's waiting run and wakes
// the dispatcher so the next poll orders by it, reporting whether it woke. An
// update that does not carry a priority change, or a payload that does not read
// as one, does neither. A failed write or wake is logged and returned; both are
// idempotent.
func (h Handler) reprioritize(ctx context.Context, change issueChange) (bool, error) {
	runID := change.Identifier
	rank, rankOK := priorityRank(change.Priority)
	prevRank, prevOK := priorityRank(change.PrevPriority)
	if !runner.ValidRunID(runID) || !rankOK || !prevOK || rank == prevRank {
		return false, nil
	}
	applied, err := h.Priority.UpdateQueuedPriority(ctx, runID, rank)
	if err != nil {
		h.Logger.Error("webhookFailed", "reason", "priority", "run", runID, "err", err)
		return false, err
	}
	if !applied {
		return false, nil
	}
	callCtx, cancelCall := context.WithTimeout(ctx, callTimeout)
	defer cancelCall()
	if err := h.Poll.Wake(callCtx); err != nil {
		h.Logger.Error("webhookWakeFailed", "run", runID, "err", err)
		return false, err
	}
	h.Logger.Info("webhookReprioritized", "run", runID, "rank", rank)
	return true, nil
}

// refreshPhases wakes the dispatcher for an issue moved to another state or
// milestone, so its poll rewrites the phase snapshots: only for a project the
// dispatcher drives, and only when no other instance has woken it within
// phaseWakeWindow. A failed wake releases its claim, so Linear's retry wakes.
func (h Handler) refreshPhases(ctx context.Context, change issueChange, now time.Time) error {
	if change.Project == "" {
		h.Logger.Info("webhookIgnored", "reason", "no-project", "issue", change.Identifier)
		return nil
	}
	driven, err := h.Driven.DrivesLinearProject(ctx, change.Project, now)
	if err != nil {
		h.Logger.Error("webhookFailed", "reason", "driven-projects", "issue", change.Identifier, "err", err)
		return err
	}
	if !driven {
		h.Logger.Info("webhookIgnored", "reason", "project-not-driven", "issue", change.Identifier)
		return nil
	}
	claimed, err := h.PhaseWakes.Claim(ctx, now, phaseWakeWindow)
	if err != nil {
		h.Logger.Error("webhookFailed", "reason", "phase-wake", "issue", change.Identifier, "err", err)
		return err
	}
	if !claimed {
		h.Logger.Info("webhookPhaseWakeDebounced", "issue", change.Identifier)
		return nil
	}
	callCtx, cancelCall := context.WithTimeout(ctx, callTimeout)
	defer cancelCall()
	if err := h.Poll.Wake(callCtx); err != nil {
		h.Logger.Error("webhookWakeFailed", "issue", change.Identifier, "err", err)
		h.releasePhaseWake(ctx, now)
		return err
	}
	h.Logger.Info("webhookPhasesRefreshed", "issue", change.Identifier)
	return nil
}

// notePhaseWake records a wake a priority edit made for an update that also
// moved the issue, so a move inside the window after it does not wake again. It
// decides nothing: a failure is logged, since the wake itself already happened.
func (h Handler) notePhaseWake(ctx context.Context, change issueChange, now time.Time) {
	if _, err := h.PhaseWakes.Claim(ctx, now, phaseWakeWindow); err != nil {
		h.Logger.Warn("webhookPhaseWakeNotRecorded", "issue", change.Identifier, "err", err)
	}
}

// releasePhaseWake releases a claim on its own deadline, since a wake that
// failed by timing out may have spent the request's.
func (h Handler) releasePhaseWake(ctx context.Context, at time.Time) {
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), callTimeout)
	defer cancel()
	if err := h.PhaseWakes.Release(callCtx, at); err != nil {
		h.Logger.Error("webhookPhaseWakeReleaseFailed", "err", err)
	}
}

// movedFields are the updatedFrom keys whose presence means the issue moved to
// another state or milestone. Linear lists a changed field's previous value
// there, null included, so the key's presence is the signal, not its value.
var movedFields = []string{"stateId", "projectMilestoneId"}

// readIssueChange reads an Issue update's identifier, project, priorities and
// moved fields, leaving any it cannot read nil, empty or false.
func readIssueChange(ev event) issueChange {
	var data struct {
		Identifier string   `json:"identifier"`
		Priority   *float64 `json:"priority"`
		Project    *struct {
			Name string `json:"name"`
		} `json:"project"`
	}
	_ = json.Unmarshal(ev.Data, &data)
	var from map[string]json.RawMessage
	_ = json.Unmarshal(ev.UpdatedFrom, &from)
	change := issueChange{Identifier: data.Identifier, Priority: data.Priority}
	if data.Project != nil {
		change.Project = data.Project.Name
	}
	if raw, ok := from["priority"]; ok {
		_ = json.Unmarshal(raw, &change.PrevPriority)
	}
	for _, field := range movedFields {
		if _, ok := from[field]; ok {
			change.Moved = true
		}
	}
	return change
}

// priorityRank is the queue rank of a Linear priority, or false when it is
// absent, fractional or outside Linear's range.
func priorityRank(p *float64) (int, bool) {
	if p == nil || *p != math.Trunc(*p) {
		return 0, false
	}
	rank, err := dispatcher.PriorityRank(int(*p))
	return rank, err == nil
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
