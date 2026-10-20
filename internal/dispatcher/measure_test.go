package dispatcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// fakePullRequests is the pull-request reads the rewrite measurement makes, from
// the fixtures a test names.
type fakePullRequests struct {
	closed      map[string][]ClosedPR
	pulls       map[int]ClosedPR
	commits     map[int][]Commit
	closedErr   error
	failClosed  string
	keepOnErr   bool
	pullErr     error
	commitErr   error
	pulled      []int
	commitCalls []int
}

func (p *fakePullRequests) Closed(_ context.Context, repo string) ([]ClosedPR, error) {
	if p.closedErr != nil && (p.failClosed == "" || p.failClosed == repo) {
		if p.keepOnErr {
			return p.closed[repo], p.closedErr
		}
		return nil, p.closedErr
	}
	return p.closed[repo], nil
}

func (p *fakePullRequests) Pull(_ context.Context, _ string, number int) (ClosedPR, error) {
	if p.pullErr != nil {
		return ClosedPR{}, p.pullErr
	}
	p.pulled = append(p.pulled, number)
	return p.pulls[number], nil
}

func (p *fakePullRequests) Commits(_ context.Context, _ string, number int) ([]Commit, error) {
	if p.commitErr != nil {
		return nil, p.commitErr
	}
	p.commitCalls = append(p.commitCalls, number)
	return p.commits[number], nil
}

// fakeRewrites is the run rows' measurement surface, from the rows a test seeds.
type fakeRewrites struct {
	// rows is keyed by branch: the row's own, and the ones its other attempts
	// pushed.
	rows map[string]RunRow
	// prs is keyed by the PR a row opened, or the one a supersede of it
	// replaced.
	prs         map[int]RunRow
	recent      []Sample
	recorded    map[string]Measured
	reverted    []string
	marked      map[string]bool
	rowErr      error
	recordErr   error
	revertedErr error
	recentErr   error
	markErr     error
}

func (r *fakeRewrites) RunForMerged(_ context.Context, _ string, pr ClosedPR) (RunRow, bool, error) {
	if r.rowErr != nil {
		return RunRow{}, false, r.rowErr
	}
	if pr.Head != "" {
		if row, ok := r.rows[pr.Head]; ok {
			return row, true, nil
		}
	}
	if row, ok := r.prs[pr.Number]; ok && pr.Number != 0 {
		return row, true, nil
	}
	return RunRow{}, false, nil
}

// MarkUnfound keeps one marker per repository and PR, as the store does, and
// reports only the first poll to write it.
func (r *fakeRewrites) MarkUnfound(_ context.Context, repo string, number int, _ time.Time) (bool, error) {
	if r.markErr != nil {
		return false, r.markErr
	}
	if r.marked == nil {
		r.marked = map[string]bool{}
	}
	key := fmt.Sprintf("%s#%d", repo, number)
	if r.marked[key] {
		return false, nil
	}
	r.marked[key] = true
	return true, nil
}

func (r *fakeRewrites) Record(_ context.Context, id string, m Measured) error {
	if r.recordErr != nil {
		return r.recordErr
	}
	if r.recorded == nil {
		r.recorded = map[string]Measured{}
	}
	r.recorded[id] = m
	// The real Record clears a revert flag, so the fake does too.
	kept := r.reverted[:0]
	for _, rid := range r.reverted {
		if rid != id {
			kept = append(kept, rid)
		}
	}
	r.reverted = kept
	// The row carries the measurement once recorded, so a later pass over the
	// same listing measures nothing.
	for branch, row := range r.rows {
		if row.RunID == id {
			row.Measured = m.MergedAt
			r.rows[branch] = row
		}
	}
	for number, row := range r.prs {
		if row.RunID == id {
			row.Measured = m.MergedAt
			r.prs[number] = row
		}
	}
	return nil
}

func (r *fakeRewrites) FlagReverted(_ context.Context, id string) error {
	if r.revertedErr != nil {
		return r.revertedErr
	}
	r.reverted = append(r.reverted, id)
	return nil
}

func (r *fakeRewrites) Recent(_ context.Context, limit int) ([]Sample, error) {
	if r.recentErr != nil {
		return nil, r.recentErr
	}
	if len(r.recent) > limit {
		return r.recent[:limit], nil
	}
	return r.recent, nil
}

