package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrFixRoundsState reports a state record this runner's loop could not have
// written: a field it does not know, a finding or a list past its cap, or a round
// number past the cap of rounds.
var ErrFixRoundsState = errors.New("the fix-round state is not one this runner wrote")

// maxStateFindingBytes is the longest finding line a state record may carry: the
// bound the round's own record writes, which truncate leaves up to one ellipsis
// past maxFindingBytes.
const maxStateFindingBytes = maxFindingBytes + len("…")

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
}

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
	if s.Calls < 0 {
		return fmt.Errorf("%w: %d agent calls", ErrFixRoundsState, s.Calls)
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
		}
	}
	if len(s.Declined) > maxReviewFindings {
		return fmt.Errorf("%w: %d declined findings, over %d", ErrFixRoundsState, len(s.Declined), maxReviewFindings)
	}
	for _, d := range s.Declined {
		if len(d.Finding) > maxStateFindingBytes || len(d.Reason) > maxStateFindingBytes {
			return fmt.Errorf("%w: a declined finding or its reason is over %d bytes", ErrFixRoundsState, maxStateFindingBytes)
		}
	}
	return nil
}
