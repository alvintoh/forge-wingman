package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunWorkflowPassesTheBuildSummaryToPRMeta(t *testing.T) {
	yml, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "run.yml"))
	if err != nil {
		t.Fatal(err)
	}
	job, _, _ := strings.Cut(string(yml), "\n  pr:\n")
	_, job, _ = strings.Cut(job, "\n  pr-meta:\n")
	for _, want := range []string{
		"ATTEMPT_ID: ${{ needs.model.outputs.attempt_id }}",
		"SUMMARY: ${{ needs.model.outputs.summary }}",
		`-attempt-id "$ATTEMPT_ID" -summary "$SUMMARY"`,
	} {
		if !strings.Contains(job, want) {
			t.Errorf("run.yml's pr-meta job lacks %q", want)
		}
	}
}

func TestRunWorkflowWordsTheSupersededPRsCloseByWhyItWasReplaced(t *testing.T) {
	yml, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "run.yml"))
	if err != nil {
		t.Fatal(err)
	}
	_, step, _ := strings.Cut(string(yml), "      - name: Close the superseded PR\n")
	step, _, _ = strings.Cut(step, "\n      # The label only marks")
	for _, want := range []string{
		"if: needs.pr-meta.outputs.superseded_pr != ''",
		"RETRY: ${{ needs.pr-meta.outputs.superseded_by_retry }}",
		`--json state --jq .state)" = OPEN ]`,
		`gh pr close "$OLD" --comment "Superseded by $NEW: $why."`,
	} {
		if !strings.Contains(step, want) {
			t.Errorf("run.yml's close step lacks %q", want)
		}
	}
	if !strings.Contains(string(yml), "superseded_by_retry: ${{ steps.meta.outputs.superseded_by_retry }}") {
		t.Error("run.yml's pr-meta job does not pass superseded_by_retry on")
	}
	// Each reason is the one chosen by RETRY, so a swap of the two wordings is caught too.
	if flat := strings.Join(strings.Fields(step), " "); !strings.Contains(flat,
		`if [ "$RETRY" = true ]; then why="the owner retried this run with a note" else why="the owner chose a different option" fi`) {
		t.Errorf("run.yml's close step does not word a retry by RETRY and an option by its absence: %s", flat)
	}
	if _, script, _ := strings.Cut(step, "run: |"); strings.Contains(script, "${{") {
		t.Error("the close step interpolates an expression into its script; it reads $OLD, $NEW and $RETRY")
	}
}

func TestRunWorkflowMarksAutoMergeEligibilityOnlyOnPRMetasDecision(t *testing.T) {
	yml, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "run.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"FRONT_END_PATHS: ${{ vars.WINGMAN_FRONT_END_PATHS }}",
		`-front-end-paths "$FRONT_END_PATHS"`,
		"if: needs.pr-meta.outputs.auto_merge == 'true'\n",
		`gh pr edit "$URL" --add-label ` + autoMergeEligibleLabel,
		"auto_merge: ${{ steps.eligible.outputs.labelled }}",
		"AUTO_MERGE: ${{ needs.pr.outputs.auto_merge == 'true' }}",
		`-auto-merge="$AUTO_MERGE"`,
	} {
		if !strings.Contains(string(yml), want) {
			t.Errorf("run.yml lacks %q", want)
		}
	}
	if strings.Contains(string(yml), "gh pr merge") {
		t.Error("run.yml requests auto-merge itself; only a clean review may")
	}
	for _, gone := range []string{"WINGMAN_AUTO_MERGE", "auto-merge-switch"} {
		if strings.Contains(string(yml), gone) {
			t.Errorf("run.yml still passes %q; pr-meta reads the auto-merge switch and the rule from the store", gone)
		}
	}
}

