package store

import (
	"context"
	"slices"
	"testing"
)

// TestRecordProjectNamesStoresNoProjectsAsAnEmptyList: a team with no projects
// is recorded as an empty list, never a null the surface would have to guard.
func TestRecordProjectNamesStoresNoProjectsAsAnEmptyList(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	doc := client.Collection(projectNamesCollection).Doc(projectNamesDoc)
	t.Cleanup(func() { _, _ = doc.Delete(context.Background()) })

	if err := NewProjectNames(client).RecordProjectNames(ctx, nil); err != nil {
		t.Fatal(err)
	}
	snap, err := doc.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if names, ok := snap.Data()["names"].([]any); !ok || len(names) != 0 {
		t.Fatalf("stored %v, want an empty list", snap.Data())
	}
}

func TestProjectNamesReadsARecordWithoutNamesAsEmpty(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	doc := client.Collection(projectNamesCollection).Doc(projectNamesDoc)
	t.Cleanup(func() { _, _ = doc.Delete(context.Background()) })

	if _, err := doc.Set(ctx, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	names, err := NewProjectNames(client).Names(ctx)
	if err != nil || names == nil || len(names) != 0 {
		t.Fatalf("names = %v, err = %v, want an empty list", names, err)
	}
}

func TestProjectNamesRefusesARecordItCannotDecode(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	doc := client.Collection(projectNamesCollection).Doc(projectNamesDoc)
	t.Cleanup(func() { _, _ = doc.Delete(context.Background()) })

	if _, err := doc.Set(ctx, map[string]any{"names": "Forge"}); err != nil {
		t.Fatal(err)
	}
	if names, err := NewProjectNames(client).Names(ctx); err == nil {
		t.Fatalf("names = %v, want a record that is not a list refused", names)
	}
}

func TestRecordProjectNamesReplacesTheWholeRecord(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	p := NewProjectNames(client)
	doc := client.Collection(projectNamesCollection).Doc(projectNamesDoc)
	t.Cleanup(func() { _, _ = doc.Delete(context.Background()) })

	if err := p.RecordProjectNames(ctx, []string{"Forge", "Atlas"}); err != nil {
		t.Fatal(err)
	}
	if got, err := p.Names(ctx); err != nil || !slices.Equal(got, []string{"Forge", "Atlas"}) {
		t.Fatalf("names = %v, err = %v", got, err)
	}

	// A project renamed or removed in Linear leaves nothing of its old name in
	// the record the next poll writes.
	if err := p.RecordProjectNames(ctx, []string{"Forge Two"}); err != nil {
		t.Fatal(err)
	}
	if got, err := p.Names(ctx); err != nil || !slices.Equal(got, []string{"Forge Two"}) {
		t.Fatalf("names = %v, err = %v, want the whole record replaced", got, err)
	}
}

func TestRecordProjectNamesHoldsTheNamesAlone(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	p := NewProjectNames(client)
	doc := client.Collection(projectNamesCollection).Doc(projectNamesDoc)
	t.Cleanup(func() { _, _ = doc.Delete(context.Background()) })

	if err := p.RecordProjectNames(ctx, []string{"Forge"}); err != nil {
		t.Fatal(err)
	}
	snap, err := doc.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data := snap.Data()
	if len(data) != 1 {
		t.Fatalf("stored %v, want the names alone", data)
	}
	if names, _ := data["names"].([]any); !slices.Equal(names, []any{"Forge"}) {
		t.Fatalf("stored %v, want the one name recorded", data)
	}
}

func TestProjectNamesReadsEmptyBeforeAnyPollRecorded(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	doc := client.Collection(projectNamesCollection).Doc(projectNamesDoc)
	if _, err := doc.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	names, err := NewProjectNames(client).Names(ctx)
	if err != nil {
		t.Fatalf("err = %v, want a record never written to read as empty", err)
	}
	if names == nil || len(names) != 0 {
		t.Fatalf("names = %v, want an empty list", names)
	}
}
