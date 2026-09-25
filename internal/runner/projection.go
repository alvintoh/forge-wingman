package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
)

const DefaultPointer = "projections/current"

// buildProjectionFile is the build projection's name in tools/projection/gen_projection.py.
const buildProjectionFile = "projection-build.md"

// ErrObjectNotFound is what an ObjectReader returns for an absent object.
var ErrObjectNotFound = errors.New("object not found")

// ErrPointerInvalid reports a pointer whose content is not a commit sha.
var ErrPointerInvalid = errors.New("projection pointer is not a commit sha")

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ObjectReader reads whole objects from the projections bucket.
type ObjectReader interface {
	ReadObject(ctx context.Context, name string) ([]byte, error)
}

// MissingError names the projection object that could not be found.
type MissingError struct {
	Object string
}

func (e *MissingError) Error() string {
	return "projection object missing: " + e.Object
}

// Projection is the build prompt published for one rule-stack sha.
type Projection struct {
	SHA  string
	Text string
}

// FetchBuildProjection resolves the pointer, then reads the build projection it names.
func FetchBuildProjection(ctx context.Context, r ObjectReader, pointer string) (Projection, error) {
	raw, err := readRequired(ctx, r, pointer)
	if err != nil {
		return Projection{}, err
	}
	sha := string(bytes.TrimSpace(raw))
	if !shaPattern.MatchString(sha) {
		return Projection{}, fmt.Errorf("%s: %w", pointer, ErrPointerInvalid)
	}
	text, err := readRequired(ctx, r, "projections/"+sha+"/"+buildProjectionFile)
	if err != nil {
		return Projection{}, err
	}
	return Projection{SHA: sha, Text: string(text)}, nil
}

func readRequired(ctx context.Context, r ObjectReader, name string) ([]byte, error) {
	b, err := r.ReadObject(ctx, name)
	if errors.Is(err, ErrObjectNotFound) {
		return nil, &MissingError{Object: name}
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", name, err)
	}
	return b, nil
}
