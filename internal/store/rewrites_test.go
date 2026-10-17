package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
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

// unclaimedPR is a pull request number no run row in the suite names, so a read
// by number or by URL reaches the row this test seeded alone: the emulator
// outlives a run, so a query cannot be scoped to one test's rows.
const unclaimedPR = 907_707_001

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
	row, ok, err := r.RunForMerged(ctx, "octo/scratch", dispatcher.ClosedPR{
		Number: 7, Head: branch, URL: "https://github.com/octo/scratch/pull/7",
	})
	if err != nil || !ok || row.RunID != run || !row.Measured.Equal(mergeAt) {
		t.Fatalf("RunForMerged by branch = %+v, %v, %v, want %s measured at %v", row, ok, err, run, mergeAt)
	}
	url := fmt.Sprintf("https://github.com/octo/scratch/pull/%d", unclaimedPR)
	if row, ok, err := r.RunForMerged(ctx, "octo/scratch",
		dispatcher.ClosedPR{Number: unclaimedPR, Head: "wingman/rewrites-none-1", URL: url}); err != nil || ok {
		t.Fatalf("RunForMerged of a PR no row names = %+v, %v, %v, want it absent", row, ok, err)
	}
}

// A run's recorded branches are the field a merge is matched on, so the record
// job's own field name and this store's read of it are one name: a rename of
// either has to fail here.
func TestRewritesFindsTheRunRowByTheBranchesItsRecordCarries(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	run := fresh("rewrites-recorded")
	forget(t, client, run)
	branch := "wingman/rewrites-recorded-1-1"
	if err := NewRecords(client).PutRecord(ctx, run, runner.Record{RunID: run, Branches: []string{branch}}); err != nil {
		t.Fatal(err)
	}
	row, ok, err := NewRewrites(client).RunForMerged(ctx, "octo/scratch",
		dispatcher.ClosedPR{Number: unclaimedPR, Head: branch})
	if err != nil || !ok || row.RunID != run {
		t.Fatalf("RunForMerged on a recorded branch = %+v, %v, %v, want %s", row, ok, err, run)
	}
}

// A merge whose branch an earlier attempt pushed is measured on the run that
// attempted it: the row's own branch names a later attempt.
func TestRewritesFindsTheRunRowByAnEarlierAttemptsBranch(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	run := fresh("rewrites-attempts")
	earlier, later := "wingman/rewrites-attempts-1-1", "wingman/rewrites-attempts-2-1"
	seedRow(t, client, run, map[string]any{branchField: later, branchesField: []string{earlier, later}})
	row, ok, err := NewRewrites(client).RunForMerged(ctx, "octo/scratch",
		dispatcher.ClosedPR{Number: unclaimedPR, Head: earlier})
	if err != nil || !ok || row.RunID != run {
		t.Fatalf("RunForMerged by an earlier attempt's branch = %+v, %v, %v, want %s", row, ok, err, run)
	}
}

// A merge from a pull request whose branch the row no longer carries is measured
// on the run that opened it, read from the row's own pr_url.
func TestRewritesFindsTheRunRowByThePROpened(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	run := fresh("rewrites-opened")
	url := fmt.Sprintf("https://github.com/octo/scratch/pull/%d", unclaimedPR)
	seedRow(t, client, run, map[string]any{branchField: "wingman/rewrites-opened-2-1", prURLField: url})
	row, ok, err := NewRewrites(client).RunForMerged(ctx, "octo/scratch",
		dispatcher.ClosedPR{Number: unclaimedPR, Head: "wingman/rewrites-opened-1-1", URL: url})
	if err != nil || !ok || row.RunID != run {
		t.Fatalf("RunForMerged by the PR a row opened = %+v, %v, %v, want %s", row, ok, err, run)
	}
}

// A merge of the pull request a supersede replaced is measured on the run that
// replaced it, and a number is read in the repository it was read for.
func TestRewritesFindsTheRunRowByThePRItSuperseded(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	run, elsewhere := fresh("rewrites-superseded"), fresh("rewrites-superseded-other")
	seedRow(t, client, run, map[string]any{repoField: "octo/scratch", supersededPRField: int64(unclaimedPR)})
	seedRow(t, client, elsewhere, map[string]any{repoField: "octo/other", supersededPRField: int64(unclaimedPR + 1)})
	r := NewRewrites(client)
	row, ok, err := r.RunForMerged(ctx, "octo/scratch",
		dispatcher.ClosedPR{Number: unclaimedPR, Head: "wingman/rewrites-superseded-2-1"})
	if err != nil || !ok || row.RunID != run {
		t.Fatalf("RunForMerged by a superseded PR = %+v, %v, %v, want %s", row, ok, err, run)
	}
	if row, ok, err := r.RunForMerged(ctx, "octo/scratch",
		dispatcher.ClosedPR{Number: unclaimedPR + 1, Head: "wingman/rewrites-superseded-2-1"}); err != nil || ok {
		t.Fatalf("RunForMerged on another repository's PR = %+v, %v, %v, want it absent", row, ok, err)
	}
}

