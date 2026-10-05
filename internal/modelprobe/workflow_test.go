package modelprobe

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// stepScript returns the block-scalar run script of the step named name in a workflow file.
func stepScript(t *testing.T, workflow, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", workflow))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	for i, l := range lines {
		if strings.TrimPrefix(strings.TrimSpace(l), "- ") != "name: "+name {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) != "run: |" {
				continue
			}
			indent := len(lines[j]) - len(strings.TrimLeft(lines[j], " "))
			var script []string
			for _, s := range lines[j+1:] {
				if strings.TrimSpace(s) != "" && len(s)-len(strings.TrimLeft(s, " ")) <= indent {
					break
				}
				script = append(script, strings.TrimPrefix(s, strings.Repeat(" ", indent+2)))
			}
			return strings.Join(script, "\n")
		}
	}
	t.Fatalf("no step %q in %s", name, workflow)
	return ""
}

func runScript(t *testing.T, dir, script string, env ...string) error {
	t.Helper()
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	cmd := exec.Command("bash", "-eo", "pipefail", "-c", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("script output: %s", out)
	}
	return err
}

func TestRunWorkflowRefusesAnEmptyOrNullDefaultModel(t *testing.T) {
	script := stepScript(t, "run.yml", "Resolve the model")
	for name, tc := range map[string]struct {
		models, input string
		wantErr       bool
		wantOutput    string
	}{
		"file default":  {`{"default":"opencode/a"}`, "", false, "model=opencode/a\n"},
		"input wins":    {`{"default":"opencode/a"}`, "opencode/b", false, "model=opencode/b\n"},
		"null default":  {`{"default":null}`, "", true, ""},
		"empty default": {`{"default":""}`, "", true, ""},
		"missing key":   {`{}`, "", true, ""},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "internal", "runner"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "internal", "runner", "models.json"), []byte(tc.models), 0o644); err != nil {
				t.Fatal(err)
			}
			outFile := filepath.Join(dir, "out")
			err := runScript(t, dir, script, "INPUT_MODEL="+tc.input, "GITHUB_OUTPUT="+outFile)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error=%v", err, tc.wantErr)
			}
			if got, _ := os.ReadFile(outFile); string(got) != tc.wantOutput {
				t.Fatalf("output = %q, want %q", got, tc.wantOutput)
			}
		})
	}
}

func TestRunWorkflowAppendsToTheStepOutputFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "internal", "runner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "runner", "models.json"), []byte(`{"default":"opencode/a"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	outFile := filepath.Join(dir, "out")
	if err := os.WriteFile(outFile, []byte("ticket=t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runScript(t, dir, stepScript(t, "run.yml", "Resolve the model"), "INPUT_MODEL=", "GITHUB_OUTPUT="+outFile); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(outFile); string(got) != "ticket=t\nmodel=opencode/a\n" {
		t.Fatalf("output = %q, want the earlier output kept", got)
	}
}

func TestRunWorkflowPassesTheTicketsNamedModelsAheadOfItsDefaults(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "run.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"override_model: ${{ steps.read.outputs.override_model }}",
		"override_review_models: ${{ steps.read.outputs.override_review_models }}",
		"override_plan_models: ${{ steps.read.outputs.override_plan_models }}",
		"model: ${{ needs.ticket.outputs.override_model || needs.ticket.outputs.model }}",
		"review_models: ${{ needs.ticket.outputs.override_review_models || inputs.review_models }}",
		"plan_models: ${{ needs.ticket.outputs.override_plan_models || inputs.plan_models }}",
	} {
		if !strings.Contains(string(b), "\n      "+want+"\n") {
			t.Errorf("run.yml has no line %q", want)
		}
	}
}

func TestRunWorkflowDefaultsTheReviewModelToTheDispatchersConstant(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "run.yml"))
	if err != nil {
		t.Fatal(err)
	}
	yml := string(b)
	i := strings.Index(yml, "review_models:")
	if i < 0 || !strings.HasPrefix(yml[i:][strings.Index(yml[i:], "default:"):], "default: "+runner.DefaultReviewModel+"\n") {
		t.Fatalf("run.yml's review_models input does not default to %s", runner.DefaultReviewModel)
	}
}
