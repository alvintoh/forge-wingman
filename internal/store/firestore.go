package store

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

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

// RunRepository is the repository run id was queued for. The dispatcher writes
// it when it queues the run; the record the run's jobs write has no such field.
func (r *Records) RunRepository(ctx context.Context, id string) (string, error) {
	snap, err := r.client.Collection(runsCollection).Doc(id).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return "", fmt.Errorf("%s: %w", id, runner.ErrRecordNotFound)
	}
	if err != nil {
		return "", err
	}
	return str(snap.Data(), repoField), nil
}

// PutRecord replaces each field rec names in the record whole, keeping the fields
// rec does not name and creating the record if it is absent.
func (r *Records) PutRecord(ctx context.Context, id string, rec runner.Record) error {
	return merge(ctx, r.client.Collection(runsCollection).Doc(id), rec)
}

// SetWaitReason sets only why run id's PR waits for the owner, which the record
// job later carries through Finalize.
func (r *Records) SetWaitReason(ctx context.Context, id string, reason runner.WaitReason) error {
	_, err := r.client.Collection(runsCollection).Doc(id).Update(ctx,
		[]firestore.Update{{Path: waitReasonField, Value: string(reason)}})
	if err != nil {
		return fmt.Errorf("setting run %s's wait reason: %w", id, err)
	}
	return nil
}

// RecordMerge sets when, and as what sha, the owner merged run id's PR, keeping
// every other field.
func (r *Records) RecordMerge(ctx context.Context, id, sha string, at time.Time) error {
	_, err := r.client.Collection(runsCollection).Doc(id).Update(ctx, []firestore.Update{
		{Path: mergedByOwnerAtField, Value: at},
		{Path: mergedSHAField, Value: sha},
	})
	if err != nil {
		return fmt.Errorf("recording run %s's merge: %w", id, err)
	}
	return nil
}

// GetPlan reads the plan stage's fields from run id. A run whose plan stage has
// not run yet reads as the zero PlanRecord.
func (r *Records) GetPlan(ctx context.Context, id string) (runner.PlanRecord, error) {
	snap, err := r.client.Collection(runsCollection).Doc(id).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return runner.PlanRecord{}, fmt.Errorf("%s: %w", id, runner.ErrRecordNotFound)
	}
	if err != nil {
		return runner.PlanRecord{}, err
	}
	var p runner.PlanRecord
	if err := snap.DataTo(&p); err != nil {
		return runner.PlanRecord{}, fmt.Errorf("decoding run %s's plan: %w", id, err)
	}
	return p, nil
}

// WritePlan writes the plan stage's fields onto run id, keeping every field it
// does not name — which is why the plan's data lives outside runner.Record, so
// it survives the record Finalize later rebuilds whole.
func (r *Records) WritePlan(ctx context.Context, id string, p runner.PlanRecord) error {
	return merge(ctx, r.client.Collection(runsCollection).Doc(id), p)
}

// merge replaces each field v names in doc whole, keeping the fields v does not
// name and creating the document if it is absent.
func merge(ctx context.Context, doc *firestore.DocumentRef, v any) error {
	data := fields(v)
	paths := make([]firestore.FieldPath, 0, len(data))
	for name := range data {
		paths = append(paths, firestore.FieldPath{name})
	}
	_, err := doc.Set(ctx, data, firestore.Merge(paths...))
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
