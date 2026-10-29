package store

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

const (
	prHeadSHAField    = "pr_head_sha"
	checksResultField = "checks_result"
)

// Checks reads the runs whose checks have not settled and records the result
// that settles one.
type Checks struct {
	client *firestore.Client
}

// NewChecks returns a Checks backed by client.
func NewChecks(client *firestore.Client) *Checks {
	return &Checks{client: client}
}

// UnsettledRuns returns up to limit runs whose latest attempt opened a pull
// request and which have no checks result yet, oldest start first. Firestore
// cannot query an absent field, so the rows with a PR are read and the others
// dropped here. A run's start falls back to its queue stamp, and is zero when it
// has neither.
func (c *Checks) UnsettledRuns(ctx context.Context, limit int) ([]dispatcher.UnsettledRun, error) {
	iter := c.client.Collection(runsCollection).
		Where(prURLField, ">", "").
		Select(prURLField, checksResultField, outcomeField, startedAtField, queuedAtField).
		Documents(ctx)
	defer iter.Stop()
	var out []dispatcher.UnsettledRun
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("listing the runs with unsettled checks: %w", err)
		}
		row := snap.Data()
		if str(row, checksResultField) != "" || str(row, outcomeField) != string(runner.OutcomePROpened) {
			continue
		}
		run := dispatcher.UnsettledRun{ID: snap.Ref.ID, PRURL: str(row, prURLField)}
		if run.StartedAt, _ = row[startedAtField].(time.Time); run.StartedAt.IsZero() {
			run.StartedAt, _ = row[queuedAtField].(time.Time)
		}
		out = append(out, run)
	}
	slices.SortFunc(out, func(a, b dispatcher.UnsettledRun) int {
		return cmp.Or(a.StartedAt.Compare(b.StartedAt), strings.Compare(a.ID, b.ID))
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// RecordChecks writes the checks result onto run id's row, with the agent's
// head sha when there is one, and reports whether it did. A row is left as it
// is when it already carries a result, or when it no longer holds the pull
// request the result was read for: a requeue clears the outcome and a retry
// replaces the PR. An absent run is runner.ErrRecordNotFound.
func (c *Checks) RecordChecks(ctx context.Context, id, prURL, sha, result string) (bool, error) {
	ref := c.client.Collection(runsCollection).Doc(id)
	recorded := false
	err := c.client.RunTransaction(ctx, func(_ context.Context, tx *firestore.Transaction) error {
		recorded = false
		snap, err := tx.Get(ref)
		if status.Code(err) == codes.NotFound {
			return fmt.Errorf("%s: %w", id, runner.ErrRecordNotFound)
		}
		if err != nil {
			return err
		}
		row := snap.Data()
		if str(row, checksResultField) != "" || str(row, prURLField) != prURL || str(row, outcomeField) != string(runner.OutcomePROpened) {
			return nil
		}
		recorded = true
		updates := []firestore.Update{{Path: checksResultField, Value: result}}
		if sha != "" {
			updates = append(updates, firestore.Update{Path: prHeadSHAField, Value: sha})
		}
		return tx.Update(ref, updates)
	})
	if err != nil {
		return false, fmt.Errorf("recording run %s's checks result: %w", id, err)
	}
	return recorded, nil
}
