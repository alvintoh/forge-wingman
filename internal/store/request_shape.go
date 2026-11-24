package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

const (
	// defaultRequestShapeRuns is how many of the newest settled runs the typical
	// requests are read from, whichever stages each recorded, so the read never
	// grows with the run history. It is a default the owner may change.
	defaultRequestShapeRuns = 20
	// minRequestShapeRuns is the fewest usable runs a stage needs before its
	// shape is worth reporting; below it the stage has no shape at all.
	minRequestShapeRuns = 3
)

// requestStages are the stages a request is priced for, in the order they are
// reported. A stage is the run phase of the same name: the fix and fix-review
// phases belong to a pull request's own review loop, not to these three.
var requestStages = []runner.Phase{runner.PhasePlan, runner.PhaseBuild, runner.PhaseReview}

// RequestShape is the typical request of a stage: the tokens one model request
// reads uncached, reads from the provider's cache, and writes (reasoning
// included).
type RequestShape struct {
	Fresh, Cached, Output int64
}

// StageShape is one stage's typical request and how many runs it came from.
// Shape is nil when too few runs recorded the stage to say, and Runs then still
// reports how many did, so a caller shows the gap rather than a default.
type StageShape struct {
	Stage runner.Phase
	Runs  int
	Shape *RequestShape
}

// requestTokens is one run's request of a stage, in whole tokens.
type requestTokens struct {
	fresh, cached, output int64
}

// RequestShapes returns the typical request of the plan, build and review
// stages, in that order: for each, the median request over those of the newest
// defaultRequestShapeRuns settled runs that recorded it. A stage's request is the
// stage's token total over its step count, so a run counts once however many
// rounds the stage took. The plan stage's tokens are recorded only for a run
// that planned inside its own build, so a plan-staged run is not among them.
//
// A row that cannot be decoded fails the read rather than being skipped, as the
// comparison's does. The field mask only trims what each row carries over the
// wire.
func (r *Records) RequestShapes(ctx context.Context) ([]StageShape, error) {
	iter := r.client.Collection(runsCollection).
		Where(settledAtField, ">", time.Time{}).
		OrderBy(settledAtField, firestore.Desc).
		Limit(defaultRequestShapeRuns).
		Select(stepsField).
		Documents(ctx)
	defer iter.Stop()
	var recs []runner.Record
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("listing the settled runs: %w", err)
		}
		var rec runner.Record
		if err := snap.DataTo(&rec); err != nil {
			return nil, fmt.Errorf("decoding run %s: %w", snap.Ref.ID, err)
		}
		recs = append(recs, rec)
	}
	return requestShapes(recs), nil
}

// requestShapes takes each stage's median over the runs that recorded it.
func requestShapes(recs []runner.Record) []StageShape {
	out := make([]StageShape, len(requestStages))
	for i, stage := range requestStages {
		var samples []requestTokens
		for _, rec := range recs {
			if req, usable := stageRequest(rec, stage); usable {
				samples = append(samples, req)
			}
		}
		out[i] = StageShape{Stage: stage, Runs: len(samples), Shape: medianRequest(samples)}
	}
	return out
}

// stageRequest is the request rec's stage made: its tokens over its steps,
// summed across the stage's rounds. A run that took no step of the stage has
// none to report, and neither has one whose stage carries a negative figure.
func stageRequest(rec runner.Record, stage runner.Phase) (requestTokens, bool) {
	var total runner.Usage
	for _, step := range rec.Steps {
		if step.Phase != stage {
			continue
		}
		if slices.ContainsFunc([]int64{step.Tokens.Input, step.Tokens.CacheRead, step.Tokens.Output, int64(step.Tokens.Steps)},
			func(v int64) bool { return v < 0 }) {
			return requestTokens{}, false
		}
		total.Input += step.Tokens.Input
		total.CacheRead += step.Tokens.CacheRead
		total.Output += step.Tokens.Output
		total.Steps += step.Tokens.Steps
	}
	if total.Steps <= 0 {
		return requestTokens{}, false
	}
	n := int64(total.Steps)
	return requestTokens{
		fresh:  wholeTokens(total.Input, n),
		cached: wholeTokens(total.CacheRead, n),
		output: wholeTokens(total.Output, n),
	}, true
}

// wholeTokens is total over n steps, rounded half up to a whole token.
func wholeTokens(total, n int64) int64 { return (2*total + n) / (2 * n) }

// medianRequest is each token kind's median over samples, nil under
// minRequestShapeRuns of them.
func medianRequest(samples []requestTokens) *RequestShape {
	if len(samples) < minRequestShapeRuns {
		return nil
	}
	pick := func(f func(requestTokens) int64) int64 {
		vals := make([]int64, len(samples))
		for i, s := range samples {
			vals[i] = f(s)
		}
		return median(vals)
	}
	return &RequestShape{
		Fresh:  pick(func(s requestTokens) int64 { return s.fresh }),
		Cached: pick(func(s requestTokens) int64 { return s.cached }),
		Output: pick(func(s requestTokens) int64 { return s.output }),
	}
}

// median is the middle of vals: the one middle value of an odd count, the mean
// of the two middle ones of an even count rounded half up to a whole token.
func median(vals []int64) int64 {
	slices.Sort(vals)
	n := len(vals)
	if n%2 == 1 {
		return vals[n/2]
	}
	return wholeTokens(vals[n/2-1]+vals[n/2], 2)
}
