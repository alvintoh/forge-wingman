package runner

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

//go:embed models.json
var modelsJSON []byte

// ModelSet is a provider's models, each read from models.json: the default and
// its ordered same-provider fallbacks, the plan phase's default, the review
// phase's ordered default list and the findings-only reviewers.
type ModelSet struct {
	Default   string   `json:"default"`
	Fallbacks []string `json:"fallbacks"`
	Plan      string   `json:"plan"`
	Review    []string `json:"review"`
	// FixReview are the models whose findings join the review pass's in the fix
	// rounds without deciding the verdict; empty runs none.
	FixReview []string `json:"fix_review"`
}

var embeddedModels = mustParseModelSet(modelsJSON)

// DefaultModel is every dispatched run's starting model, read from models.json.
func DefaultModel() string { return embeddedModels.Default }

// DefaultPlanModel is the plan phase's model when none is configured, read from
// models.json.
func DefaultPlanModel() string { return embeddedModels.Plan }

// DefaultReviewModels is the ordered review list run.yml's review_models input
// defaults to, used when a ticket names none, read from models.json.
func DefaultReviewModels() []string { return slices.Clone(embeddedModels.Review) }

// DefaultFixReviewModels is the findings-only reviewers a build runs after the
// review pass, read from models.json.
func DefaultFixReviewModels() []string { return slices.Clone(embeddedModels.FixReview) }

// parseModelSet decodes and validates a models.json document.
//
// Unknown fields are rejected, since the file is this repository's own contract.
func parseModelSet(b []byte) (ModelSet, error) {
	var s ModelSet
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return ModelSet{}, fmt.Errorf("decoding models: %w", err)
	}
	if err := s.Validate(); err != nil {
		return ModelSet{}, err
	}
	return s, nil
}

func mustParseModelSet(b []byte) ModelSet {
	s, err := parseModelSet(b)
	if err != nil {
		panic(err)
	}
	return s
}

// Validate reports whether the set names well-formed models of one provider:
// the default and its fallbacks, which must also be distinct, the plan default,
// which may repeat one of them, a non-empty review list with no entry repeated,
// and a fix-review list, which may be empty, with no entry repeated and at least
// one entry neither the default nor a fallback, since a build drops those. A
// free id among them must be one a plan-smoke run has proved.
func (s ModelSet) Validate() error {
	provider := Provider(s.Default)
	seen := map[string]bool{}
	for _, m := range append([]string{s.Default}, s.Fallbacks...) {
		switch {
		case !ValidModel(m):
			return fmt.Errorf("model %q is not provider/model", m)
		case Provider(m) != provider:
			return fmt.Errorf("model %q is not a %s model", m, provider)
		case seen[m]:
			return fmt.Errorf("model %q is listed twice", m)
		}
		seen[m] = true
	}
	if err := validateModelList("review", s.Review); err != nil {
		return err
	}
	if len(s.FixReview) > 0 {
		if err := validateModelList("fix-review", s.FixReview); err != nil {
			return err
		}
	}
	for _, m := range slices.Concat([]string{s.Plan}, s.Review, s.FixReview) {
		switch {
		case !ValidModel(m):
			return fmt.Errorf("model %q is not provider/model", m)
		case Provider(m) != provider:
			return fmt.Errorf("model %q is not a %s model", m, provider)
		}
	}
	if len(s.FixReview) > 0 && !slices.ContainsFunc(s.FixReview, func(m string) bool { return !seen[m] }) {
		return fmt.Errorf("every fix-review model of %q is the default or one of its fallbacks", s.FixReview)
	}
	if err := providers.CheckFreeIDs(slices.Concat([]string{s.Default, s.Plan}, s.Fallbacks, s.Review, s.FixReview)); err != nil {
		return err
	}
	return nil
}

// fallbackModels is the same-provider substitution list for a starting model:
// the ordered fallbacks when it is the default, none otherwise.
func fallbackModels(model string) []string {
	if model != embeddedModels.Default {
		return nil
	}
	return slices.Clone(embeddedModels.Fallbacks)
}
