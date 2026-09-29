package runner

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

func TestEmbeddedModelsAreCanonicalAndNameTheDefault(t *testing.T) {
	got, err := embeddedModels.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, modelsJSON) {
		t.Fatalf("models.json is not in Marshal's form:\n%s", got)
	}
	if DefaultModel() != "opencode/big-pickle" {
		t.Fatalf("DefaultModel() = %q, want the model models.json names", DefaultModel())
	}
}

func TestFallbackModelsAreTheDefaultsOnly(t *testing.T) {
	want := []string{"opencode/mimo-v2.6-flash-free"}
	if got := fallbackModels(DefaultModel()); !slices.Equal(got, want) {
		t.Fatalf("fallbackModels(default) = %v, want %v", got, want)
	}
	if got := fallbackModels("opencode/other"); got != nil {
		t.Fatalf("fallbackModels(other) = %v, want none", got)
	}
}

func TestParseModelSetRejectsAnInvalidSet(t *testing.T) {
	for name, tc := range map[string]struct{ doc, want string }{
		"other provider":   {`{"default":"opencode-go/glm-5.3-flash","fallbacks":[]}`, "not a opencode model"},
		"malformed name":   {`{"default":"big-pickle","fallbacks":[]}`, "not provider/model"},
		"duplicate":        {`{"default":"opencode/a","fallbacks":["opencode/a"]}`, "listed twice"},
		"empty default":    {`{"default":"","fallbacks":[]}`, "not provider/model"},
		"unknown field":    {`{"default":"opencode/a","fallbacks":[],"extra":1}`, "unknown field"},
		"fallback foreign": {`{"default":"opencode/a","fallbacks":["opencode-go/b"]}`, "not a opencode model"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseModelSet([]byte(tc.doc))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestModelSetMarshalRoundTrips(t *testing.T) {
	in := ModelSet{
		Default:   "opencode/a",
		Fallbacks: []string{"opencode/b"},
		ProbedAt:  "2026-09-29",
		Evidence:  []ModelEvidence{{Model: "opencode/a", ToolCalls: 7, TranscriptBytes: 900}},
	}
	b, err := in.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	out, err := ParseModelSet(b)
	if err != nil {
		t.Fatal(err)
	}
	if out.Default != in.Default || !slices.Equal(out.Fallbacks, in.Fallbacks) || out.Evidence[0] != in.Evidence[0] {
		t.Fatalf("round trip = %+v, want %+v", out, in)
	}
}

func TestModelSetMarshalWritesEmptyListsNotNull(t *testing.T) {
	b, err := ModelSet{Default: "opencode/a"}.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"fallbacks": []`, `"evidence": []`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("Marshal = %s, want it to contain %s", b, want)
		}
	}
}

func TestFallbackModelsReturnsACopy(t *testing.T) {
	first := fallbackModels(DefaultModel())
	first[0] = "opencode/mutated"
	if got := fallbackModels(DefaultModel()); got[0] == "opencode/mutated" {
		t.Fatalf("fallbackModels shares its backing array: %v", got)
	}
}
