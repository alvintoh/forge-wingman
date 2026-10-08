package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	freeReviewModel   = "command-code/poolside/laguna-s-2.1-free"
	pricedReviewModel = "command-code/deepseek/deepseek-v4.1-flash"
	unpricedModel     = "command-code/unpriced/model"
	usedTokens        = `{"type":"step_finish","part":{"tokens":{"input":1000,"output":100},"cost":0}}` + "\n"
)

func TestReviewSmokeMetersAndReadsTheReview(t *testing.T) {
	tests := []struct {
		name        string
		model       string
		events      string
		wantPriced  bool
		wantCharged bool
		wantFlagged bool
	}{
		{"a free model flagging the defect", freeReviewModel, usedTokens + reviewEvent("Percent panics: division by zero when whole is 0"), true, false, true},
		{"a defect named in capitals", freeReviewModel, usedTokens + reviewEvent("DIVISION BY ZERO when whole is 0"), true, false, true},
		{"a free model missing the defect", freeReviewModel, usedTokens + reviewEvent("rename part to numerator"), true, false, false},
		{"a clean review", freeReviewModel, usedTokens + reviewEvent(""), true, false, false},
		{"a priced model", pricedReviewModel, usedTokens + reviewEvent(""), true, true, false},
		{"a model with no rate card", unpricedModel, usedTokens + reviewEvent(""), false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := ReviewSmoke(context.Background(), &fakeAgent{events: tt.events}, tt.model, t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if res.Model != tt.model || res.Priced != tt.wantPriced || (res.Cost > 0) != tt.wantCharged || res.Flagged != tt.wantFlagged {
				t.Fatalf("result = %+v", res)
			}
		})
	}
}

func TestReviewSmokeReviewsTheFixtureDiffAgainstItsTicket(t *testing.T) {
	agent := &fakeAgent{events: reviewEvent("")}
	dir := t.TempDir()
	if _, err := ReviewSmoke(context.Background(), agent, freeReviewModel, dir, nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"+++ b/percent.go", "+\treturn part * 100 / whole", "Percent(n, 0) returns 0", "```review-findings"} {
		if !strings.Contains(agent.prompt, want) {
			t.Errorf("the prompt lacks %q:\n%s", want, agent.prompt)
		}
	}
	got, err := os.ReadFile(filepath.Join(dir, "percent.go"))
	if err != nil || !strings.Contains(string(got), "func Percent(part, whole int) int") {
		t.Fatalf("the fixture source is not in the directory: %q, %v", got, err)
	}
}

func TestReviewSmokeReportsAnUnreadableReviewWithItsCost(t *testing.T) {
	res, err := ReviewSmoke(context.Background(), &fakeAgent{events: usedTokens + planEvent("looks fine")}, pricedReviewModel, t.TempDir(), nil)
	if !errors.Is(err, ErrReviewSmokeOutput) {
		t.Fatalf("err = %v, want ErrReviewSmokeOutput", err)
	}
	if res.Text != "looks fine" || res.Cost <= 0 {
		t.Fatalf("result = %+v", res)
	}
}

func TestReviewSmokeRedactsSecretsFromAFailedRun(t *testing.T) {
	agent := &fakeAgent{stderr: "auth failed for key sk-live-123\n", err: errors.New("exit status 1")}
	_, err := ReviewSmoke(context.Background(), agent, freeReviewModel, t.TempDir(), []string{"sk-live-123"})
	if err == nil || errors.Is(err, ErrReviewSmokeOutput) {
		t.Fatalf("err = %v, want an agent failure", err)
	}
	if msg := err.Error(); strings.Contains(msg, "sk-live-123") || !strings.Contains(msg, "auth failed for key [redacted]") ||
		!strings.Contains(msg, "exit status 1") {
		t.Fatalf("err = %q", msg)
	}
}

func TestReviewSmokeReportsATimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	_, err := ReviewSmoke(ctx, &fakeAgent{err: errors.New("signal: killed")}, freeReviewModel, t.TempDir(), nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline", err)
	}
}

func TestReviewSmokeFailsWhenTheDirectoryCannotBeWritten(t *testing.T) {
	if _, err := ReviewSmoke(context.Background(), &fakeAgent{}, freeReviewModel, filepath.Join(t.TempDir(), "absent"), nil); err == nil {
		t.Fatal("ReviewSmoke wrote a fixture into a missing directory")
	}
}

func TestReviewSmokeRunsTheScriptedBinaryUnderTheReviewProfileOnTheGivenModel(t *testing.T) {
	bin, attempts := scriptedCLIAgent(t, "", "command-code/given")
	_, err := ReviewSmoke(context.Background(), scriptedAgent(t, bin, ProfileReview, "command-code/ignored"), "command-code/given", t.TempDir(), nil)
	if !errors.Is(err, ErrReviewSmokeOutput) {
		t.Fatalf("err = %v, want the scripted plan reply read as no review", err)
	}
	if got := attempts(); len(got) != 1 || got[0] != (fallbackAttempt{"command-code/given", "always-ask"}) {
		t.Fatalf("attempts %+v", got)
	}
}

func TestReviewSmokeWorkflowIsManualAndHoldsNoWriteToken(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "review-smoke.yml"))
	if err != nil {
		t.Fatal(err)
	}
	yml := string(b)
	for _, want := range []string{"on:\n  workflow_dispatch:\n", "\npermissions: {}\n", "    permissions:\n      contents: read\n", "persist-credentials: false"} {
		if !strings.Contains(yml, want) {
			t.Errorf("review-smoke.yml has no %q", want)
		}
	}
	for _, banned := range []string{": write", "id-token"} {
		if strings.Contains(yml, banned) {
			t.Errorf("review-smoke.yml carries %q", banned)
		}
	}
}

func TestReviewSmokeCapsTheFindingsButFlagsADefectPastTheCap(t *testing.T) {
	long := strings.Repeat("rename a local; ", 60) + "division by zero"
	res, err := ReviewSmoke(context.Background(), &fakeAgent{events: usedTokens + reviewEvent(long)}, freeReviewModel, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) > 404 || !strings.HasSuffix(res.Findings, "…") {
		t.Errorf("findings are %d bytes, want at most 400 and an ellipsis", len(res.Findings))
	}
	if !res.Flagged {
		t.Error("a defect named past the cap is not flagged")
	}
}
