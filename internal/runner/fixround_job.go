package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

const (
	// FixBundleName is the git bundle of a fix round's one commit, in the
	// artifact the round writes.
	FixBundleName = "fix.bundle"
	// FixStateName is the updated state record, in the same artifact.
	FixStateName = "fix-state.json"
)

// FixRoundInput is one fix-round job's input: the pull request's branch and head,
// the review's findings, the previous round's state (nil on the first round) and
// the directory the round's artifact is written to.
type FixRoundInput struct {
	Branch   string
	Head     string
	Findings string
	State    *FixRoundsState
	OutDir   string
}

// FixRoundResult is what a fix-round job reports to its step: the commit it made
// (empty when the round changed no file), the round the loop is at afterwards and
// the reason the loop ended there, if it did.
type FixRoundResult struct {
	Commit string
	Round  int
	// Stop is set when the loop ends at this job: FixStopBudget or FixStopCap with
	// no round run, or FixStopNoChange for a round that changed no file.
	Stop   FixStop
	Detail string
}

// FixRoundIdentity is the stop a fix-round job makes when its environment holds a
// GitHub token or is not the owner's account: the model's job never holds one.
func FixRoundIdentity(id Identity) error {
	if err := id.CheckModel(); err != nil {
		return stopWith(OutcomeStopped, StopIdentityMismatch, err)
	}
	return nil
}

// FixRoundJob runs one fix round at the pull request's head from the state the
// previous round wrote and writes the round's artifact to in.OutDir: FixStateName
// always, and FixBundleName holding the round's commit when it made one. It
// pushes nothing. The identity is checked before the worktree is cut or any
// agent runs.
//
// With no state it seeds one at round 0 starting now. Every round counts against
// the one fixRoundsBudget measured from that start, and a round the budget leaves
// no room for, or one past DefaultReviewFixRounds, ends the loop with a result
// naming FixStopBudget or FixStopCap and writes no artifact. An agent, commit or
// worktree failure is a *StopError, and a round with no findings a plain error.
func FixRoundJob(ctx context.Context, d BuildDeps, c BuildConfig, in FixRoundInput) (FixRoundResult, error) {
	if err := FixRoundIdentity(c.Identity); err != nil {
		return FixRoundResult{}, err
	}
	if err := c.Ticket.Validate(); err != nil {
		return FixRoundResult{}, stopWith(OutcomeStopped, StopTicketMissing, err)
	}
	if !ValidModel(c.Model) {
		return FixRoundResult{}, stopWith(OutcomeStopped, StopModelInvalid, errors.New("model is not provider/model"))
	}
	if g, ok := d.Agent.(modelGate); ok {
		if err := gateFixModel(g, c.Model); err != nil {
			return FixRoundResult{}, err
		}
	}
	if in.Branch == "" || in.Head == "" || in.OutDir == "" {
		return FixRoundResult{}, errors.New("a fix round needs a branch, a head and a directory to write to")
	}
	state := seedOrContinue(d, in)
	if state.Round >= DefaultReviewFixRounds {
		return FixRoundResult{Round: state.Round, Stop: FixStopCap, Detail: fixCapDetail(DefaultReviewFixRounds)}, nil
	}
	issued := withoutDeclined(reviewPending(in.Findings), state.Declined)

	_, rules, _, err := fetchRules(ctx, d, c)
	if err != nil {
		return FixRoundResult{}, err
	}
	wt, err := CheckoutWorktree(ctx, c.Repo, filepath.Join(c.TempDir, "wt"), in.Branch, in.Head)
	switch {
	case errors.Is(err, ErrBranchHeadMismatch):
		return FixRoundResult{}, stopWith(OutcomeStopped, StopHeadMoved, err)
	case err != nil:
		return FixRoundResult{}, stopWith(OutcomeInfraFailure, StopWorktree, err)
	}
	if d.AgentProbe != nil {
		if err := d.AgentProbe(ctx, wt.Dir); err != nil {
			return FixRoundResult{}, stopWith(OutcomeInfraFailure, StopAgentContainer, err)
		}
	}
	sum := Summary{DurationsMS: map[string]int64{}, StartedAt: d.Now()}
	round, sha, err := RunFixRound(ctx, d, c, wt, &sum, FixRound{
		Branch: in.Branch, Head: in.Head, Session: state.Session, Rules: rules,
		Findings: findingTexts(issued), Calls: state.Calls, StartedAt: state.StartedAt,
	})
	if errors.Is(err, ErrFixRoundsBudget) {
		return FixRoundResult{Round: state.Round, Stop: FixStopBudget, Detail: err.Error()}, nil
	}
	if err != nil {
		return FixRoundResult{}, err
	}

	next := NewFixRoundsState(state.Round+1, round.Step.Round, state.StartedAt, round.Session, findingTexts(issued),
		append(slices.Clone(state.Declined), round.Declined...))
	next.Rounds = state.Rounds
	next = next.WithRound(round)
	if err := os.MkdirAll(in.OutDir, 0o700); err != nil {
		return FixRoundResult{}, fmt.Errorf("creating %s: %w", in.OutDir, err)
	}
	if sha != "" {
		if err := wt.Bundle(ctx, filepath.Join(in.OutDir, FixBundleName), in.Branch); err != nil {
			return FixRoundResult{}, stopWith(OutcomeInfraFailure, StopCommit, fmt.Errorf("bundling the round's commit: %w", err))
		}
	}
	if err := writeFixState(filepath.Join(in.OutDir, FixStateName), next); err != nil {
		return FixRoundResult{}, err
	}
	res := FixRoundResult{Commit: sha, Round: next.Round}
	if sha == "" {
		res.Stop, res.Detail = FixStopNoChange, fmt.Sprintf("round %d changed no file", next.Round)
	}
	return res, nil
}

// seedOrContinue is the state the round runs from: the previous round's, with its
// last round's record filled from the review that followed it, or a new one
// starting now at round 0 when there is none.
func seedOrContinue(d BuildDeps, in FixRoundInput) FixRoundsState {
	if in.State == nil {
		return NewFixRoundsState(0, 0, d.Now(), "", findingTexts(reviewPending(in.Findings)), nil)
	}
	return in.State.WithReview(in.Findings)
}

// gateFixModel is the stop for a model the agent's harness cannot serve: a
// harness lacking its key as a missing credential, any other as an invalid model.
func gateFixModel(g modelGate, model string) error {
	err := g.Gate(model)
	var notReady *harnessNotReadyError
	switch {
	case errors.As(err, &notReady):
		return stopWith(OutcomeStopped, StopCredentialAbsent, err)
	case err != nil:
		return stopWith(OutcomeStopped, StopModelInvalid, err)
	}
	return nil
}

// writeFixState writes s to path owner-only, after reading it back through the
// parser the next job uses, so a record that job would reject is never left for
// it.
func writeFixState(path string, s FixRoundsState) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encoding the fix-round state: %w", err)
	}
	if _, err := ParseFixRoundsState(string(raw)); err != nil {
		return err
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("writing the fix-round state: %w", err)
	}
	return nil
}
