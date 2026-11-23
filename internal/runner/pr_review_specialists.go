package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// The outcomes of one specialist's review of a pull request.
const (
	SpecialistReviewed    = "reviewed"
	SpecialistNotReviewed = "not reviewed"
)

// The reasons a specialist is not reviewed. The deadline and a cancel name why it
// stopped and can follow any number of attempts; the others name what every
// attempt ended in, and mixed endings are SpecialistReasonFailed.
const (
	SpecialistReasonCancelled  = "cancelled"
	SpecialistReasonDeadline   = "deadline"
	SpecialistReasonExhausted  = "allowance-exhausted"
	SpecialistReasonTimedOut   = "timed-out"
	SpecialistReasonUnreadable = "unreadable"
	SpecialistReasonFailed     = "failed"
)

const (
	// PRReviewDeadline is the hard stop for the specialist pass, counted from the
	// runner's start, which is after the review job's own setup (checkout, build,
	// image pull, artifact download: about 6 minutes). The job allows 45 minutes
	// (pr-review.yml, job review), so 25 leaves them and the artifact upload
	// inside it. It stops a specialist starting and also cuts the one running, and
	// no further model of it is started.
	PRReviewDeadline = 25 * time.Minute
	// maxPRSpecialistFindingsBytes caps one specialist's findings. A review of one
	// area is narrower than the general one.
	maxPRSpecialistFindingsBytes = 8 << 10
	// costEpsilon is how far a specialist's cost may differ from its attempts' sum.
	costEpsilon = 1e-6
)

// attemptReasons maps what every attempt of a specialist ended in to the
// specialist's reason; any other mix is SpecialistReasonFailed.
var attemptReasons = map[string]string{
	AttemptExhausted:  SpecialistReasonExhausted,
	AttemptTimedOut:   SpecialistReasonTimedOut,
	AttemptUnreadable: SpecialistReasonUnreadable,
}

// SpecialistVerdict is one area specialist's review: its findings and the model
// that answered, or why no model did, and every attempt it took.
type SpecialistVerdict struct {
	// Name is the review area's name.
	Name    string `json:"name"`
	Outcome string `json:"outcome"`
	// Reason is why the specialist is not reviewed, empty when it is.
	Reason string `json:"reason"`
	// Findings is the area's findings; empty when it found none or was not reviewed.
	Findings string `json:"findings"`
	// Model, Priced and Usage are the answering model's, empty when not reviewed.
	Model  string `json:"model"`
	Priced bool   `json:"priced"`
	Usage  Usage  `json:"usage"`
	// Cost is the dollar figure across every attempt, the unanswering ones included.
	Cost     float64           `json:"cost"`
	Attempts []PRReviewAttempt `json:"attempts"`
}

// SpecialistPass says which areas may be reviewed after the general review, and
// until when.
type SpecialistPass struct {
	// Areas are every configured review area; the pass runs those the diff touches.
	Areas []ReviewArea
	// Deadline is a hard stop: no specialist and no further model of one starts at
	// or after it, and an attempt still running then is cut. The zero time is a
	// deadline already past, so a pass that forgot to set one runs nothing.
	Deadline time.Time
	// Now reads the clock the starts are checked against; nil reads the wall
	// clock. The cut of a running attempt always counts in real time.
	Now func() time.Time
}