var (
	// mergedAt is when the fixture PRs merged, and the clock the phase reads.
	mergedAt = time.Date(2026, 10, 14, 3, 0, 0, 0, time.UTC)
	// runBranch is the branch the fixture run pushed, its id and attempt last.
	runBranch   = runner.BranchPrefix + "FRG-1-123-1"
	runOnBranch = "123"
	// oneRepo is the allowlist a measurement test dispatches into.
	oneRepo = Config{Repos: []string{scratch}}
)

// rewriteDeps is the deps the measurement phase reads, with no notices raised
// yet.
func rewriteDeps(prs *fakePullRequests, rows *fakeRewrites) (Deps, *fakeNotices) {
	notices := &fakeNotices{}
	return Deps{
		PullRequests: prs,
		Rewrites:     rows,
		Notices:      notices,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:          func() time.Time { return mergedAt },
	}, notices
}

func TestMeasureRecordsTheLinesOfAMergedPR(t *testing.T) {
	prs := &fakePullRequests{
		closed: map[string][]ClosedPR{scratch: {{Number: 7, Title: "a run's PR", Head: runBranch, MergedAt: mergedAt}}},
		commits: map[int][]Commit{7: {
			{Login: runner.CommitAuthorLogin, Additions: 10, Deletions: 2},
			{Name: "the owner", Email: "owner@example.com", Additions: 3, Deletions: 1},
		}},
	}
	rows := &fakeRewrites{rows: map[string]RunRow{runBranch: {RunID: runOnBranch}}}
	deps, _ := rewriteDeps(prs, rows)
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	want := Measured{
		Rewrite:  Rewrite{AgentLines: 12, OwnerLines: 4, UnlabelledLines: 4},
		MergedAt: mergedAt,
		PR:       7,
		Title:    "a run's PR",
	}
	if got := rows.recorded[runOnBranch]; got != want {
		t.Fatalf("recorded %+v, want %+v", got, want)
	}
	// A later poll finds that merge already measured and asks GitHub for nothing
	// more.
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	if len(prs.commitCalls) != 1 {
		t.Fatalf("commitCalls = %v, want the merge measured once", prs.commitCalls)
	}
}

func TestMeasureLeavesAPRThatClosedUnmergedAlone(t *testing.T) {
	prs := &fakePullRequests{closed: map[string][]ClosedPR{scratch: {{Number: 7, Head: runBranch}}}}
	rows := &fakeRewrites{rows: map[string]RunRow{runBranch: {RunID: runOnBranch}}}
	deps, _ := rewriteDeps(prs, rows)
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	if len(rows.recorded) != 0 || len(prs.commitCalls) != 0 {
		t.Fatalf("recorded %v from %v, want an unmerged PR untouched", rows.recorded, prs.commitCalls)
	}
}

// A merged PR whose branch no run row holds is measured on the row that names it
// as the PR it opened, which is the row its own branch no longer names.
func TestMeasureMatchesAMergedPRByThePRItNames(t *testing.T) {
	url := "https://github.com/octo/scratch/pull/7"
	prs := &fakePullRequests{
		closed: map[string][]ClosedPR{scratch: {
			{Number: 7, Title: "a run's PR", Head: "wingman/FRG-1-456-2", URL: url, MergedAt: mergedAt},
		}},
		commits: map[int][]Commit{7: {{Login: runner.CommitAuthorLogin, Additions: 8}}},
	}
	rows := &fakeRewrites{prs: map[int]RunRow{7: {RunID: runOnBranch}}}
	deps, _ := rewriteDeps(prs, rows)
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	if got := rows.recorded[runOnBranch]; got.PR != 7 || got.AgentLines != 8 {
		t.Fatalf("recorded %+v, want the merge measured on the row that names it", got)
	}
	if len(rows.marked) != 0 {
		t.Fatalf("marked = %v, want a matched merge reported unfound by nothing", rows.marked)
	}
}

