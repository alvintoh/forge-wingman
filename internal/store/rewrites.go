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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
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
	branchesField           = "branches"
	autoMergeField          = "auto_merge"
	prURLField              = "pr_url"
	planModelField          = "plan_model"
	stepsField              = "steps"
)

// Rewrites records each merged agent PR's measured lines on its run row and
// reads back the samples the auto-merge rule is decided from.
type Rewrites struct {
	client *firestore.Client
}

// NewRewrites returns a Rewrites backed by client.
func NewRewrites(client *firestore.Client) *Rewrites {
	return &Rewrites{client: client}
}

// RunForMerged returns the run row the merged PR pr belongs to, or false when no
// row does: a branch no run pushed, or a row removed since.
//
// The PR names its run five ways: the row's own branch, the branches its earlier
// attempts pushed, the page of the PR it opened, the number of that PR, and the
// number of the PR a supersede of it replaced. Each read names one field, which
// Firestore indexes by itself. A branch carries the run's GitHub id and a page
// carries its repository, so neither can name another repository's run; a number
// names a pull request of every repository, so those reads settle the repository
// here.
func (r *Rewrites) RunForMerged(ctx context.Context, repo string, pr dispatcher.ClosedPR) (dispatcher.RunRow, bool, error) {
	coll := r.client.Collection(runsCollection)
	var queries []firestore.Query
	if pr.Head != "" {
		queries = append(queries,
			coll.Where(branchField, "==", pr.Head),
			coll.Where(branchesField, "array-contains", pr.Head),
		)
	}
	if pr.URL != "" {
		queries = append(queries, coll.Where(prURLField, "==", pr.URL))
	}
	for _, q := range queries {
		snaps, err := q.Limit(1).Documents(ctx).GetAll()
		if err != nil {
			return dispatcher.RunRow{}, false, fmt.Errorf("reading the run of merged %s#%d: %w", repo, pr.Number, err)
		}
		if len(snaps) > 0 {
			return runRow(snaps[0]), true, nil
		}
	}
	if pr.Number != 0 {
		for _, field := range []string{prNumberField, supersededPRField} {
			snaps, err := coll.Where(field, "==", int64(pr.Number)).Documents(ctx).GetAll()
			if err != nil {
				return dispatcher.RunRow{}, false, fmt.Errorf("reading the run of merged %s#%d: %w", repo, pr.Number, err)
			}
			for _, snap := range snaps {
				if str(snap.Data(), repoField) == repo {
					return runRow(snap), true, nil
				}
			}
		}
	}
	return dispatcher.RunRow{}, false, nil
}

// runRow reads the row a query matched: what it is called, and when a merge last
// measured it.
func runRow(snap *firestore.DocumentSnapshot) dispatcher.RunRow {
	row := dispatcher.RunRow{RunID: snap.Ref.ID}
	if at, ok := snap.Data()[rewriteMergedAtField].(time.Time); ok {
		row.Measured = at
	}
	return row
}

// The marker a merged PR with no run row is reported under, and when the poll
// that found it did.
const (
	unfoundPrefix  = "unfound-"
	unfoundAtField = "unfound_at"
)

// MarkUnfound records that merged PR number of repo has no run row, reporting
// whether this poll is the first to record it: a PR with no row at all keeps
// having none, and every poll that reads the listing finds it again.
func (r *Rewrites) MarkUnfound(ctx context.Context, repo string, number int, at time.Time) (bool, error) {
	doc := r.client.Collection(dispatchCollection).Doc(unfoundDocID(repo, number))
	fresh := false
	err := r.client.RunTransaction(ctx, func(_ context.Context, tx *firestore.Transaction) error {
		fresh = false
		if _, err := tx.Get(doc); err == nil {
			return nil
		} else if status.Code(err) != codes.NotFound {
			return err
		}
		fresh = true
		return tx.Set(doc, map[string]any{unfoundAtField: at})
	})
	if err != nil {
		return false, fmt.Errorf("marking %s#%d as having no run: %w", repo, number, err)
	}
	return fresh, nil
}

// unfoundDocID names the marker of one repository's unfound pull request: a
// document id cannot carry the slash in a repository's own name, and a
// repository's name cannot carry the # it is joined with.
func unfoundDocID(repo string, number int) string {
	return fmt.Sprintf("%s%s#%d", unfoundPrefix, strings.ReplaceAll(repo, "/", "#"), number)
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
// first, projected to the fields a sample reads and the steps its phase models
// come from. The measurement fields are the store's own, so the range filter is
// what tells a measured row from one no merge has reached.
func recentSamples(ctx context.Context, client *firestore.Client, limit int) ([]dispatcher.Sample, error) {
	iter := client.Collection(runsCollection).
		Where(rewriteMergedAtField, ">", time.Time{}).
		OrderBy(rewriteMergedAtField, firestore.Desc).
		Select(sizeField, repoField, prURLField, prTitleField, autoMergeField, prNumberField,
			rewriteMergedAtField, rewriteRevertedField, rewriteAgentLinesField, rewriteOwnerLinesField,
			rewriteReviewFoundField, rewriteOwnerChangeField, rewriteUnlabelledField, rewriteUnmeasuredField,
			planModelField, stepsField).
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
		var run struct {
			Steps []runner.Step `firestore:"steps"`
		}
		if err := snap.DataTo(&run); err != nil {
			return nil, fmt.Errorf("decoding the steps of run %s: %w", snap.Ref.ID, err)
		}
		out = append(out, sample(snap.Ref.ID, snap.Data(), run.Steps))
	}
	return out, nil
}

// sample reads one measured row, id, as the auto-merge rule reads it; steps are
// the row's own, which the build and review models come from.
func sample(id string, row map[string]any, steps []runner.Step) dispatcher.Sample {
	autoMerged, _ := row[autoMergeField].(bool)
	reverted, _ := row[rewriteRevertedField].(bool)
	s := dispatcher.Sample{
		Run:         id,
		Size:        str(row, sizeField),
		Repo:        str(row, repoField),
		PRURL:       str(row, prURLField),
		Title:       str(row, prTitleField),
		PlanModel:   str(row, planModelField),
		BuildModel:  runner.PhaseModel(steps, runner.PhaseBuild),
		ReviewModel: runner.PhaseModel(steps, runner.PhaseReview),
		AutoMerged:  autoMerged,
		Reverted:    reverted,
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
