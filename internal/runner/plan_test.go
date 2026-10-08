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
			if got := outOfPlanFiles(tt.edited, tt.planned); !slices.Equal(got, tt.want) {
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
