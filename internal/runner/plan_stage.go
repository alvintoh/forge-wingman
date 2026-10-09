package runner

import (
	"context"
	"errors"
)

// PlanStage runs the plan phase alone, for the plan-stage dispatch (FRG-33): it
// checks the model job's identity and the ticket, runs the plan agent with edit
// and shell denied, and returns the files the plan names and the commit the plan
// was made against. It writes nothing to the repository and reports no summary:
// the model job's identity cannot reach a run record, so the trusted
// record-plan-stage job re-checks this result before any record write.
//
// A plan naming a file under .github/workflows/ stops here, before any build
// could try to push it (FRG-61).
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
	sum := Summary{DurationsMS: map[string]int64{}, StartedAt: d.Now()}
	files, err := runPlanPhase(ctx, d, c, c.Repo, &sum)
	if err != nil {
		return PlanStageResult{}, err
	}
	return PlanStageResult{Files: files, BaseSHA: base}, nil
}
