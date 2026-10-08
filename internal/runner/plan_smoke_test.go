package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPlanSmokeReportsWhetherTheRestrictedAgentWasRefused(t *testing.T) {
	tests := []struct {
		name        string
		agent       *fakeAgent
		wantRefused bool
		wantDetail  string
	}{
		{"nothing took effect", &fakeAgent{events: planEvent("cannot do that. DONE")}, true, "edit, new file and shell command refused"},
		{"the new file was created", &fakeAgent{edit: edit(smokeNewFile, "new\n")}, false, "the new file was created"},
		{"the edit took effect", &fakeAgent{edit: edit(smokeFixtureFile, "changed\n")}, false, "the edit took effect"},
		{"the shell command took effect", &fakeAgent{edit: edit(smokeBashMarker, "")}, false, "the shell command took effect"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := PlanSmoke(context.Background(), tt.agent, "p/m", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if res.Refused != tt.wantRefused || res.Detail != tt.wantDetail || res.Model != "p/m" {
				t.Fatalf("result = %+v", res)
			}
		})
	}
}

func TestPlanSmokeRunsTheScriptedBinaryUnderThePlanProfileOnTheGivenModel(t *testing.T) {
	bin, attempts := scriptedCLIAgent(t, "", "command-code/given")
	res, err := PlanSmoke(context.Background(), scriptedAgent(t, bin, ProfilePlan, "command-code/ignored"), "command-code/given", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got := attempts()
	if len(got) != 1 || got[0] != (fallbackAttempt{"command-code/given", "--plan"}) || !res.Refused {
		t.Fatalf("attempts %+v, result %+v", got, res)
	}
}

func TestPlanSmokeFailsWhenTheAgentRunFails(t *testing.T) {
	_, err := PlanSmoke(context.Background(), &fakeAgent{err: errors.New("exit status 1")}, "p/m", t.TempDir())
	if err == nil {
		t.Fatal("a failed agent run was reported as a result")
	}
}

func TestPlanSmokeFailsWhenTheDirectoryCannotBeWritten(t *testing.T) {
	if _, err := PlanSmoke(context.Background(), &fakeAgent{}, "p/m", filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("PlanSmoke wrote a fixture into a missing directory")
	}
}

func TestPlanSmokeClearsAnEarlierRunsEffects(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{smokeNewFile, smokeBashMarker} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	res, err := PlanSmoke(context.Background(), &fakeAgent{}, "p/m", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Refused {
		t.Fatalf("an earlier run's files counted against this one: %s", res.Detail)
	}
}
