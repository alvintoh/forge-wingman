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

// ModelSet is the default model and its ordered same-provider fallbacks;
// models.json holds the one copy.
type ModelSet struct {
	Default   string   `json:"default"`
	Fallbacks []string `json:"fallbacks"`
}

var embeddedModels = mustParseModelSet(modelsJSON)

// DefaultModel is every dispatched run's starting model, read from models.json.
func DefaultModel() string { return embeddedModels.Default }

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

// Validate reports whether the set names well-formed, distinct models of one
// provider.
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