// ReviewPRWithSpecialists is ReviewPR followed by one specialist per review area
// the diff touches, in configuration order and one after another. Each runs its
// area's prompt once, on the first model of the area's list that answers readably.
// A specialist that no model answers, that the deadline reaches, or that a cancel
// interrupts is kept as not reviewed and does not change the general verdict or
// stop the others, which a cancel or the deadline then leaves not reviewed too:
// each is still asked for, finds no model it may start, and records no attempt.
// Only a failure of the general review is an error, as in ReviewPR.
func ReviewPRWithSpecialists(ctx context.Context, agent Agent, models []string, timeout time.Duration, c PRReviewContext, secrets []string, pass SpecialistPass) (PRVerdict, error) {
	v, err := ReviewPR(ctx, agent, models, timeout, c, secrets)
	if err != nil {
		return PRVerdict{}, err
	}
	now := pass.Now
	if now == nil {
		now = time.Now
	}
	mayStart := func() bool { return now().Before(pass.Deadline) }
	stop, cancel := context.WithDeadline(ctx, pass.Deadline)
	defer cancel()
	for _, area := range touchedReviewAreas(pass.Areas, c.Diff) {
		v.Specialists = append(v.Specialists, reviewSpecialist(ctx, stop, agent, area, timeout, c, secrets, mayStart))
	}
	return v, nil
}

// reviewSpecialist runs area's specialist under stop, which ends with the pass's
// deadline and with parent. When parent is done the specialist is cancelled; when
// only stop is, or mayStart says the deadline has passed, it reached the deadline.
func reviewSpecialist(parent, stop context.Context, agent Agent, area ReviewArea, timeout time.Duration, c PRReviewContext, secrets []string, mayStart func() bool) SpecialistVerdict {
	ans, err := reviewOnModels(stop, agent, area.Models, timeout, PRSpecialistPrompt(area, c.Diff, c.Ticket), secrets, mayStart)
	sv := SpecialistVerdict{Name: area.Name, Outcome: SpecialistNotReviewed, Cost: attemptsCost(ans.attempts),
		Attempts: append([]PRReviewAttempt{}, ans.attempts...)}
	switch {
	case ans.answered:
		sv.Outcome, sv.Model, sv.Priced, sv.Usage = SpecialistReviewed, ans.model, ans.result.Priced, ans.result.Usage
		sv.Findings = truncate(ans.result.Findings, maxPRSpecialistFindingsBytes-len("…"))
	case parent.Err() != nil:
		sv.Reason = SpecialistReasonCancelled
	case errors.Is(err, errReviewDeadline), errors.Is(err, context.DeadlineExceeded):
		sv.Reason = SpecialistReasonDeadline
	default:
		sv.Reason = specialistReason(ans.attempts)
	}
	return sv
}

// attemptsCost is the dollar figure across attempts.
func attemptsCost(attempts []PRReviewAttempt) float64 {
	var cost float64
	for _, a := range attempts {
		cost += a.Cost
	}
	return cost
}

// specialistReason is why no model answered: what every attempt ended in when
// they agree, else a plain failure.
func specialistReason(attempts []PRReviewAttempt) string {
	if len(attempts) == 0 {
		return SpecialistReasonFailed
	}
	reason, ok := attemptReasons[attempts[0].Outcome]
	if !ok {
		return SpecialistReasonFailed
	}
	for _, a := range attempts[1:] {
		if attemptReasons[a.Outcome] != reason {
			return SpecialistReasonFailed
		}
	}
	return reason
}

// specialistLensInstruction is what a specialist is asked after its area's
// prompt. It answers in the general review's format, so an empty review-findings
// block means the specialist found nothing in its lens.
const specialistLensInstruction = "Report only findings that fall in this lens and leave the rest to the other " +
	"reviewers. Do not edit any files or run any shell commands: this phase is review only.\n\n" +
	"Leave the review-findings block empty if you found nothing in this lens.\n\n" + reviewFindingsBlockInstruction

// PRSpecialistPrompt is a specialist's prompt: the general review's preamble and
// its diff and ticket, all data to review, then the area's own prompt as the
// lens and the review-findings format.
func PRSpecialistPrompt(area ReviewArea, diff string, t Ticket) string {
	return prReviewPreamble + reviewPrompt(diff, t, strings.TrimSpace(area.Prompt)+"\n\n"+specialistLensInstruction)
}

// specialistKeys are the keys every specialist entry carries: the writer emits
// them all, so an entry that lacks one was not written by it.
var specialistKeys = []string{"name", "outcome", "reason", "findings", "model", "priced", "usage", "cost", "attempts"}

