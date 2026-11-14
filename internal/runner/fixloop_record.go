package runner

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

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
	// RoundNote, PushNote and AwaitNote are the jobs' own words, of which the
	// stopping one's is appended to the detail.
	RoundNote, PushNote, AwaitNote string
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
//
// A round reason that is not a StopReason is reported as "error", a stop that is
// not a FixStop as "unknown", and a push reason, taken as it came, on one line.
func FixRoundsWait(end FixLoopEnd) (reason WaitReason, detail string, ok bool) {
	var what, note string
	switch {
	case end.RoundReason != "":
		what, note = "fix round stopped: "+knownOr(end.RoundReason, stopReasons, roundStopError), end.RoundNote
	case end.PushReason != "":
		what, note = "fix push refused: "+oneLine(end.PushReason), end.PushNote
	case slices.Contains([]string{awaitStopTimeout, awaitStopNoVerdict, awaitStopError}, end.AwaitReason):
		what, note = "review not posted: "+end.AwaitReason, end.AwaitNote
	case end.Stop != "":
		what, note = "fix loop ended: "+knownOr(string(end.Stop), fixStops, unknownFixStop), end.RoundNote
	default:
		return "", "", false
	}
	if note := oneLine(note); note != "" {
		what += ": " + note
	}
	return WaitFixRoundsStopped, truncate(what, stopDetailLimit), true
}

// roundStopError is fix-round's reason for a failure that is not a stop, and
// what a reason that names no StopReason reads as.
const roundStopError = "error"

// unknownFixStop is what a stop fix-round reported that is not a FixStop reads as.
const unknownFixStop = "unknown"

// fixStops is every FixStop.
var fixStops = []string{string(FixStopNoChange), string(FixStopUnchanged), string(FixStopNew), string(FixStopDeclined),
	string(FixStopBudget), string(FixStopCap)}

// stopReasons is every StopReason record.go declares.
var stopReasons = []string{
	string(StopAgentContainer), string(StopAgentExit), string(StopAgentNoProgress), string(StopAgentTimeout),
	string(StopAllowanceExhausted), string(StopBranchMismatch), string(StopChecksRun), string(StopCommit),
	string(StopCompletions), string(StopCredentialAbsent), string(StopFormatterOutsideDiff), string(StopGitTampered),
	string(StopHeadMoved), string(StopIdentityMismatch), string(StopModelInvalid), string(StopModelRefused),
	string(StopModelUnavailable), string(StopNoBuildRecord), string(StopPRJob), string(StopPanic),
	string(StopPlanInvalid), string(StopPlanStageFailed), string(StopProjectionInvalid), string(StopProjectionMissing),
	string(StopProjectionRead), string(StopProviderSetting), string(StopRebaseConflict), string(StopRecordMissing),
	string(StopReviewInvalid), string(StopRunBudget), string(StopSecretInBranch), string(StopSetup),
	string(StopSummaryInvalid), string(StopSummaryUnreadable), string(StopTicketMissing), string(StopTicketNotDelivered),
	string(StopWaitingOnOwner), string(StopWorkflowChange), string(StopWorktree),
}

