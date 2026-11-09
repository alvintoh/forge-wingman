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

// testFallbackModel is a backup a test configures beside the default model, on
// the default model's own plan.
const testFallbackModel = "opencode-go/glm-5.3-flash"

// withFallbacks makes models the default model's fallbacks until t ends, so a
// substitution test does not depend on what models.json configures.
func withFallbacks(t *testing.T, models ...string) {
	t.Helper()
	saved := embeddedModels
	embeddedModels.Fallbacks = models
	t.Cleanup(func() { embeddedModels = saved })
}

// TestEmbeddedModelsRunOnOpenCodeGoWithNoFallbacks asserts every default model
// is an OpenCode Go model, and that the default has no fallbacks, since the
// plan serves no free model and a fallback must stay on the default's plan.
func TestEmbeddedModelsRunOnOpenCodeGoWithNoFallbacks(t *testing.T) {
	set, err := parseModelSet(modelsJSON)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range append([]string{set.Default, set.Plan}, set.Review...) {
		if Provider(m) != "opencode-go" {
			t.Errorf("model %s is not an opencode-go model", m)
		}
	}
	if len(set.Fallbacks) != 0 {
		t.Errorf("fallbacks = %v, want none", set.Fallbacks)
	}
}

func TestFallbackModelsAreTheDefaultsOnly(t *testing.T) {
	withFallbacks(t, testFallbackModel)
	if got := fallbackModels(DefaultModel()); !slices.Equal(got, []string{testFallbackModel}) {
		t.Fatalf("fallbackModels(default) = %v, want the configured %s", got, testFallbackModel)
	}
	if got := fallbackModels("opencode-go/other"); got != nil {
		t.Fatalf("fallbackModels(other) = %v, want none", got)
	}
}

func TestParseModelSetRejectsAnInvalidSet(t *testing.T) {
	for name, tc := range map[string]struct{ doc, want string }{
		"mixed providers":  {`{"default":"opencode-go/a","fallbacks":["p/a"]}`, "not a opencode-go model"},
		"malformed name":   {`{"default":"big-pickle","fallbacks":[]}`, "not provider/model"},
		"duplicate":        {`{"default":"p/a","fallbacks":["p/a"]}`, "listed twice"},
		"empty default":    {`{"default":"","fallbacks":[]}`, "not provider/model"},
		"unknown field":    {`{"default":"p/a","fallbacks":[],"extra":1}`, "unknown field"},
		"fallback foreign": {`{"default":"p/a","fallbacks":["opencode-go/b"]}`, "not a p model"},
		"malformed plan":   {`{"default":"p/a","fallbacks":[],"plan":"big-pickle","review":["p/r"]}`, "not provider/model"},
		"foreign plan":     {`{"default":"p/a","fallbacks":[],"plan":"q/a","review":["p/r"]}`, "not a p model"},
		"no review":        {`{"default":"p/a","fallbacks":[],"plan":"p/a","review":[]}`, "at least one model"},
		"malformed review": {`{"default":"p/a","fallbacks":[],"plan":"p/a","review":["p/r",""]}`, "not provider/model"},
		"foreign review":   {`{"default":"p/a","fallbacks":[],"plan":"p/a","review":["p/r","q/r"]}`, "not a p model"},
		"repeated review":  {`{"default":"p/a","fallbacks":[],"plan":"p/a","review":["p/r","p/r"]}`, "listed twice"},
		"a free model with no smoke run": {`{"default":"opencode-go/deepseek-v4.1-flash",` +
			`"fallbacks":["opencode-go/ling-3.0-flash-sante:free"],` +
			`"plan":"opencode-go/deepseek-v4.1-flash","review":["opencode-go/muse-spark-1.3-contributor"]}`,
			"no recorded plan-smoke run"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseModelSet([]byte(tc.doc))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestDefaultReviewModelsReturnsACopy(t *testing.T) {
	first := DefaultReviewModels()
	first[0] = "opencode-go/mutated"
	if got := DefaultReviewModels(); got[0] == "opencode-go/mutated" {
		t.Fatalf("DefaultReviewModels shares its backing array: %v", got)
	}
}

func TestFallbackModelsReturnsACopy(t *testing.T) {
	withFallbacks(t, testFallbackModel)
	first := fallbackModels(DefaultModel())
	first[0] = "opencode-go/mutated"
	if got := fallbackModels(DefaultModel()); got[0] == "opencode-go/mutated" {
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

// TestTheDefaultPlanHasNoFreeRungAndOneReviewModel asserts the default
// model's plan offers no free rung to fall to and the review list is one
// model, so a single vendor. A change to either is deliberate.
func TestTheDefaultPlanHasNoFreeRungAndOneReviewModel(t *testing.T) {
	if rung := providers.LastResortModels(Provider(DefaultModel())); len(rung) != 0 {
		t.Fatalf("the default plan's free rung names %v, want none", rung)
	}
	if review := DefaultReviewModels(); len(review) != 1 {
		t.Fatalf("the review list names %d models (%v), want one", len(review), review)
	}
}
