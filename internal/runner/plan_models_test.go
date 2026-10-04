package runner

import (
	"context"
	"slices"
	"testing"
)

const (
	allowanceStderr    = "Error: allowance exhausted for this billing period"
	unavailableStderr  = "Error: no endpoints found for this model"
	ordinaryFailStderr = "Error: the model returned malformed output"
)

func TestBuildPlanPhaseWalksTheModelListOnTheRealCLIAgent(t *testing.T) {
	list := []string{"opencode-go/paid", "opencode/free-a", "opencode/free-b"}
	tests := []struct {
		name          string
		stderr        string
		okModel       string
		wantModels    []string
		wantOutcome   Outcome
		wantReason    StopReason
		wantPlanSteps int
	}{
		{"allowance moves to the backup", allowanceStderr, "opencode/free-a", list[:2], OutcomeBuilt, "", 2},
		{"unavailability moves to the backup", unavailableStderr, "opencode/free-b", list, OutcomeBuilt, "", 3},
		{"every model out of allowance", allowanceStderr, "", list, OutcomeBudgetStop, StopAllowanceExhausted, 3},
		{"every model unavailable", unavailableStderr, "", list, OutcomeInfraFailure, StopModelUnavailable, 3},
		{"an ordinary failure never moves on", ordinaryFailStderr, "opencode/free-a", list[:1], OutcomeAgentFailed, StopAgentExit, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin, attempts := scriptedCLIAgent(t, tt.stderr, tt.okModel)
			objects := validObjects()
			objects["projections/"+testSHA+"/"+planProjectionFile] = []byte("# Plan rules\n\n" + ticketSentinel)
			deps, _, reported := testDeps(objects, &fakeAgent{edit: edit("version.go", "package x\n")})
			deps.PlanAgent = PlanCLIAgent(bin, "ignored/first")
			c := testConfig(t, initRepo(t))
			c.Ticket.Size = "M"
			c.PlanModels = list

			_, _ = Build(context.Background(), deps, c)

			var gotModels, stepModels []string
			for _, a := range attempts() {
				gotModels = append(gotModels, a.model)
				if a.agent != planAgentName || a.config != planAgentConfig {
					t.Fatalf("attempt %+v, want the plan agent shape on every model", a)
				}
			}
			if !slices.Equal(gotModels, tt.wantModels) {
				t.Fatalf("opencode ran with -m %v, want %v", gotModels, tt.wantModels)
			}
			rec := reported.last(t)
			for _, st := range rec.Steps {
				if st.Phase == PhasePlan {
					stepModels = append(stepModels, st.Model)
				}
			}
			if !slices.Equal(stepModels, gotModels) {
				t.Fatalf("recorded plan step models %v, want %v", stepModels, gotModels)
			}
			if len(stepModels) != tt.wantPlanSteps {
				t.Fatalf("plan steps = %d, want %d", len(stepModels), tt.wantPlanSteps)
			}
			if rec.Outcome != tt.wantOutcome || rec.StopReason != tt.wantReason {
				t.Fatalf("record = %s/%s, want %s/%s", rec.Outcome, rec.StopReason, tt.wantOutcome, tt.wantReason)
			}
		})
	}
}
