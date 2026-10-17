package store

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/money"
	prov "github.com/alvintoh/forge-wingman/internal/providers"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// pollAt is the moment the polls here admit at, far enough ahead that no row the
// rest of the suite leaves in the shared runs collection — each settled at or
// near a real clock — falls inside a window these polls measure.
var pollAt = time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)

// pollRepo is the repository the candidates here name.
const pollRepo = "octo/scratch"

// pollPlan is the provider plan the candidates and the seeded runs here are on:
// a name of this test file's own, so the plan-less rows other tests leave behind
// are on no plan these polls sum.
const pollPlan = "poll-plan"

// pollModel is the build model the candidates name, on pollPlan.
const pollModel = pollPlan + "/model"

// pollBudget is the budget the polls here admit against: the window a seeded run
// can fill, and a cash ceiling wide enough never to bind first.
var pollBudget = dispatcher.BudgetConfig{
	ProviderWindows: []dispatcher.Window{{Name: windowName, Period: 5 * time.Hour, Limit: 4 * money.Dollar}},
	Cash:            dispatcher.Window{Name: dispatcher.CeilingCash, Calendar: true, Limit: 1000 * money.Dollar},
	Runner:          dispatcher.RunnerMinutes{FreeMinutes: 1_000_000},
}

// pollReadsQueue is a Queue over a client that counts the documents its reads
// return.
func pollReadsQueue(t *testing.T) (*Queue, *atomic.Int64) {
	t.Helper()
	reads := new(atomic.Int64)
	client := countingClient(t, reads, new(atomic.Int64), new(atomic.Int64), new(atomic.Int64), false)
	return NewQueue(client), reads
}

// pollCandidates enqueues n queued runs of the common size on pollPlan, one
// priority each, and returns their ids.
func pollCandidates(t *testing.T, q *Queue, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for rank := 1; rank <= n; rank++ {
		id := fresh("poll-candidate")
		// A row an interrupted earlier run left behind is not this run's.
		forget(t, q.client, id)
		run := queuedRun(id, rank)
		run.Models = runner.ModelLabels{Build: pollModel}
		ids = append(ids, run.RunID)
		if err := q.Enqueue(context.Background(), run); err != nil {
			t.Fatal(err)
		}
	}
	return ids
}

// pollHistory seeds n runs of size on pollPlan, each settled an hour before the
// polls here admit at, every third of them having planned as well: a settled run
// history inside every window these polls measure.
func pollHistory(t *testing.T, q *Queue, n int, size string) {
	t.Helper()
	for i := range n {
		id := fresh("poll-settled")
		planned := time.Time{}
		if i%3 == 0 {
			planned = pollAt.Add(-time.Hour)
		}
		historyRow(t, q.client, id, pollPlan, pollAt.Add(-time.Hour), planned, money.Dollar, money.Dollar)
		if _, err := q.client.Collection(runsCollection).Doc(id).Update(context.Background(),
			[]firestore.Update{{Path: sizeField, Value: size}}); err != nil {
			t.Fatal(err)
		}
	}
}

// pollReadDeps is a poll over the store's queue and estimator, with every other
// dependency stubbed: the reads it makes are the queue's own.
func pollReadDeps(q *Queue, openPRs int) dispatcher.Deps {
	return dispatcher.Deps{
		Source:       pollSource{},
		Delegatable:  pollDelegatable{},
		Queue:        q,
		Estimator:    NewEstimates(q.client),
		Visibility:   pollVisibility{},
		Providers:    pollProviders{},
		Breaker:      pollBreaker{},
		PeakModes:    pollPeakModes{},
		Autonomies:   pollAutonomies{},
		OpenPRs:      pollOpenPRs{count: openPRs},
		PullRequests: pollPullRequests{},
		Rewrites:     pollRewrites{},
		Earned:       pollEarned{},
		Shadow:       pollShadow{},
		CDs:          pollCDs{},
		History:      pollHistoryStore{},
		Workflow:     pollWorkflow{},
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:          func() time.Time { return pollAt },
		Overrides:    dispatcher.LabelOverrides{},
		ModelPlans:   pollModelPlans{},
		Notices:      pollNotices{},
		OpenPoster:   func(context.Context) (dispatcher.Poster, error) { return pollPoster{}, nil },
		Elicit:       pollElicit{},
		Decisions:    q,
	}
}

