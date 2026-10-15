package store

import (
	"context"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

// mergeAt is the merge a rewrite test measures, after noticeAt so a row written
// by another test never sorts ahead of one.
var mergeAt = noticeAt.Add(2 * time.Hour)

func TestRewritesRecordsTheMeasurementOnTheRunRow(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	run := fresh("rewrites-record")
	forget(t, client, run)
	r := NewRewrites(client)
	if err := r.Record(ctx, run, dispatcher.Measured{
		Rewrite: dispatcher.Rewrite{AgentLines: 12, OwnerLines: 4}, MergedAt: mergeAt,
	}); err != nil {
		t.Fatal(err)
	}
	row := readRow(t, client, run)
	if row[rewriteAgentLinesField] != int64(12) || row[rewriteOwnerLinesField] != int64(4) {
		t.Fatalf("row = %v, want 12 agent lines and 4 owner lines", row)
	}
	if at, ok := row[rewriteMergedAtField].(time.Time); !ok || !at.Equal(mergeAt) {
		t.Fatalf("merged at %v, want %v", row[rewriteMergedAtField], mergeAt)
	}
	// A revert is flagged on the row, and the next merge measured over it clears
	// the flag with the rest of the measurement.
	if err := r.FlagReverted(ctx, run); err != nil {
		t.Fatal(err)
	}
	if reverted, _ := readRow(t, client, run)[rewriteRevertedField].(bool); !reverted {
		t.Fatal("the row was not flagged reverted")
	}
	if err := r.Record(ctx, run, dispatcher.Measured{MergedAt: mergeAt.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if reverted, _ := readRow(t, client, run)[rewriteRevertedField].(bool); reverted {
		t.Fatal("a later measurement left the revert flag standing")
	}
}

func TestRewritesFindsTheRunRowByBranch(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	run := fresh("rewrites-branch")
	branch := "wingman/rewrites-9-1"
	seedRow(t, client, run, map[string]any{branchField: branch})
	r := NewRewrites(client)
	if err := r.Record(ctx, run, dispatcher.Measured{MergedAt: mergeAt}); err != nil {
		t.Fatal(err)
	}
	row, ok, err := r.RunByBranch(ctx, branch)
	if err != nil || !ok || row.RunID != run || !row.Measured.Equal(mergeAt) {
		t.Fatalf("RunByBranch(%q) = %+v, %v, %v, want %s measured at %v", branch, row, ok, err, run, mergeAt)
	}
	if row, ok, err := r.RunByBranch(ctx, "wingman/rewrites-none-1"); err != nil || ok {
		t.Fatalf("RunByBranch of an unknown branch = %+v, %v, %v, want it absent", row, ok, err)
	}
}

func TestRecentReturnsTheMeasuredMergesNewestFirst(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	r := NewRewrites(client)
	ids := []string{fresh("rewrites-oldest"), fresh("rewrites-middle"), fresh("rewrites-newest")}
	unmeasured := fresh("rewrites-unmeasured")
	forget(t, client, ids[0], ids[1], ids[2])
	// A row no merge has reached carries no measurement, so it is not a sample.
	seedRow(t, client, unmeasured, map[string]any{repoField: "octo/scratch"})
	// Merge times are this run's own, so rows an earlier run left in the shared
	// emulator sort behind them.
	at := time.Now().UTC()
	for i, id := range ids {
		err := r.Record(ctx, id, dispatcher.Measured{
			Rewrite:  dispatcher.Rewrite{AgentLines: int64(100 + i), OwnerLines: 1},
			MergedAt: at.Add(time.Duration(i) * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// The oldest row carries the fields the sample reads beside the measurement.
	if _, err := client.Collection(runsCollection).Doc(ids[0]).Set(ctx, map[string]any{
		repoField: "octo/scratch", autoMergeField: true, failedGateField: "lint",
		prURLField: "https://github.com/octo/scratch/pull/7",
	}, firestore.MergeAll); err != nil {
		t.Fatal(err)
	}
	samples, err := r.Recent(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 3 {
		t.Fatalf("Recent(3) = %+v, want three samples", samples)
	}
	for i, want := range []int64{102, 101, 100} {
		if samples[i].AgentLines != want {
			t.Fatalf("Recent(3) = %+v, want the newest merge first, %d agent lines at %d", samples, want, i)
		}
	}
	if oldest := samples[2]; oldest.Repo != "octo/scratch" || !oldest.AutoMerged ||
		oldest.PRURL != "https://github.com/octo/scratch/pull/7" || !oldest.GateMiss {
		t.Fatalf("oldest = %+v, want its repo, auto-merge, PR and failed gate read with it", oldest)
	}
	if newest := samples[0]; newest.GateMiss || newest.AutoMerged || newest.PRURL != "" {
		t.Fatalf("newest = %+v, want only the fields that row carries", newest)
	}
	if one, err := r.Recent(ctx, 1); err != nil || len(one) != 1 || one[0].AgentLines != 102 {
		t.Fatalf("Recent(1) = %+v, %v, want the newest merge alone", one, err)
	}
}
