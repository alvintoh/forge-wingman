package runner

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
)

//go:embed models.json
var modelsJSON []byte

// ModelSet is a provider's models, each read from models.json: the default and
// its ordered same-provider fallbacks, plus the plan and review phases' own
// defaults.
type ModelSet struct {
	Default   string   `json:"default"`
	Fallbacks []string `json:"fallbacks"`
	Plan      string   `json:"plan"`
	Review    string   `json:"review"`
}

var embeddedModels = mustParseModelSet(modelsJSON)

// DefaultModel is every dispatched run's starting model, read from models.json.
func DefaultModel() string { return embeddedModels.Default }

// DefaultPlanModel is the plan phase's model when none is configured, read from
// models.json.
func DefaultPlanModel() string { return embeddedModels.Plan }

// DefaultReviewModel is the review model run.yml's review_models input defaults
// to, used when a ticket names none, read from models.json.
func DefaultReviewModel() string { return embeddedModels.Review }

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
// the default and its fallbacks, which must also be distinct, plus the plan and
// review defaults, which may repeat one of them.
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
	for _, m := range []string{s.Plan, s.Review} {
		switch {
		case !ValidModel(m):
			return fmt.Errorf("model %q is not provider/model", m)
		case Provider(m) != provider:
			return fmt.Errorf("model %q is not a %s model", m, provider)
		}
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
