package runner

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
)

// ZenProvider is the only provider whose free models the default may name.
const ZenProvider = "opencode"

//go:embed models.json
var modelsJSON []byte

// ModelSet is the default model, its ordered same-provider fallbacks and the
// evidence they were chosen on; models.json holds the one copy.
type ModelSet struct {
	Default   string          `json:"default"`
	Fallbacks []string        `json:"fallbacks"`
	ProbedAt  string          `json:"probed_at"`
	Evidence  []ModelEvidence `json:"evidence"`
}

// ModelEvidence is what one surviving model measured in the probe that chose it.
type ModelEvidence struct {
	Model           string `json:"model"`
	ToolCalls       int    `json:"tool_calls"`
	TranscriptBytes int    `json:"transcript_bytes"`
}

var embeddedModels = mustParseModelSet(modelsJSON)

// DefaultModel is every dispatched run's starting model, read from models.json.
func DefaultModel() string { return embeddedModels.Default }

// ParseModelSet decodes and validates a models.json document.
//
// Unknown fields are rejected, since the file is this repository's own contract.
func ParseModelSet(b []byte) (ModelSet, error) {
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
	s, err := ParseModelSet(b)
	if err != nil {
		panic(err)
	}
	return s
}

// Validate reports whether the set names only well-formed, distinct Zen models.
func (s ModelSet) Validate() error {
	seen := map[string]bool{}
	for _, m := range append([]string{s.Default}, s.Fallbacks...) {
		switch {
		case !modelPattern.MatchString(m):
			return fmt.Errorf("model %q is not provider/model", m)
		case Provider(m) != ZenProvider:
			return fmt.Errorf("model %q is not a %s model", m, ZenProvider)
		case seen[m]:
			return fmt.Errorf("model %q is listed twice", m)
		}
		seen[m] = true
	}
	return nil
}

// Marshal renders the set as the indented, newline-terminated form models.json is kept in.
func (s ModelSet) Marshal() ([]byte, error) {
	if s.Fallbacks == nil {
		s.Fallbacks = []string{}
	}
	if s.Evidence == nil {
		s.Evidence = []ModelEvidence{}
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding models: %w", err)
	}
	return append(b, '\n'), nil
}

// fallbackModels is the same-provider substitution list for a starting model:
// the ordered fallbacks when it is the default, none otherwise.
func fallbackModels(model string) []string {
	if model != embeddedModels.Default {
		return nil
	}
	return slices.Clone(embeddedModels.Fallbacks)
}