// pollReadConfig is the boundary the polls here admit inside.
func pollReadConfig() dispatcher.Config {
	return dispatcher.Config{Repos: []string{pollRepo}, Budget: pollBudget, Model: runner.DefaultModel()}
}

// A poll reads a bounded number of documents however many candidates it walks:
// twelve queued candidates cost the poll one read of the settled history, one
// read of each of their rows and the ledger, and one estimate of their size, so
// a day of 15-minute polls (about 100 of them) stays well inside the free read
// quota (AC3, AC1).
func TestAPollReadsUnderThreeHundredDocumentsForTwelveCandidates(t *testing.T) {
	q, reads := pollReadsQueue(t)
	resetLedger(t, q.client, 1)
	pollHistory(t, q, 20, "M")
	ids := pollCandidates(t, q, 12)
	// The seeded run fills the 5h window, so every candidate is withheld on it
	// rather than claimed: the walk still reads each one's ceilings.
	res, err := dispatcher.Poll(context.Background(), pollReadDeps(q, 0), pollReadConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deferrals) != len(ids) {
		t.Fatalf("deferred %d of %d candidates, want every one withheld", len(res.Deferrals), len(ids))
	}
	for _, d := range res.Deferrals {
		if d.Ceiling != windowName {
			t.Fatalf("candidate %s deferred on %q, want %q, the window the settled runs fill", d.RunID, d.Ceiling, windowName)
		}
	}
	if got := reads.Load(); got > 300 {
		t.Fatalf("one poll read %d documents for %d candidates, want at most 300", got, len(ids))
	}
}

// A poll whose candidates the review WIP already holds defers them without
// reading the settled run history at all (AC5): the same walk against a seeded
// history costs the reads its rows and its estimates alone.
func TestAPollHeldByTheReviewWIPReadsNoSettledHistory(t *testing.T) {
	q, reads := pollReadsQueue(t)
	resetLedger(t, q.client, 1)
	// A history of a size no candidate uses, so the estimates read none of it
	// and only the claim's snapshot could.
	pollHistory(t, q, 100, "L")
	ids := pollCandidates(t, q, 12)
	res, err := dispatcher.Poll(context.Background(), pollReadDeps(q, dispatcher.DefaultLimits.ReviewWIP), pollReadConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deferrals) != len(ids) {
		t.Fatalf("deferred %d of %d candidates, want every one withheld", len(res.Deferrals), len(ids))
	}
	for _, d := range res.Deferrals {
		if d.Ceiling != dispatcher.ConditionReviewWIP {
			t.Fatalf("candidate %s deferred on %q, want the review WIP", d.RunID, d.Ceiling)
		}
	}
	// The seeded 100 runs would cost this poll about one read each were the
	// snapshot loaded for any candidate — 100 settled, plus the 34 of them that
	// planned — where the walk's own reads are its candidates, their rows and the
	// ledger, and their estimates: 150 leaves room for the rows a concurrently
	// running package leaves in the shared collection without admitting the
	// history's.
	if got := reads.Load(); got > 150 {
		t.Fatalf("one poll read %d documents with every candidate held by the review WIP, want none of the seeded history", got)
	}
}

// pollSource hands over no delegated issue: these polls walk the queue alone.
type pollSource struct{}

func (pollSource) Delegated(context.Context) ([]dispatcher.Issue, error) { return nil, nil }

// pollDelegatable offers nothing to delegate itself.
type pollDelegatable struct{}

func (pollDelegatable) Open(context.Context) ([]dispatcher.Issue, error) {
	return nil, nil
}
func (pollDelegatable) DelegateIssue(context.Context, string) error {
	return nil
}

// pollVisibility reports every repository public, which no poll here checks.
type pollVisibility struct{}

func (pollVisibility) Private(context.Context, string) (bool, error) { return false, nil }

// pollProviders reports no halted provider.
type pollProviders struct{}

func (pollProviders) Halted(context.Context, string) (bool, error) { return false, nil }

// pollBreaker reports a breaker that is not tripped.
type pollBreaker struct{}

func (pollBreaker) Tripped(context.Context) (bool, error) { return false, nil }

// pollPeakModes reports off-peak-only, which holds no candidate.
type pollPeakModes struct{}

func (pollPeakModes) Mode(context.Context) (dispatcher.PeakMode, error) {
	return dispatcher.PeakOffPeakOnly, nil
}