// knownOr is v when known lists it, and fallback otherwise.
func knownOr(v string, known []string, fallback string) string {
	if slices.Contains(known, v) {
		return v
	}
	return fallback
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

// ValidateMergedFixRound holds a round the record step merges after the pull
// request opened to ValidateFixRound's limits, and to a start inside the clock's
// window at now.
func ValidateMergedFixRound(attemptID string, r ReviewFixRound, now time.Time) error {
	if err := ValidateFixRound(attemptID, r); err != nil {
		return err
	}
	if at := r.Step.At; at.After(now.Add(maxClockSkew)) || at.Before(now.Add(-maxSummaryAge)) {
		return fmt.Errorf("fix round %d started outside the clock's window", r.Step.Round)
	}
	return nil
}

// ErrFixRoundNotOnFixPhase reports a round to merge whose step is not a pull
// request fix round's.
var ErrFixRoundNotOnFixPhase = errors.New("a round to merge is not on the fix phase")

// ErrFixRoundsOverCap reports a merge that would leave one attempt with more fix
// rounds than a loop makes.
var ErrFixRoundsOverCap = errors.New("the merge leaves an attempt with more fix rounds than a loop makes")

// fixRoundKey names a round: the attempt its completions object names, its
// phase and its round number.
type fixRoundKey struct {
	attempt string
	phase   Phase
	round   int
}

func keyOf(r ReviewFixRound) fixRoundKey {
	return fixRoundKey{completionsAttempt(r.Step), r.Step.Phase, r.Step.Round}
}

// completionsAttempt is the attempt id st's completions object names, empty when
// the object is not one completionsObject writes for st.
func completionsAttempt(st Step) string {
	suffix := "-" + string(st.Phase) + "-" + strconv.Itoa(st.Round) + ".jsonl"
	attempt, ok := strings.CutPrefix(st.CompletionsObject, "completions/")
	if !ok || !strings.HasSuffix(attempt, suffix) {
		return ""
	}
	return strings.TrimSuffix(attempt, suffix)
}

// WithFixRounds returns rec with the pull request's fix rounds in incoming
// merged in, keyed on each round's attempt, phase and round number: an incoming
// round replaces the one the record holds under its key, so merging the same
// rounds again changes nothing. A merge that would leave an attempt with more
// than DefaultReviewFixRounds rounds is ErrFixRoundsOverCap. The rounds' steps
// replace the record's fix steps, the token totals are summed again over every
// step, and a record already settled has its settled cost and time of use
// derived again from them.
func WithFixRounds(rec Record, incoming []ReviewFixRound) (Record, error) {
	if len(incoming) > DefaultReviewFixRounds {
		return Record{}, fmt.Errorf("%d rounds to merge, over %d", len(incoming), DefaultReviewFixRounds)
	}
	seen := map[fixRoundKey]bool{}
	for _, r := range incoming {
		if maxRound := DefaultReviewFixRounds * maxCallsPerFixRound; r.Step.Round < 1 || r.Step.Round > maxRound {
			return Record{}, fmt.Errorf("round %d is outside 1 to %d", r.Step.Round, maxRound)
		}
		if r.Step.Phase != PhaseFix {
			return Record{}, fmt.Errorf("%w: round %d is on phase %q", ErrFixRoundNotOnFixPhase, r.Step.Round, r.Step.Phase)
		}
		if seen[keyOf(r)] {
			return Record{}, fmt.Errorf("round %d is given twice", r.Step.Round)
		}
		seen[keyOf(r)] = true
	}
	merged := mergeFixRounds(rec, incoming)
	perAttempt := map[string]int{}
	for _, r := range fixPhaseRounds(merged) {
		perAttempt[completionsAttempt(r.Step)]++
	}
	for _, r := range incoming {
		if n := perAttempt[completionsAttempt(r.Step)]; n > DefaultReviewFixRounds {
			return Record{}, fmt.Errorf("%w: %d rounds, over %d", ErrFixRoundsOverCap, n, DefaultReviewFixRounds)
		}
	}
	return merged, nil
}

// mergeFixRounds is WithFixRounds' merge, which refuses nothing.
func mergeFixRounds(rec Record, incoming []ReviewFixRound) Record {
	seen := map[fixRoundKey]bool{}
	for _, r := range incoming {
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
	return rec
}

// carriedFixRounds is the fix rounds of existing that attemptID's run made, which
// a Finalize of that attempt keeps. An earlier attempt's rounds are left behind:
// their cost is already in what the earlier attempts settled.
func carriedFixRounds(existing Record, attemptID string) []ReviewFixRound {
	return slices.DeleteFunc(fixPhaseRounds(existing), func(r ReviewFixRound) bool {
		return completionsAttempt(r.Step) != attemptID
	})
}

// fixPhaseRounds is the rounds of rec that ran on the fix phase.
func fixPhaseRounds(rec Record) []ReviewFixRound {
	return slices.DeleteFunc(slices.Clone(rec.FixRounds), func(r ReviewFixRound) bool { return r.Step.Phase != PhaseFix })
}
