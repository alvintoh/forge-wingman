package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode"
)

// ErrFixRoundsState reports a state record this runner's loop could not have
// written: a field it does not know, a finding or a list past its cap, or a round
// number past the cap of rounds.
var ErrFixRoundsState = errors.New("the fix-round state is not one this runner wrote")

// maxStateFindingBytes is the longest finding line a state record may carry: the
// bound the round's own record writes, which truncate leaves up to one ellipsis
// past maxFindingBytes.
const maxStateFindingBytes = maxFindingBytes + len("…")

// maxStateStepDetailBytes is the longest step detail a round record may carry:
// stopDetailLimit, which truncate leaves up to one ellipsis past.
const maxStateStepDetailBytes = stopDetailLimit + len("…")

// maxCallsPerFixRound is the most agent calls one round numbers: its own, and one
// more for each backup model that answers in its place.
const maxCallsPerFixRound = 4

// FixRoundsState is what one fix round hands the next job: where the loop is, the
// findings still open, the ones a round declined, when the loop started, the
// session to resume and how many agent calls have been numbered. Each round runs
// in its own job, so this record is the only thing that carries the loop from one
// process to the next.
type FixRoundsState struct {
	// Round is how many rounds the loop has made.
	Round int `json:"round"`
	// Open is the findings the last re-review reported, one per line.
	Open []string `json:"open"`
	// Declined is every finding a round declined, with its reason.
	Declined []DeclinedFinding `json:"declined,omitempty"`
	// StartedAt is when the loop started, which each later round's share of
	// fixRoundsBudget is measured from.
	StartedAt time.Time `json:"started_at"`
	// Session is the session the next round resumes.
	Session string `json:"session,omitempty"`
	// Calls is how many agent calls the rounds have numbered, which the next
	// round's own call, its transcript and its commit trailer continue from. A
	// backup model answering advances that number past its round's own, so it is
	// not the round count.
	Calls int `json:"calls,omitempty"`
	// Rounds is each round's own record, oldest first, so the loop's evidence
	// travels with it from one job to the next.
	Rounds []ReviewFixRound `json:"rounds,omitempty"`
}

// NewFixRoundsState builds the record a round hands the next job, held to the
// limits ParseFixRoundsState reads: lines cut to maxFindingBytes with control
// characters made spaces, blank lines and unnamed declines dropped, a decline
// with no reason given noDeclineReason, and each list kept to its first
// maxReviewFindings entries.
func NewFixRoundsState(round, calls int, startedAt time.Time, session string, open []string, declined []DeclinedFinding) FixRoundsState {
	s := FixRoundsState{Round: round, Calls: calls, StartedAt: startedAt, Session: session, Open: []string{}}
	for _, line := range open {
		if line = stateLine(line); line != "" && len(s.Open) < maxReviewFindings {
			s.Open = append(s.Open, line)
		}
	}
	for _, d := range declined {
		finding, reason := stateLine(d.Finding), stateLine(d.Reason)
		if reason == "" {
			reason = noDeclineReason
		}
		if finding != "" && len(s.Declined) < maxReviewFindings {
			s.Declined = append(s.Declined, DeclinedFinding{Finding: finding, Reason: reason})
		}
	}
	return s
}

// WithRound returns s with round appended to its record, cut to the limits
// ParseFixRoundsState reads: lines cut and cleaned as NewFixRoundsState cleans
// them, and the round's step detail cut to stopDetailLimit.
func (s FixRoundsState) WithRound(round ReviewFixRound) FixRoundsState {
	clean := NewFixRoundsState(0, 0, time.Time{}, "", round.Findings, round.Declined)
	round.Findings, round.Declined = clean.Open, clean.Declined
	round.Step.Detail = truncate(round.Step.Detail, stopDetailLimit)
	s.Rounds = append(slices.Clone(s.Rounds), round)
	return s
}

// WithReview fills the last round's Resolved, New and Reviewed from the findings
// of the review that followed its commit, as the loop's own record does, and
// returns s unchanged when it holds no round or that round was already reviewed.
// The findings are cut to the caps a review's findings take first.
func (s FixRoundsState) WithReview(findings string) FixRoundsState {
	if len(s.Rounds) == 0 || s.Rounds[len(s.Rounds)-1].Reviewed {
		return s
	}
	rounds := slices.Clone(s.Rounds)
	last := &rounds[len(rounds)-1]
	capped, _, _ := CapReviewFindings(findings)
	last.Resolved, last.New = findingDelta(lineFindings(last.Findings), reviewPending(capped), s.Declined)
	last.Reviewed = true
	s.Rounds = rounds
	return s
}

// stateLine is a finding or reason as a record carries it: one line, control
// characters made spaces, trimmed and cut to maxFindingBytes.
func stateLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return truncate(strings.TrimSpace(s), maxFindingBytes)
}

