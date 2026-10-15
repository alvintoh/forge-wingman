package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

// The history is a subcollection of the dispatch document named for it, one new
// document per change of the autonomy level.
const (
	historyDocID      = "autonomy-history"
	historyCollection = "changes"
	historyFromField  = "from"
	historyToField    = "to"
	historyCauseField = "cause"
	historyEvidence   = "evidence"
	historyAtField    = "at"
)

// AutonomyHistory is the append-only record of autonomy level changes. It
// exposes Append and List and nothing that updates or deletes a document.
type AutonomyHistory struct {
	client *firestore.Client
}

// NewAutonomyHistory returns an AutonomyHistory backed by client.
func NewAutonomyHistory(client *firestore.Client) *AutonomyHistory {
	return &AutonomyHistory{client: client}
}

func (h *AutonomyHistory) changes() *firestore.CollectionRef {
	return h.client.Collection(dispatchCollection).Doc(historyDocID).Collection(historyCollection)
}

// Append writes c as a new document.
func (h *AutonomyHistory) Append(ctx context.Context, c dispatcher.Change) error {
	_, _, err := h.changes().Add(ctx, map[string]any{
		historyFromField:  int64(c.From),
		historyToField:    int64(c.To),
		historyCauseField: c.Cause,
		historyEvidence:   numbersMap(c.Numbers),
		historyAtField:    c.At,
	})
	if err != nil {
		return fmt.Errorf("appending to the autonomy history: %w", err)
	}
	return nil
}

// List returns up to limit changes, newest first.
func (h *AutonomyHistory) List(ctx context.Context, limit int) ([]dispatcher.Change, error) {
	iter := h.changes().OrderBy(historyAtField, firestore.Desc).Limit(limit).Documents(ctx)
	defer iter.Stop()
	var out []dispatcher.Change
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("listing the autonomy history: %w", err)
		}
		data := snap.Data()
		from, _ := data[historyFromField].(int64)
		to, _ := data[historyToField].(int64)
		c := dispatcher.Change{
			From: dispatcher.Autonomy(from), To: dispatcher.Autonomy(to),
			Cause: str(data, historyCauseField),
		}
		c.At, _ = data[historyAtField].(time.Time)
		if evidence, ok := data[historyEvidence].(map[string]any); ok {
			c.Numbers = numbersFrom(evidence)
		} else {
			c.Numbers = dispatcher.Numbers{NotRecorded: true}
		}
		out = append(out, c)
	}
}
