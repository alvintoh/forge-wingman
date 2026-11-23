package runner

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

//go:embed review_areas.json
var reviewAreasJSON []byte

// defaultReviewAreaModel reviews an area that configures no models of its own.
const defaultReviewAreaModel = "opencode-go/claude-haiku-5-5"

var reviewAreaName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// ReviewArea is one part of a change a specialist reviews, read from
// review_areas.json.
type ReviewArea struct {
	Name string `json:"name"`
	// Paths are the globs of the files that touch the area: `*` and `?` match
	// within one path segment, as path.Match does, and a `**` segment matches
	// any number of whole segments, at least one when it ends the pattern.
	Paths []string `json:"paths"`
	// Prompt is the review lens the area's specialist is given.
	Prompt string `json:"prompt"`
	// Models are the area's reviewers in order. A file that omits the key means
	// one default model; an empty list is refused.
	Models []string `json:"models"`
	// HighWhenRun marks an area whose risk is high whenever its specialist runs.
	// The file must say so for every area.
	HighWhenRun bool `json:"high_when_run"`
}

// reviewAreaFile is ReviewArea as the file writes it, with HighWhenRun a
// pointer so an area that omits it can be told from one that says false.
type reviewAreaFile struct {
	Name        string   `json:"name"`
	Paths       []string `json:"paths"`
	Prompt      string   `json:"prompt"`
	Models      []string `json:"models"`
	HighWhenRun *bool    `json:"high_when_run"`
}

var embeddedReviewAreas = mustParseReviewAreas(reviewAreasJSON)

// TouchedReviewAreas returns the configured review areas a diff touches, in
// configuration order and each once, however many of its files match. A file
// counts by every path it has: the one a diff names for an added, deleted or
// changed file, and both sides of a rename or copy. A diff touching no area
// returns none.
func TouchedReviewAreas(diff string) []ReviewArea {
	return touchedReviewAreas(embeddedReviewAreas, diff)
}

func touchedReviewAreas(areas []ReviewArea, diff string) []ReviewArea {
	paths := diffPaths(diff)
	var touched []ReviewArea
	for _, a := range areas {
		if slices.ContainsFunc(paths, a.matches) {
			touched = append(touched, a.clone())
		}
	}
	return touched
}

func (a ReviewArea) matches(file string) bool {
	return slices.ContainsFunc(a.Paths, func(pattern string) bool { return matchPath(pattern, file) })
}

func (a ReviewArea) clone() ReviewArea {
	a.Paths = slices.Clone(a.Paths)
	a.Models = slices.Clone(a.Models)
	return a
}

// matchPath reports whether file matches the glob pattern; see ReviewArea.Paths.
// Matching is case-sensitive and on whole paths, so `infra/**` does not match
// `infra.md`.
func matchPath(pattern, file string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(file, "/"))
}

func matchSegments(pattern, file []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			if len(pattern) == 1 {
				return len(file) > 0
			}
			for skip := 0; skip <= len(file); skip++ {
				if matchSegments(pattern[1:], file[skip:]) {
					return true
				}
			}
			return false
		}
		if len(file) == 0 {
			return false
		}
		if ok, _ := path.Match(pattern[0], file[0]); !ok {
			return false
		}
		pattern, file = pattern[1:], file[1:]
	}
	return len(file) == 0
}

