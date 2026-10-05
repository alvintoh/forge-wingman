package runner

import (
	"errors"
	"strings"
	"testing"
)

func TestParseCommitMessage(t *testing.T) {
	block := func(content string) string { return "Done.\n\n```commit-message\n" + content + "\n```\n" }
	tests := []struct {
		name string
		text string
		want CommitMessage
	}{
		{"a valid block", block("feat(runner): ABC-1 add the widget\n\nAdds the widget.\n\n- add widget.go\n- test it"),
			CommitMessage{Subject: "feat(runner): ABC-1 add the widget", Summary: "Adds the widget.", Body: "- add widget.go\n- test it"}},
		{"the last block wins", block("feat: ABC-1 old\n\nOld.\n\n- old") + block("fix: ABC-1 new\n\nNew.\n\n- new"),
			CommitMessage{Subject: "fix: ABC-1 new", Summary: "New.", Body: "- new"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCommitMessage(tt.text, "ABC-1")
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseCommitMessageRejects(t *testing.T) {
	block := func(content string) string { return "```commit-message\n" + content + "\n```" }
	tests := []struct {
		name string
		text string
	}{
		{"no block", "Done, nothing more to say."},
		{"a wrong prefix", block("Add the widget ABC-1\n\nAdds it.\n\n- add it")},
		{"a missing ticket id", block("feat: add the widget\n\nAdds it.\n\n- add it")},
		{"a subject over 72 characters", block("feat: ABC-1 " + strings.Repeat("x", 61) + "\n\nAdds it.\n\n- add it")},
		{"no bullets", block("feat: ABC-1 add the widget\n\nAdds it.\n\nAnd more prose.")},
		{"no summary", block("feat: ABC-1 add the widget\n\n- add it")},
		{"a bullet where the summary goes", block("feat: ABC-1 add the widget\n\n- add it\n\n- and this")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseCommitMessage(tt.text, "ABC-1"); !errors.Is(err, errCommitMessage) {
				t.Fatalf("err = %v, want errCommitMessage", err)
			}
		})
	}
}

func TestParseCommitMessageAcceptsExactly72Characters(t *testing.T) {
	subject := "feat: ABC-1 " + strings.Repeat("x", 60)
	if _, err := ParseCommitMessage("```commit-message\n"+subject+"\n\nAdds it.\n\n- add it\n```", "ABC-1"); err != nil {
		t.Fatal(err)
	}
}

func TestFallbackCommitMessage(t *testing.T) {
	tests := []struct {
		name        string
		title       string
		files       []string
		wantSubject string
		wantSummary string
	}{
		{"a BE ticket", "[BE] Add the widget", nil, "feat: ABC-1 add the widget", "Add the widget"},
		{"an FE ticket", "[FE] Add the widget", nil, "feat: ABC-1 add the widget", "Add the widget"},
		{"a SLICE ticket", "[SLICE] Add the widget", nil, "feat: ABC-1 add the widget", "Add the widget"},
		{"an INFRA ticket", "[INFRA] Add the bucket", nil, "chore: ABC-1 add the bucket", "Add the bucket"},
		{"an OPS ticket", "[OPS] Rotate the key", nil, "chore: ABC-1 rotate the key", "Rotate the key"},
		{"no tag", "Add the widget", nil, "feat: ABC-1 add the widget", "Add the widget"},
		{"a conventional title keeps its form", "fix(x): bound it", nil, "fix(x): ABC-1 bound it", "fix(x): bound it"},
		{"the package most files touch", "[BE] Add it",
			[]string{"internal/runner/a.go", "internal/runner/b.go", "cmd/runner/main.go"}, "feat(runner): ABC-1 add it", "Add it"},
		{"the majority across trees", "[BE] Add it",
			[]string{"internal/money/a.go", "cmd/surface/a.go", "cmd/surface/b.go"}, "feat(surface): ABC-1 add it", "Add it"},
		{"a tie goes to the alphabetically first", "[BE] Add it",
			[]string{"internal/runner/a.go", "internal/money/a.go"}, "feat(money): ABC-1 add it", "Add it"},
		{"no package", "[BE] Add it", []string{"README.md", "internal/doc.go", "docs/x/y.md"}, "feat: ABC-1 add it", "Add it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tk := Ticket{ID: "ABC-1", Title: tt.title, Size: "S", Body: "The body."}
			got := FallbackCommitMessage(tk, tt.files)
			if got.Subject != tt.wantSubject || got.Summary != tt.wantSummary || got.Body != "The body." {
				t.Fatalf("got %+v, want subject %q, summary %q, the ticket's body", got, tt.wantSubject, tt.wantSummary)
			}
		})
	}
}
