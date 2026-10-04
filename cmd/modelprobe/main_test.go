package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/modelprobe"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

const fakeCLIAgent = `#!/bin/sh
if [ "$1" = models ]; then
cat <<'EOF'
opencode/big-pickle
{
  "cost": {"input": 0, "output": 0}
}
opencode/quick-free
{
  "cost": {"input": 0, "output": 0}
}
opencode/paid
{
  "cost": {"input": 1, "output": 1}
}
EOF
exit 0
fi
while [ $# -gt 0 ]; do [ "$1" = -m ] && model="$2"; [ "$1" = --agent ] && agent="$2"; shift; done
if [ -n "$agent" ]; then
echo '{"type":"step_finish","part":{"tokens":{"input":1,"output":1},"cost":0}}'
printf '%s\n' '{"type":"text","part":{"text":"\u0060\u0060\u0060review-findings\n\u0060\u0060\u0060"}}'
exit 0
fi
printf '\n// TODO: quota unverified\n' >> internal/collect/collector.go
n=10
[ "$model" = opencode/quick-free ] && n=2
i=0
while [ $i -lt $n ]; do echo '{"type":"tool_use"}'; i=$((i+1)); done
`

func setup(t *testing.T) (bin, models, results string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "opencode")
	if err := os.WriteFile(bin, []byte(fakeCLIAgent), 0o755); err != nil {
		t.Fatal(err)
	}
	models = filepath.Join(dir, "models.json")
	if err := os.WriteFile(models, []byte(`{"default":"opencode/big-pickle","fallbacks":[],"probed_at":"","evidence":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return bin, models, filepath.Join(dir, "results")
}

func args(bin, models, results string, extra ...string) []string {
	return append([]string{"-repo", "../..", "-agent-bin", bin, "-models", models, "-results", results}, extra...)
}

func TestRunFlipsTheDefaultToAClearWinnerAndWritesTheResults(t *testing.T) {
	bin, models, results := setup(t)
	outputs := filepath.Join(t.TempDir(), "github_output")
	now := func() time.Time { return time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC) }
	err := run(context.Background(), quietLogger(), args(bin, models, results, "-outputs", outputs, "-run-id", "77"), now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(models)
	if err != nil {
		t.Fatal(err)
	}
	set, err := runner.ParseModelSet(b)
	if err != nil {
		t.Fatal(err)
	}
	if set.Default != "opencode/quick-free" || set.ProbedAt != "2026-10-01" || len(set.Fallbacks) != 1 || set.Fallbacks[0] != "opencode/big-pickle" {
		t.Fatalf("models = %+v, want the two-call model as default and the incumbent as its fallback", set)
	}
	path := filepath.Join(results, "2026-10-01-77.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("results file: %v", err)
	}
	out, _ := os.ReadFile(outputs)
	want := "results=" + path + "\nchanged=true\nfree=[\"opencode/big-pickle\",\"opencode/quick-free\"]\n"
	if string(out) != want {
		t.Fatalf("outputs = %q, want %q", out, want)
	}
}

func TestRunTwiceOnOneDateKeepsBothResultsFiles(t *testing.T) {
	bin, models, results := setup(t)
	now := func() time.Time { return time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC) }
	for _, id := range []string{"1", "2"} {
		if err := run(context.Background(), quietLogger(), args(bin, models, results, "-only", "opencode/quick-free", "-run-id", id), now); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := filepath.Glob(filepath.Join(results, "*.json"))
	if len(files) != 2 {
		t.Fatalf("results files = %v, want one per run", files)
	}
	if err := run(context.Background(), quietLogger(), args(bin, models, results, "-run-id", "../x"), now); err == nil {
		t.Fatal("run accepted a run id that is not alphanumeric")
	}
}

func TestVerifyChecksOutputsAndRendersTheSummaryFromThem(t *testing.T) {
	bin, models, results := setup(t)
	now := func() time.Time { return time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC) }
	outputs := filepath.Join(t.TempDir(), "github_output")
	if err := run(context.Background(), quietLogger(), args(bin, models, results, "-outputs", outputs, "-run-id", "5"), now); err != nil {
		t.Fatal(err)
	}
	summary := filepath.Join(t.TempDir(), "summary.md")
	verify := func(free string) error {
		return run(context.Background(), quietLogger(),
			[]string{"-verify", filepath.Join(results, "2026-10-01-5.json"), "-models", models, "-free", free, "-summary", summary}, now)
	}
	if err := verify(`["opencode/big-pickle","opencode/quick-free"]`); err != nil {
		t.Fatal(err)
	}
	if s, _ := os.ReadFile(summary); !strings.Contains(string(s), "`opencode/quick-free` (challenger-beats-margin)") {
		t.Fatalf("summary = %q", s)
	}
	if err := verify(`["opencode/big-pickle"]`); err == nil {
		t.Fatal("verify accepted a default that is not on the free roster")
	}
}

func TestRunWithOnlyLeavesTheModelsFileAlone(t *testing.T) {
	bin, models, results := setup(t)
	before, _ := os.ReadFile(models)
	err := run(context.Background(), quietLogger(), args(bin, models, results, "-only", "opencode/quick-free"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(models); string(after) != string(before) {
		t.Fatalf("models file changed by a partial probe:\n%s", after)
	}
}

func TestRunRefusesAModelOutsideTheRoster(t *testing.T) {
	bin, models, results := setup(t)
	for _, only := range []string{"opencode/paid", "opencode-go/glm-5.3-flash"} {
		if err := run(context.Background(), quietLogger(), args(bin, models, results, "-only", only), time.Now); err == nil {
			t.Fatalf("run accepted -only %s", only)
		}
	}
}

func quietLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

func fixedNow() time.Time { return time.Date(2026, 10, 1, 9, 5, 3, 0, time.UTC) }

func TestRunLeavesTheModelsFileUntouchedWhenTheDecisionChangesNothing(t *testing.T) {
	bin, models, results := setup(t)
	tie := strings.Replace(fakeCLIAgent, `[ "$model" = opencode/quick-free ] && n=2`, ":", 1)
	if err := os.WriteFile(bin, []byte(tie), 0o755); err != nil {
		t.Fatal(err)
	}
	unchanged := `{"default":"opencode/big-pickle","fallbacks":["opencode/quick-free"],"probed_at":"old","evidence":[]}`
	if err := os.WriteFile(models, []byte(unchanged), 0o644); err != nil {
		t.Fatal(err)
	}
	outputs := filepath.Join(t.TempDir(), "github_output")
	if err := run(context.Background(), quietLogger(), args(bin, models, results, "-outputs", outputs, "-run-id", "1"), fixedNow); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(models); string(after) != unchanged {
		t.Fatalf("models file rewritten although the decision changed nothing:\n%s", after)
	}
	if out, _ := os.ReadFile(outputs); !strings.Contains(string(out), "changed=false\n") {
		t.Fatalf("outputs = %q, want changed=false", out)
	}
}

func TestRunRefusesPositionalArguments(t *testing.T) {
	bin, models, results := setup(t)
	if err := run(context.Background(), quietLogger(), args(bin, models, results, "stray"), fixedNow); err == nil {
		t.Fatal("run accepted a positional argument")
	}
}

func TestRunNamesTheResultsFileByTheTimeOfDayWithoutARunID(t *testing.T) {
	bin, models, results := setup(t)
	if err := run(context.Background(), quietLogger(), args(bin, models, results, "-only", "opencode/quick-free"), fixedNow); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(results, "2026-10-01-090503.json")); err != nil {
		t.Fatalf("results file: %v", err)
	}
}

func TestRunProbesTheModelsFilesDefaultFirst(t *testing.T) {
	bin, models, results := setup(t)
	if err := os.WriteFile(models, []byte(`{"default":"opencode/quick-free","fallbacks":[],"probed_at":"","evidence":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), quietLogger(), args(bin, models, results, "-run-id", "1"), fixedNow); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(results, "2026-10-01-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	report, err := modelprobe.ReadReport(b)
	if err != nil {
		t.Fatal(err)
	}
	if report.Results[0].Model != "opencode/quick-free" {
		t.Fatalf("results = %+v, want the file's default probed first", report.Results)
	}
}

func TestRunHonoursTheMarginFlag(t *testing.T) {
	bin, models, results := setup(t)
	if err := run(context.Background(), quietLogger(), args(bin, models, results, "-margin", "0.1", "-run-id", "1"), fixedNow); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(models)
	set, err := runner.ParseModelSet(b)
	if err != nil {
		t.Fatal(err)
	}
	if set.Default != "opencode/big-pickle" {
		t.Fatalf("default = %s, want the incumbent kept when the challenger misses a 0.1 margin", set.Default)
	}
}

func TestVerifyWithoutASummaryPathWritesNothing(t *testing.T) {
	bin, models, results := setup(t)
	if err := run(context.Background(), quietLogger(), args(bin, models, results, "-run-id", "5"), fixedNow); err != nil {
		t.Fatal(err)
	}
	err := run(context.Background(), quietLogger(),
		[]string{"-verify", filepath.Join(results, "2026-10-01-5.json"), "-models", models, "-free", `["opencode/big-pickle","opencode/quick-free"]`}, fixedNow)
	if err != nil {
		t.Fatalf("verify without -summary = %v", err)
	}
}

func TestRunAppendsToAnExistingOutputsFile(t *testing.T) {
	bin, models, results := setup(t)
	outputs := filepath.Join(t.TempDir(), "github_output")
	if err := os.WriteFile(outputs, []byte("earlier=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), quietLogger(), args(bin, models, results, "-outputs", outputs, "-run-id", "1"), fixedNow); err != nil {
		t.Fatal(err)
	}
	if out, _ := os.ReadFile(outputs); !strings.HasPrefix(string(out), "earlier=1\nresults=") {
		t.Fatalf("outputs = %q, want the earlier lines kept", out)
	}
}

func TestRunDatesTheResultsFileInUTC(t *testing.T) {
	bin, models, results := setup(t)
	late := func() time.Time { return time.Date(2026, 10, 1, 23, 30, 0, 0, time.FixedZone("west", -5*3600)) }
	if err := run(context.Background(), quietLogger(), args(bin, models, results, "-only", "opencode/quick-free", "-run-id", "1"), late); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(results, "2026-10-02-1.json")); err != nil {
		t.Fatalf("results file: %v", err)
	}
}

func TestRunFailsButKeepsTheReportWhenEveryRunIsVoid(t *testing.T) {
	bin, models, results := setup(t)
	rejected := strings.Replace(fakeCLIAgent, "while [ $# -gt 0 ]", `echo '{"type":"error","error":{"name":"APIError","data":{"statusCode":401}}}'; exit 1
while [ $# -gt 0 ]`, 1)
	if err := os.WriteFile(bin, []byte(rejected), 0o755); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(models)
	outputs := filepath.Join(t.TempDir(), "output")
	err := run(context.Background(), quietLogger(), args(bin, models, results, "-outputs", outputs), time.Now)
	if !errors.Is(err, modelprobe.ErrNoEvidence) {
		t.Fatalf("err = %v, want ErrNoEvidence", err)
	}
	if files, _ := os.ReadDir(results); len(files) != 1 {
		t.Fatalf("results files = %d, want the report kept", len(files))
	}
	if out, _ := os.ReadFile(outputs); !strings.Contains(string(out), "results=") {
		t.Fatalf("outputs = %q, want results= so the workflow can stage the report", out)
	}
	if after, _ := os.ReadFile(models); string(after) != string(before) {
		t.Fatalf("models file changed by a probe that proved nothing:\n%s", after)
	}
}

func TestRunRecordsTheReviewShapeVerdictBesideTheBuildVerdict(t *testing.T) {
	bin, models, results := setup(t)
	if err := run(context.Background(), quietLogger(), args(bin, models, results, "-only", "opencode/quick-free", "-run-id", "1"), fixedNow); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(results, "2026-10-01-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	report, err := modelprobe.ReadReport(b)
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Results[0]; got.Verdict != modelprobe.VerdictPass || got.ReviewVerdict != modelprobe.VerdictPass {
		t.Fatalf("result = %+v, want a build pass and a review-shape pass", got)
	}
}
