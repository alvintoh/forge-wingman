package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

// The rewrite measurement the dispatcher writes onto a run row, beside
// runner.Record rather than inside it: the record job rebuilds the fields Record
// names, so a measurement kept among them would be wiped by the next attempt's
// Finalize. The row's own repo, branch, auto_merge, failed_gate and pr_url are
// read with them, so one query builds a whole sample.
const (
	rewriteAgentLinesField  = "rewrite_agent_lines"
	rewriteOwnerLinesField  = "rewrite_owner_lines"
	rewriteReviewFoundField = "rewrite_review_found_lines"
	rewriteOwnerChangeField = "rewrite_owner_change_lines"
	rewriteUnlabelledField  = "rewrite_unlabelled_lines"
	prNumberField           = "pr_number"
	prTitleField            = "pr_title"
	rewriteUnmeasuredField  = "rewrite_unmeasured_commits"
	rewriteMergedAtField    = "rewrite_merged_at"
	rewriteRevertedField    = "rewrite_reverted"
	branchField             = "branch"
	autoMergeField          = "auto_merge"
	failedGateField         = "failed_gate"
	prURLField              = "pr_url"
)

// Rewrites records each merged agent PR's measured lines on its run row and
// reads back the samples the autonomy level's conditions are checked against.
type Rewrites struct {
	client *firestore.Client
}

// NewRewrites returns a Rewrites backed by client.
func NewRewrites(client *firestore.Client) *Rewrites {
	return &Rewrites{client: client}
}

// RunByBranch returns the run row a PR's head branch belongs to, or false when
// no row carries that branch: a branch no run pushed, or a row removed since.
func (r *Rewrites) RunByBranch(ctx context.Context, branch string) (dispatcher.RunRow, bool, error) {
	snaps, err := r.client.Collection(runsCollection).Where(branchField, "==", branch).Limit(1).Documents(ctx).GetAll()
	if err != nil {
		return dispatcher.RunRow{}, false, fmt.Errorf("reading the run on branch %s: %w", branch, err)
	}
	if len(snaps) == 0 {
		return dispatcher.RunRow{}, false, nil
	}
	row := dispatcher.RunRow{RunID: snaps[0].Ref.ID}
	if at, ok := snaps[0].Data()[rewriteMergedAtField].(time.Time); ok {
		row.Measured = at
	}
	return row, true, nil
}

// Record writes m onto run id's row, clearing any revert a previous merge of the
// row was flagged with: the measurement is of the merge the row now carries.
func (r *Rewrites) Record(ctx context.Context, id string, m dispatcher.Measured) error {
	_, err := r.client.Collection(runsCollection).Doc(id).Set(ctx, map[string]any{
		rewriteAgentLinesField:  m.AgentLines,
		rewriteOwnerLinesField:  m.OwnerLines,
		rewriteReviewFoundField: m.ReviewFoundLines,
		rewriteOwnerChangeField: m.OwnerChangeLines,
		rewriteUnlabelledField:  m.UnlabelledLines,
		rewriteUnmeasuredField:  m.UnmeasuredCommits,
		prNumberField:           int64(m.PR),
		prTitleField:            titleText(m.Title),
		rewriteMergedAtField:    m.MergedAt,
		rewriteRevertedField:    false,
	}, firestore.MergeAll)
	if err != nil {
		return fmt.Errorf("recording run %s's rewrite: %w", id, err)
	}
	return nil
}

// FlagReverted flags run id's row as one a merged revert names.
func (r *Rewrites) FlagReverted(ctx context.Context, id string) error {
	_, err := r.client.Collection(runsCollection).Doc(id).Set(ctx,
		map[string]any{rewriteRevertedField: true}, firestore.MergeAll)
	if err != nil {
		return fmt.Errorf("flagging run %s reverted: %w", id, err)
	}
	return nil
}

// Recent returns the merged PRs' samples, newest merge first, up to limit.
func (r *Rewrites) Recent(ctx context.Context, limit int) ([]dispatcher.Sample, error) {
	return recentSamples(ctx, r.client, limit)
}

// recentSamples reads the rows a rewrite measurement sits on, newest merge
// first. The measurement fields are the store's own, so the range filter is what
// tells a measured row from one no merge has reached.
func recentSamples(ctx context.Context, client *firestore.Client, limit int) ([]dispatcher.Sample, error) {
	iter := client.Collection(runsCollection).
		Where(rewriteMergedAtField, ">", time.Time{}).
		OrderBy(rewriteMergedAtField, firestore.Desc).
		Limit(limit).Documents(ctx)
	defer iter.Stop()
	var out []dispatcher.Sample
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("listing the measured runs: %w", err)
		}
		out = append(out, sample(snap.Ref.ID, snap.Data()))
	}
	return out, nil
}

// sample reads one measured row, id, as a condition sample. A field a row does
// not carry reads as its zero, which withholds a level rather than granting one.
func sample(id string, row map[string]any) dispatcher.Sample {
	autoMerged, _ := row[autoMergeField].(bool)
	reverted, _ := row[rewriteRevertedField].(bool)
	s := dispatcher.Sample{
		Run:        id,
		Size:       str(row, sizeField),
		Repo:       str(row, repoField),
		PRURL:      str(row, prURLField),
		Title:      str(row, prTitleField),
		AutoMerged: autoMerged,
		Reverted:   reverted,
		GateMiss:   str(row, failedGateField) != "",
	}
	if at, ok := row[rewriteMergedAtField].(time.Time); ok {
		s.MergedAt = at
	}
	s.Rewrite, _ = rewriteOf(row)
	pr, _ := row[prNumberField].(int64)
	s.PR = int(pr)
	return s
}

// rewriteOf reads the rewrite measurement a run row carries, and whether a merge
// has written one: the field the measurement keeps its merge time in is what
// tells a measured row from one no merge has reached.
func rewriteOf(row map[string]any) (dispatcher.Rewrite, bool) {
	var r dispatcher.Rewrite
	r.AgentLines, _ = row[rewriteAgentLinesField].(int64)
	r.OwnerLines, _ = row[rewriteOwnerLinesField].(int64)
	r.ReviewFoundLines, _ = row[rewriteReviewFoundField].(int64)
	r.OwnerChangeLines, _ = row[rewriteOwnerChangeField].(int64)
	r.UnlabelledLines, _ = row[rewriteUnlabelledField].(int64)
	r.UnmeasuredCommits, _ = row[rewriteUnmeasuredField].(int64)
	_, measured := row[rewriteMergedAtField].(time.Time)
	return r, measured
}

// prTitleLimit is the most bytes of a PR's title a run row stores.
const prTitleLimit = 200

// titleText returns a PR's title as a run row stores it: on one line and within
// prTitleLimit bytes, so a page draws no break and never a partial character.
func titleText(title string) string {
	line := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, title)
	if len(line) <= prTitleLimit {
		return line
	}
	n := prTitleLimit
	for n > 0 && !utf8.RuneStart(line[n]) {
		n--
	}
	return line[:n]
}
