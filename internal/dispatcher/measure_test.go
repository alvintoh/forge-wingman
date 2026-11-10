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
	prs      map[int]RunRow
	recent   []Sample
	recorded map[string]Measured
	marked   map[string]bool
	rowErr   error
	// rowErrAt scopes rowErr to one PR's number, so a lookup for another PR
	// still answers.
	rowErrAt int
	// queries counts the lookups made, so a test can prove one did not happen.
	queries   int
	recordErr error
	recentErr error
	markErr   error
}

func (r *fakeRewrites) RunForMerged(_ context.Context, _ string, pr ClosedPR) (RunRow, bool, error) {
	r.queries++
	if r.rowErr != nil && (r.rowErrAt == 0 || pr.Number == r.rowErrAt) {
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
	// The row carries the measurement once recorded, so a later pass over the
	// same listing measures nothing.
	for branch, row := range r.rows {
		if row.RunID == id {
			row.Measured, row.MergedBy = m.MergedAt, m.MergedBy
			r.rows[branch] = row
		}
	}
	for number, row := range r.prs {
		if row.RunID == id {
			row.Measured, row.MergedBy = m.MergedAt, m.MergedBy
			r.prs[number] = row
		}
	}
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

// fakeEscapes keeps the first escape, as the store does: it holds auto-merge
// off until the owner clears it, so a later one does not replace it.
type fakeEscapes struct {
	escapes []MergeEscape
	err     error
}

func (f *fakeEscapes) RecordEscape(_ context.Context, e MergeEscape) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	if len(f.escapes) > 0 {
		return false, nil
	}
	f.escapes = append(f.escapes, e)
	return true, nil
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

// A listing that carries no merge account is read again for it, so the row
// records who merged the PR rather than reading it as the owner's.
func TestMeasureReadsTheMergeAccountTheListingLacks(t *testing.T) {
	prs := &fakePullRequests{
		closed:  map[string][]ClosedPR{scratch: {{Number: 7, Head: runBranch, MergedAt: mergedAt}}},
		pulls:   map[int]ClosedPR{7: {Number: 7, Head: runBranch, MergedAt: mergedAt, MergedBy: "wingman[bot]"}},
		commits: map[int][]Commit{7: {{Login: runner.CommitAuthorLogin, Additions: 5}}},
	}
	rows := &fakeRewrites{rows: map[string]RunRow{runBranch: {RunID: runOnBranch}}}
	deps, _ := rewriteDeps(prs, rows)
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	if got := rows.recorded[runOnBranch]; got.MergedBy != "wingman[bot]" {
		t.Fatalf("recorded %+v, want the merge account read from the PR", got)
	}
	if len(prs.pulled) != 1 || prs.pulled[0] != 7 {
		t.Fatalf("pulled = %v, want PR 7 read for its merge account", prs.pulled)
	}
}

// A merge account that cannot be read fails the measurement rather than being
// recorded as the owner's, so the next poll retries it.
func TestMeasureFailsWhenTheMergeAccountCannotBeRead(t *testing.T) {
	prs := &fakePullRequests{
		closed:  map[string][]ClosedPR{scratch: {{Number: 7, Head: runBranch, MergedAt: mergedAt}}},
		pullErr: errFirestore,
		commits: map[int][]Commit{7: {{Login: runner.CommitAuthorLogin, Additions: 5}}},
	}
	rows := &fakeRewrites{rows: map[string]RunRow{runBranch: {RunID: runOnBranch}}}
	deps, _ := rewriteDeps(prs, rows)
	err := measureRewrites(context.Background(), deps, oneRepo)
	if !errors.Is(err, errFirestore) || len(rows.recorded) != 0 {
		t.Fatalf("err = %v with %v recorded, want the read's failure and nothing measured", err, rows.recorded)
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

// TestMergeEscapeWordsNamesTheEscapedPR asserts the words the wait reason and
// the PR body carry name the PR that escaped and how (AC8).
func TestMergeEscapeWordsNamesTheEscapedPR(t *testing.T) {
	for name, tt := range map[string]struct {
		escape MergeEscape
		want   string
	}{
		"reverted": {MergeEscape{Repo: "octo/scratch", PR: 341, Reason: EscapeReverted},
			"octo/scratch#341 was reverted within 7 days of merging"},
		"fixed": {MergeEscape{Repo: "octo/scratch", PR: 344, By: 350, Reason: EscapeFixed},
			"octo/scratch#344 was named as fixed by #350 within 7 days of merging"},
	} {
		t.Run(name, func(t *testing.T) {
			got := tt.escape.Words()
			if !strings.Contains(got, tt.want) {
				t.Fatalf("Words = %q, want it to name %q", got, tt.want)
			}
			if !strings.Contains(got, "until you switch it on again") {
				t.Fatalf("Words = %q, want it to say the owner must switch auto-merge on again", got)
			}
		})
	}
}

func TestRevertRecordsTheEscapeOfAnAutoMergedPR(t *testing.T) {
	prs := &fakePullRequests{closed: map[string][]ClosedPR{scratch: {
		{Number: 7, Title: "a run's PR", Head: runBranch, MergedAt: mergedAt},
		{Number: 9, Title: `Revert "a run's PR"`, Body: "Reverts octo/scratch#7\n", MergedAt: mergedAt.Add(time.Hour)},
	}}}
	rows := &fakeRewrites{rows: map[string]RunRow{runBranch: {
		RunID: runOnBranch, MergedBy: "wingman[bot]", Measured: mergedAt}}}
	deps, _ := rewriteDeps(prs, rows)
	escapes := &fakeEscapes{}
	deps.Escapes = escapes
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	want := MergeEscape{Repo: scratch, PR: 7, By: 9, Reason: EscapeReverted, At: mergedAt}
	if len(escapes.escapes) != 1 || escapes.escapes[0] != want {
		t.Fatalf("escapes = %+v, want %+v", escapes.escapes, want)
	}
	if len(prs.pulled) != 0 {
		t.Fatalf("pulled = %v, want the listing's own entry used", prs.pulled)
	}
}

func TestRevertTitledByHandRecordsTheEscape(t *testing.T) {
	prs := &fakePullRequests{closed: map[string][]ClosedPR{scratch: {
		{Number: 7, Title: "a run's PR", Head: runBranch, MergedAt: mergedAt},
		{Number: 9, Title: `Revert "a run's PR"`, MergedAt: mergedAt.Add(time.Hour)},
	}}}
	rows := &fakeRewrites{rows: map[string]RunRow{runBranch: {
		RunID: runOnBranch, MergedBy: "wingman[bot]", Measured: mergedAt}}}
	deps, _ := rewriteDeps(prs, rows)
	escapes := &fakeEscapes{}
	deps.Escapes = escapes
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	if len(escapes.escapes) != 1 || escapes.escapes[0].PR != 7 {
		t.Fatalf("escapes = %+v, want the PR the revert's title quotes", escapes.escapes)
	}
}

func TestRevertOfAPROutsideTheListingIsRead(t *testing.T) {
	prs := &fakePullRequests{
		closed: map[string][]ClosedPR{scratch: {
			{Number: 9, Title: `Revert "a run's PR"`, Body: "Reverts octo/scratch#7", MergedAt: mergedAt.Add(time.Hour)},
		}},
		pulls: map[int]ClosedPR{7: {Number: 7, Head: runBranch, MergedAt: mergedAt}},
	}
	rows := &fakeRewrites{rows: map[string]RunRow{runBranch: {
		RunID: runOnBranch, MergedBy: "wingman[bot]", Measured: mergedAt}}}
	deps, _ := rewriteDeps(prs, rows)
	escapes := &fakeEscapes{}
	deps.Escapes = escapes
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	if len(prs.pulled) != 1 || prs.pulled[0] != 7 || len(escapes.escapes) != 1 {
		t.Fatalf("pulled = %v, escapes = %+v, want PR 7 read and its escape recorded", prs.pulled, escapes.escapes)
	}
}

// A revert of a PR the run's own branch no longer names still records that run's escape.
func TestRevertOfTheRunThePRItNamesBelongsTo(t *testing.T) {
	prs := &fakePullRequests{
		closed: map[string][]ClosedPR{scratch: {
			{Number: 9, Title: `Revert "a run's PR"`, Body: "Reverts octo/scratch#7", MergedAt: mergedAt.Add(time.Hour)},
		}},
		pulls: map[int]ClosedPR{7: {
			Number: 7, Head: "wingman/FRG-1-456-2",
			URL: "https://github.com/octo/scratch/pull/7", MergedAt: mergedAt,
		}},
	}
	rows := &fakeRewrites{prs: map[int]RunRow{7: {
		RunID: runOnBranch, MergedBy: "wingman[bot]", Measured: mergedAt}}}
	deps, _ := rewriteDeps(prs, rows)
	escapes := &fakeEscapes{}
	deps.Escapes = escapes
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	if len(escapes.escapes) != 1 || escapes.escapes[0].PR != 7 {
		t.Fatalf("escapes = %+v, want the run the reverted PR names", escapes.escapes)
	}
}

func TestRevertOfAPRWhenTheRevertNeverMerged(t *testing.T) {
	prs := &fakePullRequests{closed: map[string][]ClosedPR{scratch: {
		{Number: 7, Title: "a run's PR", Head: runBranch, MergedAt: mergedAt},
		{Number: 9, Title: `Revert "a run's PR"`, Body: "Reverts octo/scratch#7"},
	}}}
	rows := &fakeRewrites{rows: map[string]RunRow{runBranch: {
		RunID: runOnBranch, MergedBy: "wingman[bot]", Measured: mergedAt}}}
	deps, _ := rewriteDeps(prs, rows)
	escapes := &fakeEscapes{}
	deps.Escapes = escapes
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	if len(escapes.escapes) != 0 {
		t.Fatalf("escapes = %+v, want a revert that never merged to record nothing", escapes.escapes)
	}
}

func TestRevertAfterTheWindowRecordsNothing(t *testing.T) {
	prs := &fakePullRequests{closed: map[string][]ClosedPR{scratch: {
		{Number: 7, Title: "a run's PR", Head: runBranch, MergedAt: mergedAt},
		{Number: 9, Title: "reverts by hand", Body: "Reverts octo/scratch#7", MergedAt: mergedAt.Add(EscapeWindow + time.Hour)},
	}}}
	rows := &fakeRewrites{rows: map[string]RunRow{runBranch: {
		RunID: runOnBranch, MergedBy: "wingman[bot]", Measured: mergedAt}}}
	deps, _ := rewriteDeps(prs, rows)
	escapes := &fakeEscapes{}
	deps.Escapes = escapes
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	if len(escapes.escapes) != 0 {
		t.Fatalf("escapes = %+v, want a revert past the window to record nothing", escapes.escapes)
	}
}

func TestRevertBodyWithCRLFRecordsTheEscape(t *testing.T) {
	prs := &fakePullRequests{closed: map[string][]ClosedPR{scratch: {
		{Number: 7, Title: "a run's PR", Head: runBranch, MergedAt: mergedAt},
		{Number: 9, Title: "reverts by hand", Body: "Reverts octo/scratch#7\r\n\r\nextra\r\n", MergedAt: mergedAt.Add(time.Hour)},
	}}}
	rows := &fakeRewrites{rows: map[string]RunRow{runBranch: {
		RunID: runOnBranch, MergedBy: "wingman[bot]", Measured: mergedAt}}}
	deps, _ := rewriteDeps(prs, rows)
	escapes := &fakeEscapes{}
	deps.Escapes = escapes
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	if len(escapes.escapes) != 1 {
		t.Fatalf("escapes = %+v, want a CRLF revert body read", escapes.escapes)
	}
}

func TestRevertRecordsTheEscapeOnceAcrossPolls(t *testing.T) {
	// A listing runs most recently updated first, and every poll reads the same
	// revert: the first record holds until the owner clears it.
	prs := &fakePullRequests{closed: map[string][]ClosedPR{scratch: {
		{Number: 9, Title: "reverts by hand", Body: "Reverts octo/scratch#7", MergedAt: mergedAt.Add(time.Hour)},
		{Number: 7, Title: "a run's PR", Head: runBranch, MergedAt: mergedAt, MergedBy: "wingman[bot]"},
	}}}
	rows := &fakeRewrites{rows: map[string]RunRow{runBranch: {RunID: runOnBranch}}}
	deps, _ := rewriteDeps(prs, rows)
	escapes := &fakeEscapes{}
	deps.Escapes = escapes
	for poll := 1; poll <= 2; poll++ {
		if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
			t.Fatal(err)
		}
		if len(escapes.escapes) != 1 {
			t.Fatalf("after poll %d escapes = %+v, want the escape recorded once", poll, escapes.escapes)
		}
	}
}

// A PR that names an auto-merged PR as fixed is the other way one escapes the
// owner's read.
func TestFixPRRecordsTheEscapeOfAnAutoMergedPR(t *testing.T) {
	prs := &fakePullRequests{closed: map[string][]ClosedPR{scratch: {
		{Number: 7, Title: "a run's PR", Head: runBranch, MergedAt: mergedAt},
		{Number: 20, Title: "fix the widget", Head: "wingman/FRG-1-999-1", Body: "Fixes #7", MergedAt: mergedAt.Add(48 * time.Hour)},
	}}}
	rows := &fakeRewrites{prs: map[int]RunRow{
		7: {RunID: runOnBranch, MergedBy: "wingman[bot]", Measured: mergedAt},
	}}
	deps, _ := rewriteDeps(prs, rows)
	escapes := &fakeEscapes{}
	deps.Escapes = escapes
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	want := MergeEscape{Repo: scratch, PR: 7, By: 20, Reason: EscapeFixed, At: mergedAt}
	if len(escapes.escapes) != 1 || escapes.escapes[0] != want {
		t.Fatalf("escapes = %+v, want %+v", escapes.escapes, want)
	}
}

func TestFixPRAfterTheWindowRecordsNothing(t *testing.T) {
	prs := &fakePullRequests{closed: map[string][]ClosedPR{scratch: {
		{Number: 7, Title: "a run's PR", Head: runBranch, MergedAt: mergedAt},
		{Number: 20, Title: "fix the widget", Head: "wingman/FRG-1-999-1", Body: "Fixes #7", MergedAt: mergedAt.Add(EscapeWindow + time.Hour)},
	}}}
	rows := &fakeRewrites{prs: map[int]RunRow{
		7: {RunID: runOnBranch, MergedBy: "wingman[bot]", Measured: mergedAt},
	}}
	deps, _ := rewriteDeps(prs, rows)
	escapes := &fakeEscapes{}
	deps.Escapes = escapes
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	if len(escapes.escapes) != 0 {
		t.Fatalf("escapes = %+v, want a fix past the window to record nothing", escapes.escapes)
	}
}

func TestFixOfAHandMergedPRRecordsNothing(t *testing.T) {
	prs := &fakePullRequests{closed: map[string][]ClosedPR{scratch: {
		{Number: 7, Title: "a run's PR", Head: runBranch, MergedAt: mergedAt},
		{Number: 20, Title: "fix the widget", Head: "wingman/FRG-1-999-1", Body: "Fixes #7", MergedAt: mergedAt.Add(time.Hour)},
	}}}
	rows := &fakeRewrites{prs: map[int]RunRow{
		7: {RunID: runOnBranch, MergedBy: "the-owner", Measured: mergedAt},
	}}
	deps, _ := rewriteDeps(prs, rows)
	escapes := &fakeEscapes{}
	deps.Escapes = escapes
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	if len(escapes.escapes) != 0 {
		t.Fatalf("escapes = %+v, want a merge the owner made to record nothing", escapes.escapes)
	}
}

// A revert only latches when the row proves the PR merged itself: a merge the
// owner made, and a row that names no account, are no escape.
func TestRevertOfAMergeThatDidNotProveItselfRecordsNothing(t *testing.T) {
	for name, mergedBy := range map[string]string{"the owner's login": "the-owner", "no account kept": ""} {
		t.Run(name, func(t *testing.T) {
			prs := &fakePullRequests{closed: map[string][]ClosedPR{scratch: {
				{Number: 7, Title: "a run's PR", Head: runBranch, MergedAt: mergedAt},
				{Number: 9, Title: "reverts by hand", Body: "Reverts octo/scratch#7", MergedAt: mergedAt.Add(time.Hour)},
			}}}
			rows := &fakeRewrites{rows: map[string]RunRow{runBranch: {
				RunID: runOnBranch, MergedBy: mergedBy, Measured: mergedAt}}}
			deps, _ := rewriteDeps(prs, rows)
			escapes := &fakeEscapes{}
			deps.Escapes = escapes
			if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
				t.Fatal(err)
			}
			if len(escapes.escapes) != 0 {
				t.Fatalf("escapes = %+v, want no latch from a merge that did not prove itself", escapes.escapes)
			}
		})
	}
}

// The escape recorder is optional: without one the revert still logs, records
// nothing and fails at nothing.
func TestEscapeNeedsARecorder(t *testing.T) {
	prs := &fakePullRequests{closed: map[string][]ClosedPR{scratch: {
		{Number: 7, Title: "a run's PR", Head: runBranch, MergedAt: mergedAt},
		{Number: 9, Title: "reverts by hand", Body: "Reverts octo/scratch#7", MergedAt: mergedAt.Add(time.Hour)},
	}}}
	rows := &fakeRewrites{rows: map[string]RunRow{runBranch: {
		RunID: runOnBranch, MergedBy: "wingman[bot]", Measured: mergedAt}}}
	deps, _ := rewriteDeps(prs, rows)
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatalf("err = %v, want no recorder to record nothing", err)
	}
}

// A fix reference is not looked up at all while no recorder is wired.
func TestFixEscapesAreSkippedWithoutARecorder(t *testing.T) {
	prs := &fakePullRequests{closed: map[string][]ClosedPR{scratch: {
		{Number: 20, Title: "fix the widget", Body: "Fixes #7", MergedAt: mergedAt.Add(time.Hour)},
	}}}
	rows := &fakeRewrites{}
	deps, _ := rewriteDeps(prs, rows)
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	if rows.queries != 0 {
		t.Fatalf("queries = %d, want no lookup without a recorder", rows.queries)
	}
}

// A fix PR that never merged names no escape: nothing merged later than it.
func TestFixPRThatNeverMergedRecordsNothing(t *testing.T) {
	prs := &fakePullRequests{closed: map[string][]ClosedPR{scratch: {
		{Number: 20, Title: "fix the widget", Body: "Fixes #7"},
	}}}
	rows := &fakeRewrites{prs: map[int]RunRow{
		7: {RunID: runOnBranch, MergedBy: "wingman[bot]", Measured: mergedAt},
	}}
	deps, _ := rewriteDeps(prs, rows)
	escapes := &fakeEscapes{}
	deps.Escapes = escapes
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	if len(escapes.escapes) != 0 {
		t.Fatalf("escapes = %+v, want an unmerged fix to record nothing", escapes.escapes)
	}
}

// A PR naming its own number fixes nothing.
func TestFixPRNamingItselfRecordsNothing(t *testing.T) {
	prs := &fakePullRequests{closed: map[string][]ClosedPR{scratch: {
		{Number: 20, Title: "fix the widget", Body: "Fixes #20", MergedAt: mergedAt.Add(time.Hour)},
	}}}
	rows := &fakeRewrites{prs: map[int]RunRow{
		20: {RunID: "999", MergedBy: "wingman[bot]", Measured: mergedAt.Add(time.Hour)},
	}}
	deps, _ := rewriteDeps(prs, rows)
	escapes := &fakeEscapes{}
	deps.Escapes = escapes
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	if len(escapes.escapes) != 0 {
		t.Fatalf("escapes = %+v, want a self-reference to record nothing", escapes.escapes)
	}
}

// A lookup failure on a fix reference is returned, so the poll retries rather
// than reading the escape as absent.
func TestFixEscapeLookupFailureIsReported(t *testing.T) {
	prs := &fakePullRequests{closed: map[string][]ClosedPR{scratch: {
		{Number: 20, Title: "fix the widget", Body: "Fixes #7", MergedAt: mergedAt.Add(time.Hour)},
	}}}
	rows := &fakeRewrites{rowErr: errFirestore, rowErrAt: 7}
	deps, _ := rewriteDeps(prs, rows)
	deps.Escapes = &fakeEscapes{}
	err := measureRewrites(context.Background(), deps, oneRepo)
	if !errors.Is(err, errFirestore) {
		t.Fatalf("err = %v, want the lookup's failure returned", err)
	}
}

// A recorder that fails is reported, not read as an escape recorded.
func TestEscapeRecordFailureIsReported(t *testing.T) {
	prs := &fakePullRequests{closed: map[string][]ClosedPR{scratch: {
		{Number: 7, Title: "a run's PR", Head: runBranch, MergedAt: mergedAt},
		{Number: 9, Title: "reverts by hand", Body: "Reverts octo/scratch#7", MergedAt: mergedAt.Add(time.Hour)},
	}}}
	rows := &fakeRewrites{rows: map[string]RunRow{runBranch: {
		RunID: runOnBranch, MergedBy: "wingman[bot]", Measured: mergedAt}}}
	deps, _ := rewriteDeps(prs, rows)
	deps.Escapes = &fakeEscapes{err: errFirestore}
	err := measureRewrites(context.Background(), deps, oneRepo)
	if !errors.Is(err, errFirestore) {
		t.Fatalf("err = %v, want the recorder's failure returned", err)
	}
}

func TestRevertOfAPRThatNeverMergedRecordsNothing(t *testing.T) {
	prs := &fakePullRequests{closed: map[string][]ClosedPR{scratch: {
		{Number: 7, Title: "a run's PR", Head: runBranch},
		{Number: 9, Title: "reverts by hand", Body: "Reverts octo/scratch#7", MergedAt: mergedAt},
	}}}
	rows := &fakeRewrites{rows: map[string]RunRow{runBranch: {
		RunID: runOnBranch, MergedBy: "wingman[bot]"}}}
	deps, _ := rewriteDeps(prs, rows)
	escapes := &fakeEscapes{}
	deps.Escapes = escapes
	if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
		t.Fatal(err)
	}
	if len(escapes.escapes) != 0 {
		t.Fatalf("escapes = %+v, want a revert of an unmerged PR to record nothing", escapes.escapes)
	}
}

func TestRevertsThatNameNoRunPRRecordNothing(t *testing.T) {
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
			rows := &fakeRewrites{rows: map[string]RunRow{
				runBranch:         {RunID: runOnBranch, MergedBy: "wingman[bot]", Measured: mergedAt},
				"feature/by-hand": {RunID: "other", MergedBy: "wingman[bot]", Measured: mergedAt},
			}}
			deps, _ := rewriteDeps(prs, rows)
			escapes := &fakeEscapes{}
			deps.Escapes = escapes
			if err := measureRewrites(context.Background(), deps, oneRepo); err != nil {
				t.Fatal(err)
			}
			if len(escapes.escapes) != 0 {
				t.Fatalf("escapes = %+v, want the revert to record nothing", escapes.escapes)
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
