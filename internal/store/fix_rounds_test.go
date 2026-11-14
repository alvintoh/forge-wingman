package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

func TestMergedFixRoundFieldsAreRecordFields(t *testing.T) {
	all := fields(runner.Record{})
	for _, name := range mergedFixRoundFields {
		if _, ok := all[name]; !ok {
			t.Errorf("%q is not a field of the run record, so a merge would write nothing under it", name)
		}
	}
	if len(mergedFixRoundFields) != 5 {
		t.Errorf("a merge rewrites %d fields, want the rounds, steps, tokens, settled cost and time of use", len(mergedFixRoundFields))
	}
}

func fixRound(n int, cost float64) runner.ReviewFixRound {
	return runner.ReviewFixRound{
		Step: runner.Step{Phase: runner.PhaseFix, Round: n, Model: "p/m", Tokens: runner.Usage{Input: 10, Cost: cost, Steps: 1},
			CompletionsObject: fmt.Sprintf("completions/1-1-fix-%d.jsonl", n)},
		Findings: []string{"a finding"}, Reviewed: true, Resolved: 1,
	}
}

func TestMergeFixRoundsAgainstTheEmulator(t *testing.T) {
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
	id := fmt.Sprintf("merge-fix-rounds-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = client.Collection(runsCollection).Doc(id).Delete(context.Background()) })

	settled := time.Date(2026, 9, 25, 11, 0, 0, 0, time.UTC)
	rec := runner.NewRecord(id, runner.Ticket{ID: "ABC-12"}, settled.Add(-time.Hour))
	rec.Steps = []runner.Step{{Phase: runner.PhaseBuild, Round: 1, Model: "p/m", Tokens: runner.Usage{Input: 100, Cost: 1, Steps: 1}}}
	rec.Tokens = runner.Usage{Input: 100, Cost: 1, Steps: 1}
	rec.SettledAt, rec.SettledProviderCostMicros = settled, 1_000_000
	if err := recs.PutRecord(ctx, id, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Collection(runsCollection).Doc(id).Set(ctx, map[string]any{"linear_priority": 2}, firestore.MergeAll); err != nil {
		t.Fatal(err)
	}

	rounds := []runner.ReviewFixRound{fixRound(1, 0.25), fixRound(2, 0.5)}
	for range 2 {
		if err := recs.MergeFixRounds(ctx, id, rounds); err != nil {
			t.Fatal(err)
		}
	}
	got, err := recs.GetRecord(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.FixRounds) != 2 || len(got.Steps) != 3 {
		t.Fatalf("%d rounds and %d steps after merging twice, want 2 and 3", len(got.FixRounds), len(got.Steps))
	}
	if got.Tokens.Input != 120 || got.Tokens.Cost != 1.75 || got.SettledProviderCostMicros != 1_750_000 {
		t.Fatalf("tokens %+v, settled cost %d; want 120 input, 1.75 and 1750000 with the rounds' steps counted once", got.Tokens, got.SettledProviderCostMicros)
	}
	snap, err := client.Collection(runsCollection).Doc(id).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Data()["linear_priority"] != int64(2) {
		t.Errorf("a field the merge does not name was lost: %v", snap.Data()["linear_priority"])
	}

	if err := recs.MergeFixRounds(ctx, id+"-absent", rounds); !errors.Is(err, runner.ErrRecordNotFound) {
		t.Fatalf("merging into an absent run: err = %v, want ErrRecordNotFound", err)
	}
}
