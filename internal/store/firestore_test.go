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

	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

var plainTag = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func TestFieldsNamesEveryRecordFieldByItsFirestoreTag(t *testing.T) {
	rec := runner.Record{RunID: "r", TicketID: "ABC-12", FailedGate: "test",
		Steps: []runner.Step{{Phase: runner.PhaseBuild, Round: 1, Model: "p/m"}}}
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
	if got["ticket_id"] != "ABC-12" || got["failed_gate"] != "test" || got["steps"].([]runner.Step)[0].Model != "p/m" {
		t.Fatalf("fields = %v", got)
	}
}

// TestFieldsNamesEveryPlanRecordFieldByItsFirestoreTag guards the plan stage's
// row shape: WritePlan writes exactly the fields fields() finds, so a
// mistagged or optioned field would silently not reach the record.
func TestFieldsNamesEveryPlanRecordFieldByItsFirestoreTag(t *testing.T) {
	got := fields(runner.PlanRecord{Files: []string{"a.go"}, BaseSHA: "abc", Model: "command-code/x"})
	typ := reflect.TypeFor[runner.PlanRecord]()
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
	if _, ok := got["plan_files"]; !ok {
		t.Error("the plan's files are not written as plan_files")
	}
	if got["plan_model"] != "command-code/x" {
		t.Errorf("the plan's model is not written as plan_model: %v", got["plan_model"])
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
	if _, err := doc.Create(ctx, runner.NewRecord(id, tk, started)); err != nil {
		t.Fatal(err)
	}
	if _, err := doc.Set(ctx, map[string]any{"linear_priority": 2, "steps": []any{map[string]any{"model": "stale"}}},
		firestore.MergeAll); err != nil {
		t.Fatal(err)
	}
	in := runner.FinalizeInput{RunID: id, AttemptID: "1-1", RunResult: "failure", PRResult: "skipped",
		Identity: runner.Identity{Account: "octo", Owner: "octo"}}
	if _, err := runner.Finalize(ctx, recs, NewQueue(client), recs, in, started.Add(time.Hour)); err != nil {
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
	if steps, _ := data["steps"].([]any); len(steps) != 0 {
		t.Errorf("steps = %v, want the slice replaced", steps)
	}
	if got, _ := data["started_at"].(time.Time); !got.Equal(started) {
		t.Errorf("started_at = %v, want %v", got, started)
	}
	if data["stop_reason"] != string(runner.StopNoBuildRecord) {
		t.Errorf("stop_reason = %v", data["stop_reason"])
	}
}

// writeLegacyClaimFields writes onto run id the fields a free-tier or moved claim
// once wrote and no claim writes any more, as a row settled then still holds.
func writeLegacyClaimFields(t *testing.T, client *firestore.Client, id string) {
	t.Helper()
	labels := map[string]any{"build": "beta/free-a", "review": "beta/free-b", "plan": []string{"beta/free-a"}}
	if _, err := client.Collection(runsCollection).Doc(id).Update(context.Background(), []firestore.Update{
		{Path: "last_resort", Value: true}, {Path: "last_resort_models", Value: labels},
		{Path: "moved_from", Value: "alpha"}, {Path: "plan_models", Value: labels},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestARowAnOldClaimLeftItsTierOnReadsOnEveryPath(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	plan, size, id := fresh("legacy-plan"), "S-"+fresh("legacy-size"), fresh("legacy-run")
	forget(t, client, id)
	comparedRecord(t, client, id, comparedRun{plan: plan, model: "beta/paid", settledAt: queueAt, cost: money.Dollar})
	if _, err := client.Collection(runsCollection).Doc(id).Update(ctx, []firestore.Update{{Path: sizeField, Value: size}}); err != nil {
		t.Fatal(err)
	}
	writeLegacyClaimFields(t, client, id)
	recs, since := NewRecords(client), queueAt.Add(-time.Hour)

	rec, err := recs.GetRecord(ctx, id)
	if err != nil || rec.Plan != plan || rec.ModelLabels.Build != "beta/paid" {
		t.Fatalf("record plan %q, models %+v, err %v, want the row's own", rec.Plan, rec.ModelLabels, err)
	}
	if mean, sample, err := recs.ProviderTrailingCost(ctx, plan, since, ""); err != nil || mean != money.Dollar || sample != 1 {
		t.Fatalf("trailing cost %d over %d, err %v, want the run counted", mean, sample, err)
	}
	cmp, err := recs.Comparison(ctx, since)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := findProvider(cmp.Providers, plan); !ok || p.Sample != 1 {
		t.Fatalf("providers = %+v, want the run counted under its plan", cmp.Providers)
	}
	if _, err := recs.PlanCaches(ctx, since); err != nil {
		t.Fatal(err)
	}
	if _, err := recs.RecentSettled(ctx, 50); err != nil {
		t.Fatal(err)
	}
	est := NewEstimates(client)
	if got, err := est.Estimate(ctx, size); err != nil || got.ProviderCost != money.Dollar {
		t.Fatalf("estimate = %+v, err %v, want the run's own cost", got, err)
	}
	if _, err := est.EstimatePlan(ctx, size); err != nil {
		t.Fatal(err)
	}
}