// UnmarshalJSON refuses an entry with a key missing, and an unknown one, which a
// plain decode would read as the zero value.
func (s *SpecialistVerdict) UnmarshalJSON(b []byte) error {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(b, &keys); err != nil {
		return err
	}
	for _, k := range specialistKeys {
		if _, ok := keys[k]; !ok {
			return fmt.Errorf("specialist has no %q", k)
		}
	}
	type entry SpecialistVerdict
	return decodeStrict(b, (*entry)(s))
}

// validateSpecialists reports whether the specialists of an untrusted verdict
// are each a configured area, none repeated, and each states an outcome that
// agrees with its findings, model, reason, attempts and cost.
func validateSpecialists(specialists []SpecialistVerdict) error {
	configured := map[string]bool{}
	for _, a := range embeddedReviewAreas {
		configured[a.Name] = true
	}
	seen := map[string]bool{}
	for _, s := range specialists {
		label := truncate(s.Name, logErrorLimit)
		switch {
		case !configured[s.Name]:
			return fmt.Errorf("%w: specialist %q is not a review area", ErrPRReviewInvalid, label)
		case seen[s.Name]:
			return fmt.Errorf("%w: specialist %q is listed twice", ErrPRReviewInvalid, label)
		}
		seen[s.Name] = true
		if !s.shapeMatches() {
			return fmt.Errorf("%w: specialist %q outcome %q does not match its findings, model, reason and attempts", ErrPRReviewInvalid, label, truncate(s.Outcome, logErrorLimit))
		}
		if len(s.Findings) > maxPRSpecialistFindingsBytes {
			return fmt.Errorf("%w: specialist %q findings are over %d bytes", ErrPRReviewInvalid, label, maxPRSpecialistFindingsBytes)
		}
		if err := s.Usage.validate(); err != nil || !(s.Cost >= 0 && s.Cost <= maxCost) {
			return fmt.Errorf("%w: specialist %q has a figure out of range", ErrPRReviewInvalid, label)
		}
		if len(s.Attempts) > maxPRReviewAttempts || !validAttempts(s.Attempts) {
			return fmt.Errorf("%w: specialist %q has a malformed attempt", ErrPRReviewInvalid, label)
		}
		if math.Abs(s.Cost-attemptsCost(s.Attempts)) > costEpsilon {
			return fmt.Errorf("%w: specialist %q cost is not the sum of its attempts", ErrPRReviewInvalid, label)
		}
	}
	return nil
}

// shapeMatches reports whether the outcome agrees with the rest of the entry. A
// reviewed specialist has no reason, and its last attempt is the one that
// answered, by the model it names, after attempts that did not. A specialist not
// reviewed has no findings, model or usage, no attempt that answered, and the
// reason its attempts give: the deadline and a cancel can follow any attempts,
// and any other reason is what every attempt ended in.
func (s SpecialistVerdict) shapeMatches() bool {
	answered := func(a PRReviewAttempt) bool { return a.Outcome == AttemptReviewed }
	switch s.Outcome {
	case SpecialistReviewed:
		n := len(s.Attempts)
		return s.Reason == "" && n > 0 && answered(s.Attempts[n-1]) &&
			s.Attempts[n-1].Model == s.Model && !slices.ContainsFunc(s.Attempts[:n-1], answered)
	case SpecialistNotReviewed:
		return s.Findings == "" && s.Model == "" && !s.Priced && s.Usage == (Usage{}) &&
			!slices.ContainsFunc(s.Attempts, answered) && s.reasonFollowsAttempts()
	}
	return false
}

func (s SpecialistVerdict) reasonFollowsAttempts() bool {
	switch s.Reason {
	case SpecialistReasonDeadline, SpecialistReasonCancelled:
		return true
	}
	return len(s.Attempts) > 0 && s.Reason == specialistReason(s.Attempts)
}
