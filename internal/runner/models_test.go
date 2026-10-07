package runner

import (
	"slices"
	"strings"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/providers"
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
		"malformed plan":   {`{"default":"p/a","fallbacks":[],"plan":"big-pickle","review":["p/r"]}`, "not provider/model"},
		"foreign plan":     {`{"default":"p/a","fallbacks":[],"plan":"q/a","review":["p/r"]}`, "not a p model"},
		"no review":        {`{"default":"p/a","fallbacks":[],"plan":"p/a","review":[]}`, "at least one model"},
		"malformed review": {`{"default":"p/a","fallbacks":[],"plan":"p/a","review":["p/r",""]}`, "not provider/model"},
		"foreign review":   {`{"default":"p/a","fallbacks":[],"plan":"p/a","review":["p/r","q/r"]}`, "not a p model"},
		"repeated review":  {`{"default":"p/a","fallbacks":[],"plan":"p/a","review":["p/r","p/r"]}`, "listed twice"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseModelSet([]byte(tc.doc))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestEmbeddedReviewModelsAreTwoVendors(t *testing.T) {
	review := DefaultReviewModels()
	if len(review) != 2 {
		t.Fatalf("DefaultReviewModels() = %v, want two entries", review)
	}
	vendor := func(m string) string { return strings.Split(m, "/")[1] }
	if vendor(review[0]) == vendor(review[1]) {
		t.Errorf("review models %v share a vendor", review)
	}
}

func TestDefaultReviewModelsReturnsACopy(t *testing.T) {
	first := DefaultReviewModels()
	first[0] = "command-code/mutated"
	if got := DefaultReviewModels(); got[0] == "command-code/mutated" {
		t.Fatalf("DefaultReviewModels shares its backing array: %v", got)
	}
}

func TestFallbackModelsReturnsACopy(t *testing.T) {
	first := fallbackModels(DefaultModel())
	first[0] = "command-code/mutated"
	if got := fallbackModels(DefaultModel()); got[0] == "command-code/mutated" {
		t.Fatalf("fallbackModels shares its backing array: %v", got)
	}
}

// TestDefaultModelProviderHasConfiguredWindows asserts the provider serving the
// default model has its plan windows configured, so the dispatcher meters it
// rather than silently falling back to the cash ceiling.
func TestDefaultModelProviderHasConfiguredWindows(t *testing.T) {
	provider := Provider(DefaultModel())
	if windows := providers.Windows(provider); len(windows) == 0 {
		t.Fatalf("provider %s serving the default model has no configured plan windows", provider)
	}
}