func TestMeasureReportsAMergedPRWithNoRunRowOnce(t *testing.T) {
	prs := &fakePullRequests{
		closed: map[string][]ClosedPR{scratch: {
			{Number: 7, Head: runBranch, URL: "https://github.com/octo/scratch/pull/7", MergedAt: mergedAt},
		}},
	}
	rows := &fakeRewrites{}
	deps, _ := rewriteDeps(prs, rows)
	var logs bytes.Buffer
	deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	for poll := 1; poll <= 2; poll++ {
		if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(logs.String(), "msg=mergedPRUnfound"); n != 1 {
			t.Fatalf("after poll %d the log = %q, want the gap reported once, by the first poll", poll, logs.String())
		}
	}
	if len(rows.recorded) != 0 {
		t.Fatalf("recorded %v, want nothing for a PR no run row holds", rows.recorded)
	}
}

func TestMeasureSkipsAnUnlistableRepositoryWhenAnotherListed(t *testing.T) {
	prs := &fakePullRequests{
		closed:     map[string][]ClosedPR{"octo/other": {{Number: 7, Head: runBranch, MergedAt: mergedAt}}},
		closedErr:  errFirestore,
		failClosed: scratch,
		commits:    map[int][]Commit{7: {{Login: runner.CommitAuthorLogin, Additions: 5}}},
	}
	rows := &fakeRewrites{rows: map[string]RunRow{runBranch: {RunID: runOnBranch}}}
	deps, _ := rewriteDeps(prs, rows)
	var logs bytes.Buffer
	deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	if err := measureRewrites(context.Background(), deps, Config{Repos: []string{scratch, "octo/other"}}); err != nil {
		t.Fatalf("err = %v, want none while another repository listed", err)
	}
	if got := rows.recorded[runOnBranch]; got.AgentLines != 5 {
		t.Fatalf("recorded %+v, want the other repository still measured", got)
	}
	if got := strings.Count(logs.String(), "msg=rewriteListingFailed"); got != 1 {
		t.Fatalf("log = %q, want one rewriteListingFailed", logs.String())
	}
	if !strings.Contains(logs.String(), "repo="+scratch) || !strings.Contains(logs.String(), "err=") {
		t.Fatalf("log = %q, want the repository and the reason as their own fields", logs.String())
	}
}

func TestMeasureFailsWhenNoRepositoryCanBeListed(t *testing.T) {
	prs := &fakePullRequests{closedErr: errFirestore}
	deps, _ := rewriteDeps(prs, &fakeRewrites{})
	err := measureRewrites(context.Background(), deps, Config{Repos: []string{scratch, "octo/other"}})
	if !errors.Is(err, errFirestore) || !strings.Contains(err.Error(), scratch) || !strings.Contains(err.Error(), "octo/other") {
		t.Fatalf("err = %v, want every unlistable repository named", err)
	}
}

func TestRevertFlagsTheRunRowItNames(t *testing.T) {
	prs := &fakePullRequests{closed: map[string][]ClosedPR{scratch: {
		{Number: 7, Title: "a run's PR", Head: runBranch, MergedAt: mergedAt},
		{Number: 9, Title: `Revert "a run's PR"`, Body: "Reverts octo/scratch#7\n", MergedAt: mergedAt.Add(time.Hour)},
	}}}
	rows := &fakeRewrites{rows: map[string]RunRow{runBranch: {RunID: runOnBranch}}}
	deps, _ := rewriteDeps(prs, rows)
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	if len(rows.reverted) != 1 || rows.reverted[0] != runOnBranch {
		t.Fatalf("reverted = %v, want the run row the revert names", rows.reverted)
	}
	if len(prs.pulled) != 0 {
		t.Fatalf("pulled = %v, want the listing's own entry used", prs.pulled)
	}
}

func TestRevertTitledByHandFlagsThePRItQuotes(t *testing.T) {
	prs := &fakePullRequests{closed: map[string][]ClosedPR{scratch: {
		{Number: 7, Title: "a run's PR", Head: runBranch, MergedAt: mergedAt},
		{Number: 9, Title: `Revert "a run's PR"`, MergedAt: mergedAt.Add(time.Hour)},
	}}}
	rows := &fakeRewrites{rows: map[string]RunRow{runBranch: {RunID: runOnBranch}}}
	deps, _ := rewriteDeps(prs, rows)
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	if len(rows.reverted) != 1 || rows.reverted[0] != runOnBranch {
		t.Fatalf("reverted = %v, want the run whose title the revert quotes", rows.reverted)
	}
}

