package store

import (
	"context"
	"fmt"
	"reflect"
	"strings"

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
		return runner.Record{}, fmt.Errorf("decoding record %s: %w", id, err)
	}
	return rec, nil
}

// PutRecord replaces each field rec names in the record whole, keeping the fields
// rec does not name and creating the record if it is absent.
func (r *Records) PutRecord(ctx context.Context, id string, rec runner.Record) error {
	data := fields(rec)
	paths := make([]firestore.FieldPath, 0, len(data))
	for name := range data {
		paths = append(paths, firestore.FieldPath{name})
	}
	_, err := r.client.Collection(runsCollection).Doc(id).Set(ctx, data, firestore.Merge(paths...))
	return err
}

// CreateRecord writes the record, failing if one exists.
func (r *Records) CreateRecord(ctx context.Context, id string, rec runner.Record) error {
	_, err := r.client.Collection(runsCollection).Doc(id).Create(ctx, rec)
	return err
}

// fields maps a struct's top-level fields by their firestore names, skipping those
// tagged "-". Tag options such as omitempty are not applied.
func fields(v any) map[string]any {
	rv := reflect.ValueOf(v)
	m := make(map[string]any, rv.NumField())
	for i := range rv.NumField() {
		f := rv.Type().Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("firestore"), ",")
		switch name {
		case "-":
			continue
		case "":
			name = f.Name
		}
		m[name] = rv.Field(i).Interface()
	}
	return m
}