// pollAutonomies reports the lowest level: it delegates nothing, so a poll here
// has no delegation phase and no earned level to resolve.
type pollAutonomies struct{}

func (pollAutonomies) Level(context.Context) (dispatcher.Autonomy, error) {
	return 0, nil
}
func (pollAutonomies) Set(context.Context, dispatcher.Autonomy, time.Time) error {
	return nil
}

// pollOpenPRs reports the configured count of open agent PRs, the review WIP.
type pollOpenPRs struct{ count int }

func (p pollOpenPRs) OpenAgentPRs(context.Context, string) (int, error) { return p.count, nil }

func (p pollOpenPRs) PullOpen(context.Context, string, int) (bool, error) { return false, nil }

// pollPullRequests reports no closed pull request, so no merge is measured.
type pollPullRequests struct{}

func (pollPullRequests) Closed(context.Context, string) ([]dispatcher.ClosedPR, error) {
	return nil, nil
}
func (pollPullRequests) Pull(context.Context, string, int) (dispatcher.ClosedPR, error) {
	return dispatcher.ClosedPR{}, nil
}
func (pollPullRequests) Commits(context.Context, string, int) ([]dispatcher.Commit, error) {
	return nil, nil
}

// pollRewrites records nothing and reads no sample.
type pollRewrites struct{}

func (pollRewrites) RunForMerged(context.Context, string, dispatcher.ClosedPR) (dispatcher.RunRow, bool, error) {
	return dispatcher.RunRow{}, false, nil
}
func (pollRewrites) MarkUnfound(context.Context, string, int, time.Time) (bool, error) {
	return false, nil
}
func (pollRewrites) Record(context.Context, string, dispatcher.Measured) error {
	return nil
}
func (pollRewrites) FlagReverted(context.Context, string) error {
	return nil
}
func (pollRewrites) Recent(context.Context, int) ([]dispatcher.Sample, error) {
	return nil, nil
}

// pollEarned stores no earned snapshot, which no poll here reads.
type pollEarned struct{}

func (pollEarned) Earned(context.Context) (dispatcher.Earned, bool, error) {
	return dispatcher.Earned{}, false, nil
}
func (pollEarned) SetEarned(context.Context, dispatcher.Earned) error {
	return nil
}
func (pollEarned) MarkStale(context.Context) error {
	return nil
}

// pollShadow counts no merge and began long before these polls.
type pollShadow struct{}

func (pollShadow) ShadowStart(context.Context, time.Time) (time.Time, error) {
	return pollAt.Add(-24 * time.Hour), nil
}
func (pollShadow) CountShadow(context.Context) (int, error) { return 0, nil }

// pollCDs records no CD flag.
type pollCDs struct{}

func (pollCDs) Recorded(context.Context) (map[string]bool, error) { return nil, nil }

// pollHistoryStore records no level change.
type pollHistoryStore struct{}

func (pollHistoryStore) Append(context.Context, dispatcher.Change) error { return nil }

// pollWorkflow starts every run it is handed.
type pollWorkflow struct{}

func (pollWorkflow) Dispatch(context.Context, dispatcher.Claim) error { return nil }

// pollModelPlans reports every provider's plan as usable.
type pollModelPlans struct{}

func (pollModelPlans) Plan(context.Context, string) (prov.Plan, bool, error) {
	return prov.Plan{}, true, nil
}

// pollNotices raises and posts nothing.
type pollNotices struct{}

func (pollNotices) Raise(context.Context, dispatcher.Notice, time.Time) error {
	return nil
}
func (pollNotices) Claim(context.Context, time.Time, time.Time) ([]dispatcher.Notice, error) {
	return nil, nil
}
func (pollNotices) Posted(context.Context, string, time.Time) error {
	return nil
}
func (pollNotices) Unclaim(context.Context, string) error {
	return nil
}

// pollPoster posts no notice; nothing here claims one.
type pollPoster struct{}

func (pollPoster) Post(context.Context, dispatcher.Notice) error { return nil }

// pollElicit asks no decision, which nothing waiting here needs.
type pollElicit struct{}

func (pollElicit) Elicit(context.Context, string, runner.Decision) error { return nil }

func (pollElicit) ElicitProvisional(context.Context, string, runner.Decision, string) error {
	return nil
}
