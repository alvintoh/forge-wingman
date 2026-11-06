package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

const (
	progressListLimit = 200
	progressMaxBytes  = 4 << 10
	progressMaxCount  = 1_000_000
	progressWindow    = 2 * time.Hour
)

// Progress is the newest progress object a running build wrote, every count
// a whole number from 0 to a million.
type Progress struct {
	// Phase is a phase the runner names, or empty.
	Phase runner.Phase
	Steps int64
	// Lines is the diff's lines added plus removed.
	Lines int64
	// Created is when Cloud Storage created the object, never the time its body states.
	Created time.Time
}

// Progresses reads the progress objects running builds write to the
// completions bucket.
type Progresses struct {
	handle *storage.BucketHandle
}

// NewProgresses returns a Progresses for bucket on client.
func NewProgresses(client *storage.Client, bucket string) *Progresses {
	return &Progresses{handle: client.Bucket(bucket)}
}

// Progress reads run's newest progress object by creation time, or nil when
// the run has written none, names no valid run, or wrote one decodeProgress
// rejects. It lists at most 200 objects named in the two hours before now, and
// skips any over 4 KiB.
func (p *Progresses) Progress(ctx context.Context, run Running, now time.Time) (*Progress, error) {
	if !runner.ValidRunID(run.RunID) || run.Attempt < 0 {
		return nil, nil
	}
	newest, err := p.newest(ctx, progressPrefix(run), now)
	if err != nil || newest == nil {
		return nil, err
	}
	r, err := p.handle.Object(newest.Name).NewReader(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", newest.Name, err)
	}
	defer func() { _ = r.Close() }()
	body, err := io.ReadAll(io.LimitReader(r, progressMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", newest.Name, err)
	}
	if len(body) > progressMaxBytes {
		return nil, nil
	}
	return decodeProgress(body, newest.Created), nil
}

// progressPrefix is the folder a run's progress objects sit in, read as the
// build stage when the reservation names none, as the writer does.
func progressPrefix(run Running) string {
	stage := run.Stage
	if stage == "" {
		stage = string(runner.PhaseBuild)
	}
	return "progress/" + run.RunID + "-a" + strconv.Itoa(run.Attempt) + "-" + stage + "/"
}

// newest lists from the first name written within progressWindow of now: names
// lead with zero-padded Unix seconds, so they list oldest first.
func (p *Progresses) newest(ctx context.Context, prefix string, now time.Time) (*storage.ObjectAttrs, error) {
	query := &storage.Query{Prefix: prefix, StartOffset: prefix + fmt.Sprintf("%010d", now.Add(-progressWindow).Unix())}
	if err := query.SetAttrSelection([]string{"Name", "Created", "Size"}); err != nil {
		return nil, fmt.Errorf("selecting progress attributes: %w", err)
	}
	it := p.handle.Objects(ctx, query)
	var newest *storage.ObjectAttrs
	for range progressListLimit {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", prefix, err)
		}
		if attrs.Size > progressMaxBytes {
			continue
		}
		if newest == nil || attrs.Created.After(newest.Created) {
			newest = attrs
		}
	}
	return newest, nil
}

// decodeProgress reads body as a JSON object whose every count is in bounds,
// or nil; an unknown phase is read as empty and keeps the progress.
func decodeProgress(body []byte, created time.Time) *Progress {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil
	}
	steps, okSteps := progressCount(fields["steps"])
	added, okAdded := progressCount(fields["lines_added"])
	removed, okRemoved := progressCount(fields["lines_removed"])
	if !okSteps || !okAdded || !okRemoved || added+removed > progressMaxCount {
		return nil
	}
	out := &Progress{Steps: steps, Lines: added + removed, Created: created}
	var phase runner.Phase
	if json.Unmarshal(fields["phase"], &phase) == nil && runner.KnownPhase(phase) {
		out.Phase = phase
	}
	return out
}

// progressCount is raw as a whole number from 0 to a million, and false for
// anything else, a missing or null value included.
func progressCount(raw json.RawMessage) (int64, bool) {
	var n int64
	if string(raw) == "null" || json.Unmarshal(raw, &n) != nil || n < 0 || n > progressMaxCount {
		return 0, false
	}
	return n, true
}
