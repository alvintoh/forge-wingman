package runner

import (
	"context"
	"errors"
	"path/filepath"
)

// PlanStage runs the plan phase alone, for the plan-stage dispatch: it
// checks the model job's identity and the ticket, runs the plan agent with
// edit and shell denied, and returns the files the plan names, the commit the
// plan was made against and the model it ran on. It writes nothing to the
// repository and reports no summary: the model job's identity cannot reach a
// run record, so the trusted record-plan-stage job re-checks this result
// before any record write.
//
// When d.AgentProbe is set the agent runs in a worktree of its own, probed
// first, so a container never mounts the checkout, which holds the auth step's
// credentials file.
//
// A plan naming a file under .github/workflows/ stops here, before any build
// could try to push it.
func PlanStage(ctx context.Context, d BuildDeps, c BuildConfig) (PlanStageResult, error) {
	if err := c.Identity.CheckModel(); err != nil {
		return PlanStageResult{}, stopWith(OutcomeStopped, StopIdentityMismatch, err)
	}
	if err := c.Ticket.Validate(); err != nil {
		return PlanStageResult{}, stopWith(OutcomeStopped, StopTicketMissing, err)
	}
	if c.Ticket.Size == "S" {
		return PlanStageResult{}, stopWith(OutcomeStopped, StopPlanInvalid,
			errors.New("an S ticket is never plan-staged"))
	}
	if err := ValidatePlanModels(c.PlanModels); err != nil {
		return PlanStageResult{}, stopWith(OutcomeStopped, StopModelInvalid, err)
	}
	// A harness the run is not configured for stops here, before any agent runs
	// (AC7): a plan whose harnesses all lack their key as a missing credential,
	// any other as an invalid model.
	if g, ok := d.PlanAgent.(modelGate); ok {
		for _, m := range c.PlanModels {
			err := g.Gate(m)
			var notReady *harnessNotReadyError
			switch {
			case errors.As(err, &notReady):
				return PlanStageResult{}, stopWith(OutcomeStopped, StopCredentialAbsent, err)
			case err != nil:
				return PlanStageResult{}, stopWith(OutcomeStopped, StopModelInvalid, err)
			}
		}
	}
	base, err := HeadSHA(ctx, c.Repo)
	if err != nil {
		return PlanStageResult{}, stopWith(OutcomeInfraFailure, StopWorktree, err)
	}
	dir := c.Repo
	if d.AgentProbe != nil {
		wt, err := AddWorktree(ctx, c.Repo, filepath.Join(c.TempDir, "wt"), localBranch)
		if err != nil {
			return PlanStageResult{}, stopWith(OutcomeInfraFailure, StopWorktree, err)
		}
		dir = wt.Dir
		start := d.Now()
		if err := d.AgentProbe(ctx, dir); err != nil {
			return PlanStageResult{}, stopWith(OutcomeInfraFailure, StopAgentContainer, err)
		}
		d.Logger.Info("agentStartProbed", "ms", d.Now().Sub(start).Milliseconds())
	}
	sum := Summary{DurationsMS: map[string]int64{}, StartedAt: d.Now()}
	files, decision, err := runPlanPhase(ctx, d, c, dir, &sum)
	if err != nil {
		return PlanStageResult{}, err
	}
	return PlanStageResult{Files: files, BaseSHA: base, Model: planModel(sum), Decision: decision}, nil
}

// planModel names the model the plan phase's agent ran on: the model of its
// last step, which is the main plan model unless a backup took over, and empty
// when the phase recorded no step.
func planModel(sum Summary) string {
	for i := len(sum.Steps) - 1; i >= 0; i-- {
		if sum.Steps[i].Phase == PhasePlan {
			return sum.Steps[i].Model
		}
	}
	return ""
}
