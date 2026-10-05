package runner

import (
	"slices"
	"strings"
	"testing"
)

func TestEmbeddedModelsNameTheDefault(t *testing.T) {
	set, err := parseModelSet(modelsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if set.Default == "" || DefaultModel() != set.Default {
		t.Fatalf("DefaultModel() = %q, want the model models.json names (%q)", DefaultModel(), set.Default)
	}
}

func TestFallbackModelsAreTheDefaultsOnly(t *testing.T) {
	set, err := parseModelSet(modelsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if got := fallbackModels(DefaultModel()); !slices.Equal(got, set.Fallbacks) {
		t.Fatalf("fallbackModels(default) = %v, want models.json's %v", got, set.Fallbacks)
	}
	if got := fallbackModels("command-code/other"); got != nil {
		t.Fatalf("fallbackModels(other) = %v, want none", got)
	}
}

func TestParseModelSetRejectsAnInvalidSet(t *testing.T) {
	for name, tc := range map[string]struct{ doc, want string }{
		"mixed providers":  {`{"default":"command-code/a","fallbacks":["p/a"]}`, "not a command-code model"},
		"malformed name":   {`{"default":"big-pickle","fallbacks":[]}`, "not provider/model"},
		"duplicate":        {`{"default":"p/a","fallbacks":["p/a"]}`, "listed twice"},
		"empty default":    {`{"default":"","fallbacks":[]}`, "not provider/model"},
		"unknown field":    {`{"default":"p/a","fallbacks":[],"extra":1}`, "unknown field"},
		"fallback foreign": {`{"default":"p/a","fallbacks":["command-code/b"]}`, "not a p model"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseModelSet([]byte(tc.doc))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestFallbackModelsReturnsACopy(t *testing.T) {
	first := fallbackModels(DefaultModel())
	first[0] = "command-code/mutated"
	if got := fallbackModels(DefaultModel()); got[0] == "command-code/mutated" {
		t.Fatalf("fallbackModels shares its backing array: %v", got)
	}
}