func TestWithdrawWorkflowDisablesAutoMergeOnAPushToARunsBranch(t *testing.T) {
	yml, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "automerge-withdraw.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"pull_request_target:\n    types: [synchronize]",
		"startsWith(github.event.pull_request.head.ref, 'wingman/')",
		`requested="$(gh pr view "$PR" --json autoMergeRequest`,
		`gh pr merge "$PR" --disable-auto`,
		`gh pr edit "$PR" --remove-label wingman:auto-merge-eligible`,
	} {
		if !strings.Contains(string(yml), want) {
			t.Errorf("automerge-withdraw.yml lacks %q", want)
		}
	}
	if strings.Index(string(yml), "--remove-label") > strings.Index(string(yml), "auto-merge is not requested") {
		t.Error("automerge-withdraw.yml can exit before it removes the eligibility label")
	}
	if strings.Contains(string(yml), "actions/checkout") {
		t.Error("automerge-withdraw.yml checks out the branch it runs for")
	}
}

// TestRunWorkflowGatesTheStageOnTheDispatcher is the regression for the
// two-stage dispatch's dormancy: the stage gate is inert until the dispatcher
// stage=plan, and then the build-side jobs are skipped rather than run against
// a plan-stage branch.
func TestRunWorkflowGatesTheStageOnTheDispatcher(t *testing.T) {
	yml, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "run.yml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(yml)
	if want := "      stage:\n        description: the stage to run — plan or build\n        type: string\n        default: build\n        required: true\n"; !strings.Contains(s, want) {
		t.Errorf("run.yml lacks the stage input:\n%q", want)
	}
	for _, want := range []string{
		"stage: ${{ inputs.stage }}",
		"if: inputs.stage != 'plan' && needs.model.outputs.changed == 'true'\n",
		"if: always() && inputs.stage != 'plan' && github.ref == 'refs/heads/main'\n",
		"  record-plan-stage:\n",
		"if: always() && github.ref == 'refs/heads/main' && inputs.stage == 'plan'",
		"record-plan-stage\n          -run-id \"$RUN_ID\" -plan-file \"$RUNNER_TEMP/plan.json\" -dispatcher-uri \"$DISPATCHER_RUN_URI\"",
		"-run-url \"$RUN_URL\" -plan-stage-result \"${{ needs.model.result }}\"",
		"if: needs.model.result == 'success'\n        with:\n          name: plan\n",
		"      - name: Record the plan stage\n        if: always()\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("run.yml lacks %q", want)
		}
	}
	if n := strings.Count(s, "inputs.stage != 'plan'"); n != 4 {
		t.Errorf("run.yml gates %d jobs on the stage, want 4 (check, pr-meta, pr, record)", n)
	}
}

func TestModelWorkflowRunsThePlanStageForThePlanStream(t *testing.T) {
	yml, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "model.yml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(yml)
	for _, want := range []string{
		"      stage:\n        type: string\n        required: true\n",
		"if: inputs.stage != 'plan'\n",
		`"$RUNNER_TEMP/runner" plan-stage -plan-models "$PLAN_MODELS" -ticket-file "$RUNNER_TEMP/ticket.json" -out "$RUNNER_TEMP/plan.json"`,
		"name: plan\n          path: ${{ runner.temp }}/plan.json\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("model.yml lacks %q", want)
		}
	}
}

func TestRunWorkflowStampsTheClaimAttemptOnEverySettlingStep(t *testing.T) {
	yml, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "run.yml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(yml)
	if want := "      attempt:\n        description: "; !strings.Contains(s, want) {
		t.Errorf("run.yml lacks the attempt input")
	}
	if got := strings.Count(s, `-attempt "$ATTEMPT"`); got != 3 {
		t.Errorf("run.yml passes the claim attempt %d times, want 3: record-plan-stage, record and its unreadable-summary fallback", got)
	}
	if got := strings.Count(s, "ATTEMPT: ${{ inputs.attempt }}"); got != 2 {
		t.Errorf("run.yml reads the attempt input into %d jobs, want 2: record-plan-stage and record", got)
	}
}
