package store

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

var plainTag = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func TestFieldsNamesEveryRecordFieldByItsFirestoreTag(t *testing.T) {
	rec := runner.Record{RunID: "r", TicketID: "ABC-12", FailedGate: "test", Models: map[string]string{"build": "p/m"}}
	got := fields(rec)
	typ := reflect.TypeFor[runner.Record]()
	if len(got) != typ.NumField() {
		t.Fatalf("%d fields, want %d", len(got), typ.NumField())
	}
	for f := range typ.Fields() {
		tag := f.Tag.Get("firestore")
		if !plainTag.MatchString(tag) {
			t.Errorf("field %s has tag %q; fields applies no tag options", f.Name, tag)
		}
		if _, ok := got[tag]; !ok {
			t.Errorf("field %s is not written as %q", f.Name, tag)
		}
	}
	if got["ticket_id"] != "ABC-12" || got["failed_gate"] != "test" || got["models"].(map[string]string)["build"] != "p/m" {
		t.Fatalf("fields = %v", got)
	}
}

func TestFieldsSkipsAndStripsTagOptions(t *testing.T) {
	got := fields(struct {
		A int `firestore:"a,omitempty"`
		B int `firestore:"-"`
		C int
	}{1, 2, 3})
	if !reflect.DeepEqual(got, map[string]any{"a": 1, "C": 3}) {
		t.Fatalf("fields = %v", got)
	}
}

func TestPutRecordAgainstTheEmulator(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "forge-wingman-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	recs := NewRecords(client)
	id := fmt.Sprintf("store-test-%d", time.Now().UnixNano())
	doc := client.Collection(runsCollection).Doc(id)
	t.Cleanup(func() { _, _ = doc.Delete(context.Background()) })

	started := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	tk := runner.Ticket{ID: "ABC-12", Title: "feat(x): add a file", Size: "S", Body: "Add a file."}
	if err := runner.Seed(ctx, recs, id, tk, started); err != nil {
		t.Fatal(err)
	}
	if _, err := doc.Set(ctx, map[string]any{"linear_priority": 2, "models": map[string]any{"stale": "x"}},
		firestore.MergeAll); err != nil {
		t.Fatal(err)
	}
	in := runner.FinalizeInput{RunID: id, AttemptID: "1-1", RunResult: "failure", PRResult: "skipped",
		Identity: runner.Identity{Account: "octo", Owner: "octo"}}
	if _, err := runner.Finalize(ctx, recs, in, started.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	snap, err := doc.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data := snap.Data()
	if data["linear_priority"] != int64(2) {
		t.Errorf("a field no Record names was lost: %v", data["linear_priority"])
	}
	if models, _ := data["models"].(map[string]any); len(models) != 0 {
		t.Errorf("models = %v, want the map replaced", models)
	}
	if got, _ := data["started_at"].(time.Time); !got.Equal(started) {
		t.Errorf("started_at = %v, want %v", got, started)
	}
	if data["stop_reason"] != string(runner.StopNoBuildRecord) {
		t.Errorf("stop_reason = %v", data["stop_reason"])
	}
}