// parseReviewAreas decodes and validates a review_areas.json document.
//
// Unknown fields are rejected, since the file is this repository's own
// contract, and every error names the area it is about.
func parseReviewAreas(b []byte) ([]ReviewArea, error) {
	var doc struct {
		Areas []json.RawMessage `json:"areas"`
	}
	if err := decodeStrict(b, &doc); err != nil {
		return nil, fmt.Errorf("decoding review areas: %w", err)
	}
	if len(doc.Areas) == 0 {
		return nil, fmt.Errorf("review areas: no area is configured")
	}
	areas := make([]ReviewArea, 0, len(doc.Areas))
	seen := map[string]bool{}
	for i, raw := range doc.Areas {
		// The name is read on its own so an area with an unknown field is still named.
		var named struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(raw, &named)
		label := fmt.Sprintf("review area %q", named.Name)
		if named.Name == "" {
			label = fmt.Sprintf("review area %d", i+1)
		}
		var f reviewAreaFile
		if err := decodeStrict(raw, &f); err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		if f.HighWhenRun == nil {
			return nil, fmt.Errorf("%s: high_when_run is not set", label)
		}
		a := ReviewArea{Name: f.Name, Paths: f.Paths, Prompt: f.Prompt, Models: f.Models, HighWhenRun: *f.HighWhenRun}
		if err := a.validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		if seen[a.Name] {
			return nil, fmt.Errorf("%s: listed twice", label)
		}
		seen[a.Name] = true
		if a.Models == nil {
			a.Models = []string{defaultReviewAreaModel}
		}
		areas = append(areas, a)
	}
	return areas, nil
}

func mustParseReviewAreas(b []byte) []ReviewArea {
	a, err := parseReviewAreas(b)
	if err != nil {
		panic(err)
	}
	return a
}

func decodeStrict(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// validate reports whether the area is well formed. It checks the shape of the
// model list and that a free model has a recorded plan-smoke run, and not
// whether the models exist or share a provider.
func (a ReviewArea) validate() error {
	switch {
	case !reviewAreaName.MatchString(a.Name):
		return fmt.Errorf("name is not lower-case words joined by hyphens")
	case len(a.Paths) == 0:
		return fmt.Errorf("has no paths")
	case strings.TrimSpace(a.Prompt) == "":
		return fmt.Errorf("has an empty prompt")
	case a.Models != nil && len(a.Models) == 0:
		return fmt.Errorf("has an empty model list")
	}
	for _, p := range a.Paths {
		if err := checkPattern(p); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, m := range a.Models {
		switch {
		case !ValidModel(m):
			return fmt.Errorf("model %q is not provider/model", m)
		case seen[m]:
			return fmt.Errorf("model %q is listed twice", m)
		}
		seen[m] = true
	}
	return providers.CheckFreeIDs(a.Models)
}

// checkPattern reports whether pattern is a repository-relative glob with no
// empty segment, no segment path.Match rejects and no `**` beside other text.
func checkPattern(pattern string) error {
	for seg := range strings.SplitSeq(pattern, "/") {
		if seg == "" {
			return fmt.Errorf("path %q has an empty segment", pattern)
		}
		if seg != "**" && strings.Contains(seg, "**") {
			return fmt.Errorf("path %q has a segment with ** that is not the whole segment", pattern)
		}
		if _, err := path.Match(seg, ""); err != nil {
			return fmt.Errorf("path %q is not a glob: %w", pattern, err)
		}
	}
	return nil
}

// ReviewAreas returns the configured review areas, in configuration order.
func ReviewAreas() []ReviewArea {
	areas := make([]ReviewArea, len(embeddedReviewAreas))
	for i, a := range embeddedReviewAreas {
		areas[i] = a.clone()
	}
	return areas
}

// ValidateReviewAreas reports whether every area's model list is a usable review
// list as ValidateReviewModels says it, and also of one provider: the build
// model's, so a specialist never runs on a provider the plan does not hold. The
// error names the area.
func ValidateReviewAreas(areas []ReviewArea, build string) error {
	for _, a := range areas {
		label := fmt.Sprintf("review area %q", a.Name)
		if err := validateModelList(label, a.Models); err != nil {
			return err
		}
		if err := notBuilder(label, a.Models, build); err != nil {
			return err
		}
		for _, m := range a.Models {
			if Provider(m) != Provider(build) {
				return fmt.Errorf("%s model %q is not a %s model", label, m, Provider(build))
			}
		}
	}
	return nil
}
