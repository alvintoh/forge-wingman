package store

import (
	"context"

	"cloud.google.com/go/firestore"

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

// PutRecord replaces the record.
func (r *Records) PutRecord(ctx context.Context, id string, rec runner.Record) error {
	_, err := r.client.Collection(runsCollection).Doc(id).Set(ctx, rec)
	return err
}