// hasControl reports whether s holds a control character, a newline included.
func hasControl(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

// stateKeys is the keys a record may carry, spelled exactly as the encoder writes
// them: encoding/json would also match "Round" or "OPEN" to a field.
var stateKeys = func() map[string]bool {
	keys := map[string]bool{}
	for f := range reflect.TypeFor[FixRoundsState]().Fields() {
		keys[strings.Split(f.Tag.Get("json"), ",")[0]] = true
	}
	return keys
}()

// ParseFixRoundsState decodes and validates a state record the previous job
// wrote. A record holding a field this runner does not know, a round number past
// DefaultReviewFixRounds, or a finding past its line or count cap is rejected
// rather than trimmed: a trimmed record would carry a different loop forward than
// the one that wrote it.
func ParseFixRoundsState(raw string) (FixRoundsState, error) {
	if raw == "" {
		return FixRoundsState{}, fmt.Errorf("%w: no record", ErrFixRoundsState)
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	var s FixRoundsState
	if err := dec.Decode(&s); err != nil {
		return FixRoundsState{}, fmt.Errorf("%w: decoding: %v", ErrFixRoundsState, err)
	}
	if dec.More() {
		return FixRoundsState{}, fmt.Errorf("%w: trailing data", ErrFixRoundsState)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &keys); err != nil {
		return FixRoundsState{}, fmt.Errorf("%w: decoding: %v", ErrFixRoundsState, err)
	}
	for k := range keys {
		if !stateKeys[k] {
			return FixRoundsState{}, fmt.Errorf("%w: key %q is not spelled as the runner writes it", ErrFixRoundsState, k)
		}
	}
	if err := s.validate(); err != nil {
		return FixRoundsState{}, err
	}
	return s, nil
}

// Issued is the findings the next round is given: the open ones minus the ones a
// round already declined, which wait for the owner rather than for another round.
func (s FixRoundsState) Issued() []string {
	return findingTexts(withoutDeclined(lineFindings(s.Open), s.Declined))
}

func (s FixRoundsState) validate() error {
	if s.Round < 0 || s.Round > DefaultReviewFixRounds {
		return fmt.Errorf("%w: round %d is outside 0 to %d", ErrFixRoundsState, s.Round, DefaultReviewFixRounds)
	}
	if maxCalls := DefaultReviewFixRounds * maxCallsPerFixRound; s.Calls < s.Round || s.Calls > maxCalls {
		return fmt.Errorf("%w: %d agent calls for round %d, outside %d to %d", ErrFixRoundsState, s.Calls, s.Round, s.Round, maxCalls)
	}
	if s.StartedAt.IsZero() {
		return fmt.Errorf("%w: no start time", ErrFixRoundsState)
	}
	if len(s.Open) > maxReviewFindings {
		return fmt.Errorf("%w: %d open findings, over %d", ErrFixRoundsState, len(s.Open), maxReviewFindings)
	}
	for _, line := range s.Open {
		switch {
		case strings.TrimSpace(line) == "":
			return fmt.Errorf("%w: an open finding is blank", ErrFixRoundsState)
		case len(line) > maxStateFindingBytes:
			return fmt.Errorf("%w: an open finding is %d bytes, over %d", ErrFixRoundsState, len(line), maxStateFindingBytes)
		case hasControl(line):
			return fmt.Errorf("%w: an open finding holds a control character", ErrFixRoundsState)
		}
	}
	if len(s.Declined) > maxReviewFindings {
		return fmt.Errorf("%w: %d declined findings, over %d", ErrFixRoundsState, len(s.Declined), maxReviewFindings)
	}
	for _, d := range s.Declined {
		switch {
		case strings.TrimSpace(d.Finding) == "" || strings.TrimSpace(d.Reason) == "":
			return fmt.Errorf("%w: a declined finding or its reason is blank", ErrFixRoundsState)
		case len(d.Finding) > maxStateFindingBytes || len(d.Reason) > maxStateFindingBytes:
			return fmt.Errorf("%w: a declined finding or its reason is over %d bytes", ErrFixRoundsState, maxStateFindingBytes)
		case hasControl(d.Finding) || hasControl(d.Reason):
			return fmt.Errorf("%w: a declined finding or its reason holds a control character", ErrFixRoundsState)
		}
	}
	return s.validateRounds()
}

// validateRounds holds each round's record to the limits a round written by this
// runner meets: at most one per round made, each on the fix phase, with its lines
// as bounded as the open list's.
func (s FixRoundsState) validateRounds() error {
	if len(s.Rounds) > s.Round {
		return fmt.Errorf("%w: %d round records for %d rounds", ErrFixRoundsState, len(s.Rounds), s.Round)
	}
	for _, r := range s.Rounds {
		switch {
		case r.Step.Phase != PhaseFix:
			return fmt.Errorf("%w: a round record is on phase %q, not %q", ErrFixRoundsState, r.Step.Phase, PhaseFix)
		case len(r.Step.Detail) > maxStateStepDetailBytes:
			return fmt.Errorf("%w: a round's step detail is over %d bytes", ErrFixRoundsState, maxStateStepDetailBytes)
		case len(r.Findings) > maxReviewFindings || len(r.Declined) > maxReviewFindings:
			return fmt.Errorf("%w: a round record lists over %d findings", ErrFixRoundsState, maxReviewFindings)
		case r.Resolved < 0 || r.Resolved > maxReviewFindings || r.New < 0 || r.New > maxReviewFindings:
			return fmt.Errorf("%w: a round's resolved or new count is outside 0 to %d", ErrFixRoundsState, maxReviewFindings)
		}
		for _, line := range r.Findings {
			if strings.TrimSpace(line) == "" || len(line) > maxStateFindingBytes || hasControl(line) {
				return fmt.Errorf("%w: a round record holds a blank, over-long or control-character finding", ErrFixRoundsState)
			}
		}
		for _, d := range r.Declined {
			if strings.TrimSpace(d.Finding) == "" || strings.TrimSpace(d.Reason) == "" ||
				len(d.Finding) > maxStateFindingBytes || len(d.Reason) > maxStateFindingBytes ||
				hasControl(d.Finding) || hasControl(d.Reason) {
				return fmt.Errorf("%w: a round record holds a blank, over-long or control-character decline", ErrFixRoundsState)
			}
		}
	}
	return nil
}
