package runner

import (
	"context"
	"errors"
	"fmt"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

// ModelLabelPrefix starts the ticket label naming the model a run's build runs on.
const ModelLabelPrefix = "model:"

// modelTier is where a model sits on its plan's escalation ladder.
var modelTier = providers.TierOf

// AttemptReader reads a run's ended attempts, which an owner's retry archives
// before it requeues the run, numbered as the run's attempt is, from 0.
type AttemptReader interface {
	GetAttempt(ctx context.Context, runID string, attempt int) (Record, error)
}

// escalates reports whether a run that ended on outcome in phase is one the
// escalation ladder answers: the build agent failing its own work. A gate stop,
// an infrastructure fault and an exhausted allowance each end on an outcome of
// their own, so none of them ever names a tier.
func escalates(phase Phase, outcome Outcome) bool {
	return phase == PhaseBuild && outcome == OutcomeAgentFailed
}

// buildModel is the model the run's build phase started on, empty when it ran
// none. Its first build step's: a later step may be an availability fallback,
// which is never a tier.
func (r Record) buildModel() string {
	for _, st := range r.Steps {
		if st.Phase == PhaseBuild {
			return st.Model
		}
	}
	return ""
}

// escalationDetail is rec's stop detail opened by its escalation note, or left
// as it is when the model's plan has no ladder. The attempt the owner retried is
// read from store only when rec ran on an escalated tier, and only where store
// archives attempts.
func escalationDetail(ctx context.Context, store RecordStore, in FinalizeInput, rec Record) string {
	model := rec.buildModel()
	tier := modelTier(model)
	if !tier.Laddered {
		return rec.StopDetail
	}
	attempt := 0
	if in.Attempt != nil {
		attempt = *in.Attempt
	}
	prior := ""
	if reader, ok := store.(AttemptReader); ok && tier.Rung > 0 {
		prior = retriedAttempt(ctx, reader, in.RunID, attempt)
	}
	note := escalationNote(model, tier, attempt, prior)
	if rec.StopDetail == "" {
		return note
	}
	return note + "\n\n" + rec.StopDetail
}

// retriedAttempt is how the newest archived attempt before attempt ended, empty
// when none was archived. Only an owner's retry archives one: an automatic
// requeue after a transient fault or a rebase conflict moves the attempt on
// without, so the newest archive is the attempt the owner retried.
func retriedAttempt(ctx context.Context, reader AttemptReader, runID string, attempt int) string {
	for n := attempt - 1; n >= 0; n-- {
		archived, err := reader.GetAttempt(ctx, runID, n)
		switch {
		case err == nil:
			return fmt.Sprintf("Attempt %d %s.", n, attemptSummary(archived))
		case !errors.Is(err, ErrRecordNotFound):
			return fmt.Sprintf("Attempt %d could not be read.", n)
		}
	}
	return ""
}

// escalationNote names the tier above model and the label that chooses it, that
// none is configured, or, for a run already above the ladder's first tier, that
// its one escalation is spent — beside prior, how the attempt the owner retried
// ended.
func escalationNote(model string, tier providers.Tier, attempt int, prior string) string {
	switch {
	case tier.Rung > 0:
		note := fmt.Sprintf("Escalation spent: attempt %d failed its build on the escalated tier %s, so the run ends here.", attempt, model)
		if prior != "" {
			note += " " + prior
		}
		return note
	case tier.Next != "":
		return fmt.Sprintf("Next model tier: %s. A `%s%s` label on the ticket chooses it, and a retry then runs once on that tier.",
			tier.Next, ModelLabelPrefix, tier.Next)
	}
	return fmt.Sprintf("No model tier is configured above %s.", model)
}

// attemptSummary is how an archived attempt ended and the model its build
// started on.
func attemptSummary(r Record) string {
	ended := "ended " + string(r.Outcome)
	if r.StopReason != "" {
		ended += " (" + string(r.StopReason) + ")"
	}
	if m := r.buildModel(); m != "" {
		ended += " on " + m
	}
	return ended
}
