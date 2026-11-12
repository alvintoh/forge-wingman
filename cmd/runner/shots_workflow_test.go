package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func shotsJob(t *testing.T) string {
	t.Helper()
	yml, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "run.yml"))
	if err != nil {
		t.Fatal(err)
	}
	job, _, _ := strings.Cut(string(yml), "\n  pr:\n")
	_, job, _ = strings.Cut(job, "\n  shots:\n")
	return job
}

func TestShotsJobPublishesOnlyACaptureThatRanToItsEnd(t *testing.T) {
	job := shotsJob(t)
	for _, want := range []string{
		`[[ ! -f "$RUNNER_TEMP/shots/checks.json" ]] || echo "complete=true" >> "$GITHUB_OUTPUT"`,
		"if: ${{ !cancelled() && steps.complete.outputs.complete == 'true' }}",
		"if: ${{ !cancelled() && steps.upload.outcome == 'success' }}",
	} {
		if !strings.Contains(job, want) {
			t.Errorf("run.yml's shots job lacks %q", want)
		}
	}
}

func TestShotsJobSummaryIndentsTheFailureLinesInsteadOfFencingThem(t *testing.T) {
	job := shotsJob(t)
	if strings.Contains(job, "```") {
		t.Error("run.yml's shots job fences the failure lines; a backtick run in one would close the fence")
	}
	if !strings.Contains(job, `| sed 's/^/    /'`) {
		t.Error("run.yml's shots job does not indent the failure lines in the step summary")
	}
	if !strings.Contains(job, `echo "::error::the screenshot capture failed; see the shotsFailed lines"`) {
		t.Error("run.yml's shots job does not annotate the failure with a fixed message")
	}
}
