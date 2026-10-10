package runner

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestPlanPromptEndsWithTheFileListInstruction(t *testing.T) {
	projection := "# Rules\n\n" + ticketSentinel + "\n"
	rules, prompt, err := PlanPrompt(projection, testTicket, false)
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
	if strings.Contains(prompt, "plan-decision") {
		t.Fatalf("an unguided prompt told the plan it may ask a decision: %q", prompt)
	}
}

// TestGuidedPlanPromptNamesTheOwnerOnlyDecisions covers AC1: a guided plan is
// told which decisions are the owner's to take, and that a local, evidenced,
// reversible choice is not one of them.
func TestGuidedPlanPromptNamesTheOwnerOnlyDecisions(t *testing.T) {
	projection := "# Rules\n\n" + ticketSentinel + "\n"
	_, prompt, err := PlanPrompt(projection, testTicket, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "```plan-decision") || !strings.Contains(prompt, "\"options\"") {
		t.Fatalf("the guided prompt carries no decision instruction: %q", prompt)
	}
	for _, want := range []string{"outward-facing", "irreversible", "security", "scope", "reversible, in-repo decision is yours"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the guided prompt does not name %q", want)
		}
	}
	if !strings.HasSuffix(prompt, "```") {
		t.Fatalf("the guided prompt does not end with the decision block: %q", prompt)
	}
}

func TestPlanPromptRejectsAnInvalidProjection(t *testing.T) {
	if _, _, err := PlanPrompt("no sentinel", testTicket, false); !errors.Is(err, errSentinel) {
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
		{"one decision", PlanStageResult{Decision: &testDecision, BaseSHA: sha}, nil, true},
		{"a decision without a base", PlanStageResult{Decision: &testDecision}, nil, false},
		{"files and a decision", PlanStageResult{Files: []string{"a.go"}, Decision: &testDecision, BaseSHA: sha}, errPlanFiles, false},
		{"an invalid decision", PlanStageResult{Decision: &Decision{Question: "q"}, BaseSHA: sha}, errDecisionInvalid, false},
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

// testDecision is a valid decision the plan-stage tests carry.
var testDecision = Decision{
	Question: "Ship the run behind the existing flag or a new one?",
	Context:  []string{"The flag is read in two places."},
	Options: []DecisionOption{
		{Label: "Reuse the existing flag", Value: "reuse", Cost: "no new config"},
		{Label: "Add a new flag", Value: "new", Cost: "one more env var"},
	},
}

func TestParsePlanArtifacts(t *testing.T) {
	block := "```plan-decision\n" +
		`{"question":"Q?","context":["c1"],"options":[{"label":"A","value":"a","cost":"free"},{"label":"B","value":"b","cost":"$1"}]}` +
		"\n```"
	tests := []struct {
		name     string
		text     string
		guided   bool
		wantFile []string
		wantDec  bool
		sent     error
	}{
		{"files in a guided plan", "```plan-files\ninternal/x.go\n```", true, []string{"internal/x.go"}, false, nil},
		{"a decision in a guided plan", block, true, nil, true, nil},
		{"a decision in an unguided plan", block, false, nil, false, errDecisionInvalid},
		{"both blocks", block + "\n```plan-files\ninternal/x.go\n```", true, nil, false, errPlanFiles},
		{"neither block", "just prose", true, nil, false, errPlanFiles},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files, decision, err := parsePlanArtifacts(tt.text, tt.guided)
			if !errors.Is(err, tt.sent) {
				t.Fatalf("err = %v, want %v", err, tt.sent)
			}
			if tt.sent != nil {
				return
			}
			if !slices.Equal(files, tt.wantFile) || (decision != nil) != tt.wantDec {
				t.Fatalf("files %q, decision %v", files, decision)
			}
		})
	}
}

func TestDecisionCheck(t *testing.T) {
	ok := testDecision
	tests := []struct {
		name string
		d    Decision
		ok   bool
	}{
		{"a valid decision", ok, true},
		{"no question", Decision{Context: ok.Context, Options: ok.Options}, false},
		{"too many context lines", Decision{Question: "Q?", Context: []string{"a", "b", "c", "d"}, Options: ok.Options}, false},
		{"too few options", Decision{Question: "Q?", Options: ok.Options[:1]}, false},
		{"too many options", Decision{Question: "Q?", Options: []DecisionOption{
			ok.Options[0], ok.Options[1], ok.Options[0], ok.Options[1], ok.Options[0]}}, false},
		{"an option with no cost", Decision{Question: "Q?", Options: []DecisionOption{
			ok.Options[0], {Label: "B", Value: "b", Cost: ""}}}, false},
		{"a question with a newline", Decision{Question: "Q?\nsecond", Options: ok.Options}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.d.Check() == nil; got != tt.ok {
				t.Fatalf("Check() ok = %v, want %v", got, tt.ok)
			}
		})
	}
}

func TestDecisionMatch(t *testing.T) {
	d := testDecision
	for _, tc := range []struct {
		name  string
		reply string
		want  string
	}{
		{"the exact label", "Reuse the existing flag", "reuse"},
		{"the label, trimmed and case-folded", "  reuse THE EXISTING flag \n", "reuse"},
		{"the exact value", "new", "new"},
		{"the value, case-folded", "NEW", "new"},
		{"free text matching none", "let's do something else", ""},
		{"empty", "   ", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opt, ok := d.Match(tc.reply)
			if (tc.want == "") == ok {
				t.Fatalf("Match(%q) = %v, %v", tc.reply, opt, ok)
			}
			if ok && opt.Value != tc.want {
				t.Fatalf("Match(%q) = %q, want %q", tc.reply, opt.Value, tc.want)
			}
		})
	}
}

func TestParsePlanStageCarriesADecision(t *testing.T) {
	sha := strings.Repeat("a", 40)
	raw, err := EncodePlanStage(PlanStageResult{Decision: &testDecision, BaseSHA: sha})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParsePlanStage(raw)
	if err != nil || got.Decision == nil || got.Decision.Question != testDecision.Question || len(got.Decision.Options) != 2 {
		t.Fatalf("plan %+v, err %v", got, err)
	}
}