func TestRevertOfAPROutsideTheListingIsRead(t *testing.T) {
	prs := &fakePullRequests{
		closed: map[string][]ClosedPR{scratch: {
			{Number: 9, Title: `Revert "a run's PR"`, Body: "Reverts octo/scratch#7", MergedAt: mergedAt.Add(time.Hour)},
		}},
		pulls: map[int]ClosedPR{7: {Number: 7, Head: runBranch, MergedAt: mergedAt}},
	}
	rows := &fakeRewrites{rows: map[string]RunRow{runBranch: {RunID: runOnBranch}}}
	deps, _ := rewriteDeps(prs, rows)
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	if len(prs.pulled) != 1 || prs.pulled[0] != 7 || len(rows.reverted) != 1 {
		t.Fatalf("pulled = %v, reverted = %v, want PR 7 read and its run flagged", prs.pulled, rows.reverted)
	}
}

// A revert of a PR the run's own branch no longer names still flags that run.
func TestRevertFlagsTheRunThePRItNamesBelongsTo(t *testing.T) {
	prs := &fakePullRequests{
		closed: map[string][]ClosedPR{scratch: {
			{Number: 9, Title: `Revert "a run's PR"`, Body: "Reverts octo/scratch#7", MergedAt: mergedAt.Add(time.Hour)},
		}},
		pulls: map[int]ClosedPR{7: {
			Number: 7, Head: "wingman/FRG-1-456-2",
			URL: "https://github.com/octo/scratch/pull/7", MergedAt: mergedAt,
		}},
	}
	rows := &fakeRewrites{prs: map[int]RunRow{7: {RunID: runOnBranch}}}
	deps, _ := rewriteDeps(prs, rows)
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	if len(rows.reverted) != 1 || rows.reverted[0] != runOnBranch {
		t.Fatalf("reverted = %v, want the run the reverted PR names", rows.reverted)
	}
}

func TestRevertOfAPRWhenTheRevertNeverMerged(t *testing.T) {
	prs := &fakePullRequests{closed: map[string][]ClosedPR{scratch: {
		{Number: 7, Title: "a run's PR", Head: runBranch, MergedAt: mergedAt},
		{Number: 9, Title: `Revert "a run's PR"`, Body: "Reverts octo/scratch#7"},
	}}}
	rows := &fakeRewrites{rows: map[string]RunRow{runBranch: {RunID: runOnBranch}}}
	deps, _ := rewriteDeps(prs, rows)
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	if len(rows.reverted) != 0 {
		t.Fatalf("reverted = %v, want a revert that never merged to flag nothing", rows.reverted)
	}
}

func TestRevertBodyWithCRLFFlagsTheRun(t *testing.T) {
	prs := &fakePullRequests{closed: map[string][]ClosedPR{scratch: {
		{Number: 7, Title: "a run's PR", Head: runBranch, MergedAt: mergedAt},
		{Number: 9, Title: "reverts by hand", Body: "Reverts octo/scratch#7\r\n\r\nextra\r\n", MergedAt: mergedAt.Add(time.Hour)},
	}}}
	rows := &fakeRewrites{rows: map[string]RunRow{runBranch: {RunID: runOnBranch}}}
	deps, _ := rewriteDeps(prs, rows)
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	if len(rows.reverted) != 1 {
		t.Fatalf("reverted = %v, want a CRLF revert body read", rows.reverted)
	}
}

func TestRevertListedBeforeItsOriginalStaysFlagged(t *testing.T) {
	// Listings run most recently updated first, so the revert precedes the merge
	// it takes back and the measurement must not clear its flag.
	prs := &fakePullRequests{closed: map[string][]ClosedPR{scratch: {
		{Number: 9, Title: "reverts by hand", Body: "Reverts octo/scratch#7", MergedAt: mergedAt.Add(time.Hour)},
		{Number: 7, Title: "a run's PR", Head: runBranch, MergedAt: mergedAt},
	}}}
	rows := &fakeRewrites{rows: map[string]RunRow{runBranch: {RunID: runOnBranch}}}
	deps, _ := rewriteDeps(prs, rows)
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	if len(rows.recorded) != 1 || len(rows.reverted) != 1 {
		t.Fatalf("recorded = %v, reverted = %v, want the run measured and still flagged", rows.recorded, rows.reverted)
	}
}