// A row a merge already measured names its PR by number alone, and that number is
// read in the repository it was read for.
func TestRewritesFindsTheRunRowByThePRNumberItHolds(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	run, elsewhere := fresh("rewrites-number"), fresh("rewrites-number-other")
	seedRow(t, client, run, map[string]any{repoField: "octo/scratch", prNumberField: int64(unclaimedPR + 2)})
	seedRow(t, client, elsewhere, map[string]any{repoField: "octo/other", prNumberField: int64(unclaimedPR + 3)})
	r := NewRewrites(client)
	row, ok, err := r.RunForMerged(ctx, "octo/scratch",
		dispatcher.ClosedPR{Number: unclaimedPR + 2, Head: "wingman/rewrites-number-3-1"})
	if err != nil || !ok || row.RunID != run {
		t.Fatalf("RunForMerged by a PR number = %+v, %v, %v, want %s", row, ok, err, run)
	}
	if row, ok, err := r.RunForMerged(ctx, "octo/scratch",
		dispatcher.ClosedPR{Number: unclaimedPR + 3, Head: "wingman/rewrites-number-3-1"}); err != nil || ok {
		t.Fatalf("RunForMerged on another repository's PR number = %+v, %v, %v, want it absent", row, ok, err)
	}
}

// A pull request no run row holds is reported by the poll that first finds it:
// every poll after reads the same absence.
func TestMarkUnfoundReportsTheFirstPollAlone(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	repo, other := fresh("rewrites-unfound"), fresh("rewrites-unfound-other")
	t.Cleanup(func() {
		ctx := context.Background()
		for _, name := range []string{repo, other} {
			_, _ = client.Collection(dispatchCollection).Doc(unfoundDocID(name, unclaimedPR)).Delete(ctx)
		}
	})
	r := NewRewrites(client)
	if first, err := r.MarkUnfound(ctx, repo, unclaimedPR, mergeAt); err != nil || !first {
		t.Fatalf("MarkUnfound = %v, %v, want the first poll to report it", first, err)
	}
	if first, err := r.MarkUnfound(ctx, repo, unclaimedPR, mergeAt.Add(time.Minute)); err != nil || first {
		t.Fatalf("MarkUnfound again = %v, %v, want a later poll silent", first, err)
	}
	if first, err := r.MarkUnfound(ctx, other, unclaimedPR, mergeAt); err != nil || !first {
		t.Fatalf("MarkUnfound for another repository = %v, %v, want its own marker", first, err)
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

// A newly measured PR's title is written on its run row as pr_title, on one
// line.
func TestRewritesRecordsThePRTitleOnTheRunRow(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	run := fresh("rewrites-title")
	forget(t, client, run)
	if err := NewRewrites(client).Record(ctx, run,
		dispatcher.Measured{MergedAt: mergeAt, PR: 7, Title: "FRG-1 add a\nwidget"}); err != nil {
		t.Fatal(err)
	}
	if got := readRow(t, client, run)[prTitleField]; got != "FRG-1 add a widget" {
		t.Fatalf("pr_title = %v, want the PR's title on one line", got)
	}
}

// The title a row holds is read with the rest of the measurement, and a row
// measured before the title was kept reads as an empty one.
func TestSampleReadsThePRTitleOffTheRow(t *testing.T) {
	row := map[string]any{prTitleField: "feat(runner): FRG-1 add the widget"}
	if got, want := sample("run-1", row).Title, "feat(runner): FRG-1 add the widget"; got != want {
		t.Fatalf("title = %q, want %q", got, want)
	}
	// A row measured before this change carries the measurement and no title.
	before := map[string]any{rewriteMergedAtField: mergeAt, rewriteAgentLinesField: int64(3)}
	if got := sample("run-old", before).Title; got != "" {
		t.Fatalf("title = %q, want a row with no pr_title read as empty", got)
	}
}

// titleText keeps a title on one line and within the limit, cutting a longer
// one back to a character boundary.
func TestTitleTextKeepsATitleToOneLineWithinTheLimit(t *testing.T) {
	limit := strings.Repeat("x", prTitleLimit)
	// 67 three-byte characters are 201 bytes, so a cut at the 200th byte splits
	// one: the stored title stops at the 66 that fill 198.
	euro := strings.Repeat("€", 67)
	// 50 four-byte characters are exactly the limit, so a cut one byte further
	// lands on a boundary and keeps all 50.
	emoji := strings.Repeat("🙂", 50)
	for _, tt := range []struct {
		name  string
		title string
		want  string
	}{
		{"a plain title is kept", "FRG-1 add the widget", "FRG-1 add the widget"},
		{"a newline becomes a space", "FRG-1 add a\nwidget", "FRG-1 add a widget"},
		{"a carriage return becomes a space", "FRG-1 a\rb", "FRG-1 a b"},
		{"a tab becomes a space", "FRG-1\ta b", "FRG-1 a b"},
		{"a NUL becomes a space", "FRG-1 a\x00b", "FRG-1 a b"},
		{"a title of the limit is kept whole", limit, limit},
		{"a title over the limit is cut to it", limit + "x", limit},
		{"a cut steps back to the character boundary", euro, strings.Repeat("€", 66)},
		{"a cut landing on a boundary keeps it whole", emoji + "x", emoji},
	} {
		if got := titleText(tt.title); got != tt.want {
			t.Errorf("%s: titleText(%q) = %q, want %q", tt.name, tt.title, got, tt.want)
		}
	}
}
