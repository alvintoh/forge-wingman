package store

import (
	"context"
	"fmt"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

const runsCollection = "runs"

// Records keeps run records in Firestore's runs collection.
type Records struct {
	client *firestore.Client
}

// NewRecords returns a Records backed by client.
func NewRecords(client *firestore.Client) *Records {
	return &Records{client: client}
}

// GetRecord returns the record, or runner.ErrRecordNotFound.
func (r *Records) GetRecord(ctx context.Context, id string) (runner.Record, error) {
	snap, err := r.client.Collection(runsCollection).Doc(id).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return runner.Record{}, fmt.Errorf("%s: %w", id, runner.ErrRecordNotFound)
	}
	if err != nil {
		return runner.Record{}, err
	}
	var rec runner.Record
	if err := snap.DataTo(&rec); err != nil {
		return runner.Record{}, fmt.Errorf("decoding %s: %w", id, err)
	}
	return rec, nil
}

// PutRecord replaces the record.
func (r *Records) PutRecord(ctx context.Context, id string, rec runner.Record) error {
	_, err := r.client.Collection(runsCollection).Doc(id).Set(ctx, rec)
	return err
}
