package runner

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestPlanPromptEndsWithTheFileListInstruction(t *testing.T) {
	projection := "# Rules\n\n" + ticketSentinel + "\n"
	rules, prompt, err := PlanPrompt(projection, testTicket)
	if err != nil {
		t.Fatal(err)
	}
	if rules != "# Rules\n\n" {
		t.Fatalf("rules = %q, want the plan projection's head", rules)
	}
	if !strings.HasPrefix(prompt, testTicket.Text()) ||
		!strings.HasSuffix(prompt, "```plan-files\npath/one.go\npath/one_test.go\npath/two.go\n```") {
		t.Fatalf("prompt = %q", prompt)
	}
}

func TestPlanPromptRejectsAnInvalidProjection(t *testing.T) {
	if _, _, err := PlanPrompt("no sentinel", testTicket); !errors.Is(err, errSentinel) {
		t.Fatalf("err = %v, want errSentinel", err)
	}
}

func TestParsePlanFiles(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{"one file", "plan\n\n```plan-files\ninternal/x.go\n```\n", []string{"internal/x.go"}},
		{"several files, blank lines ignored", "```plan-files\ninternal/x.go\n\ninternal/x_test.go\n```",
			[]string{"internal/x.go", "internal/x_test.go"}},
		{"only the last block counts", "```plan-files\nold.go\n```\n\nmore reasoning\n\n```plan-files\nnew.go\n```",
			[]string{"new.go"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePlanFiles(tt.text)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("files = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParsePlanFilesRejects(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{"no block", "here is my plan, no files listed"},
		{"an empty block", "```plan-files\n\n```"},
		{"an absolute path", "```plan-files\n/etc/passwd\n```"},
		{"a path above the repository", "```plan-files\n../x.go\n```"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parsePlanFiles(tt.text); !errors.Is(err, errPlanFiles) {
				t.Fatalf("err = %v, want errPlanFiles", err)
			}
		})
	}
}

func TestOutOfPlanFiles(t *testing.T) {
	tests := []struct {
		name    string
		edited  []string
		planned []string
		want    []string
	}{
		{"every edit planned", []string{"a.go", "b.go"}, []string{"a.go", "b.go"}, nil},
		{"one edit unplanned", []string{"a.go", "c.go"}, []string{"a.go", "b.go"}, []string{"c.go"}},
		{"nothing planned", []string{"a.go"}, nil, []string{"a.go"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OutOfPlanFiles(tt.edited, tt.planned); !slices.Equal(got, tt.want) {
				t.Fatalf("extra = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWorkflowFiles(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		want  []string
	}{
		{"a workflow among others", []string{"a.go", ".github/workflows/ci.yml"}, []string{".github/workflows/ci.yml"}},
		{"an unclean path", []string{"./.github/x/../workflows/ci.yml"}, []string{"./.github/x/../workflows/ci.yml"}},
		{"the directory itself", []string{".github/workflows/"}, []string{".github/workflows/"}},
		{"near misses", []string{".github/dependabot.yml", "docs/.github/workflows/ci.yml", ".github/workflows.md", ".github/workflows-old/ci.yml"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := workflowFiles(tt.paths); !slices.Equal(got, tt.want) {
				t.Fatalf("workflow files = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestPlanStageResultCheck covers the trusted record job's re-check of the
// model job's plan: the plan travels untrusted, so an unusable one must never
// reach a record write.
func TestPlanStageResultCheck(t *testing.T) {
	sha := strings.Repeat("a", 40)
	tests := []struct {
		name string
		res  PlanStageResult
		sent error
		ok   bool
	}{
		{"a usable plan", PlanStageResult{Files: []string{"a.go", "a_test.go"}, BaseSHA: sha}, nil, true},
		{"no files", PlanStageResult{BaseSHA: sha}, errPlanFiles, false},
		{"an absolute path", PlanStageResult{Files: []string{"/etc/passwd"}, BaseSHA: sha}, errPlanFiles, false},
		{"a path outside the repository", PlanStageResult{Files: []string{"../x.go"}, BaseSHA: sha}, errPlanFiles, false},
		{"a workflow file", PlanStageResult{Files: []string{".github/workflows/ci.yml"}, BaseSHA: sha}, errWorkflowChange, false},
		{"a base that is not a sha", PlanStageResult{Files: []string{"a.go"}, BaseSHA: "main"}, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.res.Check()
			if tt.ok != (err == nil) {
				t.Fatalf("err = %v, want ok %v", err, tt.ok)
			}
			if tt.sent != nil && !errors.Is(err, tt.sent) {
				t.Fatalf("err = %v, want %v", err, tt.sent)
			}
		})
	}
}

func TestParsePlanStageRejectsUnknownFieldsAndTrailingData(t *testing.T) {
	sha := strings.Repeat("a", 40)
	good, err := EncodePlanStage(PlanStageResult{Files: []string{"a.go"}, BaseSHA: sha})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParsePlanStage(good)
	if err != nil || !slices.Equal(got.Files, []string{"a.go"}) || got.BaseSHA != sha {
		t.Fatalf("plan %+v, err %v", got, err)
	}
	for name, raw := range map[string]string{
		"an unknown field": `{"files":["a.go"],"base_sha":"` + sha + `","extra":1}`,
		"trailing data":    string(good) + `{}`,
		"no files":         `{"files":[],"base_sha":"` + sha + `"}`,
	} {
		if _, err := ParsePlanStage([]byte(raw)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}