func TestRevertOfAPRThatNeverMergedFlagsNothing(t *testing.T) {
	prs := &fakePullRequests{closed: map[string][]ClosedPR{scratch: {
		{Number: 7, Title: "a run's PR", Head: runBranch},
		{Number: 9, Title: "reverts by hand", Body: "Reverts octo/scratch#7", MergedAt: mergedAt},
	}}}
	rows := &fakeRewrites{rows: map[string]RunRow{runBranch: {RunID: runOnBranch}}}
	deps, _ := rewriteDeps(prs, rows)
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	if len(rows.reverted) != 0 {
		t.Fatalf("reverted = %v, want a revert of an unmerged PR to flag nothing", rows.reverted)
	}
}

func TestRevertsThatNameNoRunPRFlagNothing(t *testing.T) {
	for name, tt := range map[string]struct {
		revert ClosedPR
		pulls  map[int]ClosedPR
	}{
		"another repository's PR number": {
			ClosedPR{Number: 9, Body: "Reverts octo/other#7", MergedAt: mergedAt.Add(time.Hour)},
			map[int]ClosedPR{7: {Number: 7, Head: runBranch, MergedAt: mergedAt}},
		},
		"a PR that merged after the revert": {
			ClosedPR{Number: 9, Body: "Reverts octo/scratch#7", MergedAt: mergedAt},
			map[int]ClosedPR{7: {Number: 7, Head: runBranch, MergedAt: mergedAt.Add(time.Hour)}},
		},
		"a PR that is not a run's": {
			ClosedPR{Number: 9, Body: "Reverts octo/scratch#7", MergedAt: mergedAt.Add(time.Hour)},
			map[int]ClosedPR{7: {Number: 7, Head: "feature/by-hand", MergedAt: mergedAt}},
		},
		"another PR than the one named": {
			ClosedPR{Number: 9, Body: "Reverts octo/scratch#7", MergedAt: mergedAt.Add(time.Hour)},
			map[int]ClosedPR{7: {Number: 8, Head: runBranch, MergedAt: mergedAt}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			prs := &fakePullRequests{closed: map[string][]ClosedPR{scratch: {tt.revert}}, pulls: tt.pulls}
			rows := &fakeRewrites{rows: map[string]RunRow{runBranch: {RunID: runOnBranch}, "feature/by-hand": {RunID: "other"}}}
			deps, _ := rewriteDeps(prs, rows)
			if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
				t.Fatal(err)
			}
			if len(rows.reverted) != 0 {
				t.Fatalf("reverted = %v, want the revert to flag no run", rows.reverted)
			}
		})
	}
}

func TestTruncatedListingWithAFreshMergeOnItsLastPageFailsClosed(t *testing.T) {
	prs := &fakePullRequests{
		closed:    map[string][]ClosedPR{scratch: {{Number: 7, Head: runBranch, MergedAt: mergedAt}}},
		commits:   map[int][]Commit{7: {{Login: "the-owner", Additions: 1}}},
		closedErr: ErrClosedTruncated,
		keepOnErr: true,
	}
	rows := &fakeRewrites{rows: map[string]RunRow{runBranch: {RunID: runOnBranch}}, recent: samples(scratch, 20, 100, 0)}
	deps, _ := rewriteDeps(prs, rows)
	if err := measureRewrites(context.Background(), deps, oneRepo); !errors.Is(err, ErrClosedTruncated) {
		t.Fatalf("err = %v, want the truncation reported", err)
	}
}

func TestTruncatedListingWithNothingNewOnItsLastPageIsRead(t *testing.T) {
	prs := &fakePullRequests{
		closed:    map[string][]ClosedPR{scratch: {{Number: 7, Head: runBranch, MergedAt: mergedAt}}},
		closedErr: ErrClosedTruncated,
		keepOnErr: true,
	}
	rows := &fakeRewrites{rows: map[string]RunRow{runBranch: {RunID: runOnBranch, Measured: mergedAt}}, recent: samples(scratch, 20, 100, 0)}
	deps, _ := rewriteDeps(prs, rows)
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatalf("err = %v, want a capped listing whose last page is already measured to be fine", err)
	}
}
