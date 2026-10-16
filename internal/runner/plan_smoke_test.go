package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
			res, err := PlanSmoke(context.Background(), tt.agent, "p/m", t.TempDir(), nil)
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
	res, err := PlanSmoke(context.Background(), scriptedAgent(t, bin, ProfilePlan, "command-code/ignored"), "command-code/given", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := attempts()
	if len(got) != 1 || got[0] != (fallbackAttempt{"command-code/given", "always-ask"}) || !res.Refused {
		t.Fatalf("attempts %+v, result %+v", got, res)
	}
}

func TestPlanSmokeFailsWhenTheAgentRunFails(t *testing.T) {
	_, err := PlanSmoke(context.Background(), &fakeAgent{err: errors.New("exit status 1")}, "p/m", t.TempDir(), nil)
	if err == nil {
		t.Fatal("a failed agent run was reported as a result")
	}
}

func TestPlanSmokeRedactsSecretsFromAFailedRun(t *testing.T) {
	stderr := "OVERSIZE " + strings.Repeat("chatty ", 200) + "auth failed for key sk-live-123\n"
	agent := &fakeAgent{stderr: stderr, err: errors.New("exit status 1")}
	_, err := PlanSmoke(context.Background(), agent, "p/m", t.TempDir(), []string{"sk-live-123"})
	if err == nil {
		t.Fatal("a failed agent run was reported as a result")
	}
	msg := err.Error()
	if strings.Contains(msg, "sk-live-123") || !strings.Contains(msg, "auth failed for key [redacted]") ||
		!strings.Contains(msg, "exit status 1") {
		t.Fatalf("err = %q", msg)
	}
	if strings.Contains(msg, "OVERSIZE") {
		t.Fatalf("the reported stderr tail is not cut to a fixed length: %q", msg)
	}
}

func TestPlanSmokeReportsATimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	_, err := PlanSmoke(ctx, &fakeAgent{err: errors.New("signal: killed")}, "p/m", t.TempDir(), nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline", err)
	}
}

func TestPlanSmokeFailsWhenTheDirectoryCannotBeWritten(t *testing.T) {
	if _, err := PlanSmoke(context.Background(), &fakeAgent{}, "p/m", filepath.Join(t.TempDir(), "absent"), nil); err == nil {
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
	res, err := PlanSmoke(context.Background(), &fakeAgent{}, "p/m", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Refused {
		t.Fatalf("an earlier run's files counted against this one: %s", res.Detail)
	}
}
