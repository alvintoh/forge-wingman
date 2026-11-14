package runner

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/alvintoh/forge-wingman/internal/money"
)

// FixLoopEnd is what the jobs of a fix-round loop reported when it ended: the
// stop each one made, empty for a job that did not stop it.
type FixLoopEnd struct {
	// RoundReason is fix-round's reason output: a StopReason for an agent or
	// commit stop, "error" for any other failure.
	RoundReason string
	// PushReason is fix-push's refusal reason, empty when it pushed. It is taken
	// as it came, so a reason this runner does not list still names its stop.
	PushReason string
	// AwaitReason is fix-await's reason output.
	AwaitReason string
	// Stop is the FixStop fix-round reported for a loop that ended without a
	// failure.
	Stop FixStop
	// Note is the stopping job's own words, appended to the detail.
	Note string
}

// The fix-await reasons that end the loop, as fix-await writes them.
const (
	awaitStopTimeout   = "timeout"
	awaitStopNoVerdict = "no-verdict"
	awaitStopError     = "error"
)

// FixRoundsWait names why a PR whose fix-round loop ended waits for the owner:
// WaitFixRoundsStopped and a detail naming the specific stop, from the first job
// that stopped in the order the loop runs them. ok is false when none did, as
// for a loop that ended with the review clean.
func FixRoundsWait(end FixLoopEnd) (reason WaitReason, detail string, ok bool) {
	var what string
	switch {
	case end.RoundReason != "":
		what = "fix round stopped: " + end.RoundReason
	case end.PushReason != "":
		what = "fix push refused: " + end.PushReason
	case slices.Contains([]string{awaitStopTimeout, awaitStopNoVerdict, awaitStopError}, end.AwaitReason):
		what = "review not posted: " + end.AwaitReason
	case end.Stop != "":
		what = "fix loop ended: " + string(end.Stop)
	default:
		return "", "", false
	}
	if note := oneLine(end.Note); note != "" {
		what += ": " + note
	}
	return WaitFixRoundsStopped, truncate(what, stopDetailLimit), true
}

// oneLine is s with every run of whitespace, newlines included, made one space.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// ValidateFixRound holds one fix round to the limits a summary's holds it to: its
// step names a model, a known harness and the completions object attemptID's
// run wrote, and a round whose re-review did not complete carries no counts.
func ValidateFixRound(attemptID string, r ReviewFixRound) error {
	if err := r.Step.validate(attemptID); err != nil {
		return err
	}
	if !r.Reviewed && (r.Resolved != 0 || r.New != 0) {
		return fmt.Errorf("fix round %d has counts but no completed re-review", r.Step.Round)
	}
	if r.Resolved < 0 || r.New < 0 || int64(r.Resolved) > maxCount || int64(r.New) > maxCount {
		return fmt.Errorf("fix round %d counts %d resolved and %d new, out of range", r.Step.Round, r.Resolved, r.New)
	}
	return nil
}

// ErrFixRoundNotOnFixPhase reports a round to merge whose step is not a pull
// request fix round's.
var ErrFixRoundNotOnFixPhase = errors.New("a round to merge is not on the fix phase")

type fixRoundKey struct {
	phase Phase
	round int
}

func keyOf(r ReviewFixRound) fixRoundKey { return fixRoundKey{r.Step.Phase, r.Step.Round} }

// WithFixRounds returns rec with the pull request's fix rounds in incoming
// merged in, keyed on each round's phase and round number: an incoming round
// replaces the one the record holds under its key, so merging the same rounds
// again changes nothing. The rounds' steps replace the record's fix steps, the
// token totals are summed again over every step, and a record already settled
// has its settled cost and time of use derived again from them.
func WithFixRounds(rec Record, incoming []ReviewFixRound) (Record, error) {
	seen := map[fixRoundKey]bool{}
	for _, r := range incoming {
		if r.Step.Phase != PhaseFix {
			return Record{}, fmt.Errorf("%w: round %d is on phase %q", ErrFixRoundNotOnFixPhase, r.Step.Round, r.Step.Phase)
		}
		if seen[keyOf(r)] {
			return Record{}, fmt.Errorf("round %d is given twice", r.Step.Round)
		}
		seen[keyOf(r)] = true
	}
	rounds := slices.DeleteFunc(slices.Clone(rec.FixRounds), func(r ReviewFixRound) bool { return seen[keyOf(r)] })
	rounds = append(rounds, incoming...)
	slices.SortStableFunc(rounds, func(a, b ReviewFixRound) int { return a.Step.Round - b.Step.Round })
	steps := slices.DeleteFunc(slices.Clone(rec.Steps), func(s Step) bool { return s.Phase == PhaseFix })
	for _, r := range rounds {
		if r.Step.Phase == PhaseFix {
			steps = append(steps, r.Step)
		}
	}
	rec.FixRounds, rec.Steps = rounds, steps
	rec.Tokens = sumSteps(steps)
	if !rec.SettledAt.IsZero() {
		rec.SettledProviderCostMicros = money.FromUSD(rec.Tokens.Cost) + rec.PriorSettledProviderCostMicros
		rec.TimeOfUse = timeOfUse(steps)
	}
	return rec, nil
}

// fixPhaseRounds is the rounds of rec that ran on the fix phase.
func fixPhaseRounds(rec Record) []ReviewFixRound {
	return slices.DeleteFunc(slices.Clone(rec.FixRounds), func(r ReviewFixRound) bool { return r.Step.Phase != PhaseFix })
}
