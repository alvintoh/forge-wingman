package dispatcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

var checksAt = time.Date(2026, 10, 29, 3, 0, 0, 0, time.UTC)

const checksPR = "https://github.com/octo/scratch/pull/7"

type checksRecord struct{ id, prURL, sha, result string }

// fakeChecksRuns holds the rows a test seeds, and leaves a settled one out of
// the next listing as the store does.
type fakeChecksRuns struct {
	runs      []UnsettledRun
	listErr   error
	recordErr error
	// stale makes RecordChecks decline a row, as the store does one that moved on.
	stale    bool
	limit    int
	deadline time.Time
	recorded []checksRecord
}

func (f *fakeChecksRuns) UnsettledRuns(ctx context.Context, limit int) ([]UnsettledRun, error) {
	f.limit = limit
	f.deadline, _ = ctx.Deadline()
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []UnsettledRun
	for _, run := range f.runs {
		if !slices.ContainsFunc(f.recorded, func(r checksRecord) bool { return r.id == run.ID }) {
			out = append(out, run)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeChecksRuns) RecordChecks(_ context.Context, id, prURL, sha, result string) (bool, error) {
	if f.recordErr != nil {
		return false, f.recordErr
	}
	if f.stale {
		return false, nil
	}
	f.recorded = append(f.recorded, checksRecord{id, prURL, sha, result})
	return true, nil
}

type outcomeCall struct {
	repo, sha string
	required  []string
}

// fakeChecksGitHub answers every pull request with the same commits.
type fakeChecksGitHub struct {
	commits       []PullCommit
	commitsByPR   map[int][]PullCommit
	required      []string
	outcome       string
	commitsErr    error
	requiredErr   error
	outcomeErr    error
	requiredCalls []string
	outcomeCalls  []outcomeCall
	commitCalls   int
	onCommits     func()
}

func (f *fakeChecksGitHub) PullCommits(_ context.Context, _ string, number int) ([]PullCommit, error) {
	f.commitCalls++
	if f.onCommits != nil {
		f.onCommits()
	}
	if commits, ok := f.commitsByPR[number]; ok {
		return commits, f.commitsErr
	}
	return f.commits, f.commitsErr
}

func (f *fakeChecksGitHub) RequiredChecks(_ context.Context, repo, branch string) ([]string, error) {
	f.requiredCalls = append(f.requiredCalls, repo+"@"+branch)
	return f.required, f.requiredErr
}

func (f *fakeChecksGitHub) ChecksOutcome(_ context.Context, repo, sha string, required []string) (string, error) {
	f.outcomeCalls = append(f.outcomeCalls, outcomeCall{repo, sha, required})
	return f.outcome, f.outcomeErr
}

// agentThenOwner is a pull request whose agent commit the owner built on.
func agentThenOwner() []PullCommit {
	return []PullCommit{
		{SHA: "agent1", Login: runner.CommitAuthorLogin},
		{SHA: "owner1", Login: "alvintoh"},
	}
}

func checksFixture(outcome string, runs ...UnsettledRun) (Deps, *fakeChecksRuns, *fakeChecksGitHub, *bytes.Buffer) {
	if len(runs) == 0 {
		runs = []UnsettledRun{{ID: "run-1", PRURL: checksPR, StartedAt: checksAt.Add(-time.Hour)}}
	}
	store := &fakeChecksRuns{runs: runs}
	gh := &fakeChecksGitHub{commits: agentThenOwner(), required: []string{"go", "web"}, outcome: outcome}
	var logs bytes.Buffer
	d := Deps{
		Checks: ChecksDeps{Runs: store, GitHub: gh, Branch: "main"},
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
		Now:    func() time.Time { return checksAt },
	}
	return d, store, gh, &logs
}

func TestSettleChecksRecordsTheResultOnTheAgentsHeadOnceItSettles(t *testing.T) {
	for _, outcome := range []string{CheckGreen, CheckFailing} {
		t.Run(outcome, func(t *testing.T) {
			d, store, gh, _ := checksFixture(outcome)
			if err := settleChecks(context.Background(), d); err != nil {
				t.Fatal(err)
			}
			want := []checksRecord{{"run-1", checksPR, "agent1", outcome}}
			if !slices.Equal(store.recorded, want) {
				t.Fatalf("recorded %+v, want %+v", store.recorded, want)
			}
			if got := gh.outcomeCalls[0]; got.repo != "octo/scratch" || !slices.Equal(got.required, []string{"go", "web"}) {
				t.Fatalf("outcome read %+v, want the repository and required checks of the run", got)
			}
		})
	}
}

func TestSettleChecksReadsTheAgentsLastCommitNotTheOwnersNorAnEarlierAgentOne(t *testing.T) {
	d, store, _, _ := checksFixture(CheckGreen)
	d.Checks.GitHub.(*fakeChecksGitHub).commits = []PullCommit{
		{SHA: "agent1", Login: runner.CommitAuthorLogin},
		{SHA: "agent2", Login: strings.ToUpper(runner.CommitAuthorLogin)},
		{SHA: "owner1", Login: "alvintoh"},
		{SHA: "owner2"},
	}
	if err := settleChecks(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if len(store.recorded) != 1 || store.recorded[0].sha != "agent2" {
		t.Fatalf("recorded %+v, want the agent's last commit agent2", store.recorded)
	}
}

func TestSettleChecksWritesNothingWhileAnyRequiredCheckIsPending(t *testing.T) {
	d, store, _, logs := checksFixture(CheckPending)
	if err := settleChecks(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if len(store.recorded) != 0 {
		t.Fatalf("recorded %+v for a pending run", store.recorded)
	}
	if strings.Contains(logs.String(), "checksPendingStale") {
		t.Fatalf("a run an hour old was reported stale: %s", logs)
	}
}

func TestSettleChecksSaysSoWhenAChecksResultIsPendingPastADay(t *testing.T) {
	d, store, _, logs := checksFixture(CheckPending,
		UnsettledRun{ID: "run-1", PRURL: checksPR, StartedAt: checksAt.Add(-25 * time.Hour)})
	if err := settleChecks(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "checksPendingStale") || !strings.Contains(logs.String(), "run=run-1") {
		t.Fatalf("logs = %s, want checksPendingStale for run-1", logs)
	}
	if len(store.recorded) != 0 {
		t.Fatalf("recorded %+v for a pending run", store.recorded)
	}
}

func TestSettleChecksCallsAPendingResultStaleOnlyPastADay(t *testing.T) {
	for _, tt := range []struct {
		age   time.Duration
		stale bool
	}{{23 * time.Hour, false}, {24 * time.Hour, false}, {24*time.Hour + time.Minute, true}} {
		t.Run(tt.age.String(), func(t *testing.T) {
			d, _, _, logs := checksFixture(CheckPending,
				UnsettledRun{ID: "run-1", PRURL: checksPR, StartedAt: checksAt.Add(-tt.age)})
			if err := settleChecks(context.Background(), d); err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(logs.String(), "checksPendingStale"); got != tt.stale {
				t.Fatalf("stale reported = %v for a run %v old, want %v", got, tt.age, tt.stale)
			}
		})
	}
}

func TestSettleChecksLeavesTheRowAndNeverWritesFailingWhenAReadFails(t *testing.T) {
	for name, fail := range map[string]func(*fakeChecksGitHub){
		"the commit listing fails": func(g *fakeChecksGitHub) { g.commitsErr = errors.New("502") },
		"the required checks fail": func(g *fakeChecksGitHub) { g.requiredErr = errors.New("rules read") },
		"the checks outcome fails": func(g *fakeChecksGitHub) { g.outcomeErr = errors.New("checks read") },
	} {
		t.Run(name, func(t *testing.T) {
			d, store, _, logs := checksFixture(CheckFailing)
			fail(d.Checks.GitHub.(*fakeChecksGitHub))
			if err := settleChecks(context.Background(), d); err != nil {
				t.Fatalf("a read failing for one run failed the phase: %v", err)
			}
			if len(store.recorded) != 0 {
				t.Fatalf("recorded %+v after a failed read", store.recorded)
			}
			if !strings.Contains(logs.String(), "checksReadFailed") || !strings.Contains(logs.String(), "run=run-1") {
				t.Fatalf("logs = %s, want checksReadFailed for run-1", logs)
			}
		})
	}
}

func TestSettleChecksMarksARunWhoseAgentWroteNoCommitUnresolvedOnce(t *testing.T) {
	d, store, gh, logs := checksFixture(CheckGreen)
	gh.commits = []PullCommit{{SHA: "owner1", Login: "alvintoh"}}
	for range 2 {
		if err := settleChecks(context.Background(), d); err != nil {
			t.Fatal(err)
		}
	}
	if want := []checksRecord{{"run-1", checksPR, "", CheckUnresolved}}; !slices.Equal(store.recorded, want) {
		t.Fatalf("recorded %+v, want %+v with no head sha", store.recorded, want)
	}
	if len(gh.outcomeCalls) != 0 || gh.commitCalls != 1 {
		t.Fatalf("read %d outcomes and listed commits %d times, want no outcome and the commits once", len(gh.outcomeCalls), gh.commitCalls)
	}
	if got := strings.Count(logs.String(), "checksNoAgentCommit"); got != 1 || !strings.Contains(logs.String(), "run=run-1") {
		t.Fatalf("logs = %s, want checksNoAgentCommit once for run-1", logs)
	}
}

func TestSettleChecksMarksAURLNamingNoPullRequestUnresolvedAndSettlesTheRest(t *testing.T) {
	d, store, gh, logs := checksFixture(CheckGreen,
		UnsettledRun{ID: "bad", PRURL: "https://example.com/not-a-pull"},
		UnsettledRun{ID: "good", PRURL: checksPR})
	for range 2 {
		if err := settleChecks(context.Background(), d); err != nil {
			t.Fatal(err)
		}
	}
	want := []checksRecord{
		{"bad", "https://example.com/not-a-pull", "", CheckUnresolved},
		{"good", checksPR, "agent1", CheckGreen},
	}
	if !slices.Equal(store.recorded, want) {
		t.Fatalf("recorded %+v, want %+v", store.recorded, want)
	}
	if gh.commitCalls != 1 {
		t.Fatalf("listed commits %d times, want once for the run with a pull request", gh.commitCalls)
	}
	if got := strings.Count(logs.String(), "checksBadPRURL"); got != 1 || !strings.Contains(logs.String(), "run=bad") {
		t.Fatalf("logs = %s, want checksBadPRURL once for the bad run", logs)
	}
}

func TestSettleChecksDoesNotMarkARunItCannotYetSettleUnresolved(t *testing.T) {
	for name, set := range map[string]func(*fakeChecksGitHub){
		"its checks are pending": func(g *fakeChecksGitHub) { g.outcome = CheckPending },
		"its commit read fails":  func(g *fakeChecksGitHub) { g.commitsErr = errors.New("502") },
		"its outcome read fails": func(g *fakeChecksGitHub) { g.outcomeErr = errors.New("502") },
	} {
		t.Run(name, func(t *testing.T) {
			d, store, gh, _ := checksFixture(CheckGreen)
			set(gh)
			if err := settleChecks(context.Background(), d); err != nil {
				t.Fatal(err)
			}
			if len(store.recorded) != 0 {
				t.Fatalf("recorded %+v, want the run left for the next poll", store.recorded)
			}
		})
	}
}

func TestSettleChecksReportsUnresolvedMarksApartFromSettledResults(t *testing.T) {
	d, _, gh, logs := checksFixture(CheckFailing,
		UnsettledRun{ID: "squashed", PRURL: "https://github.com/octo/scratch/pull/8"},
		UnsettledRun{ID: "built", PRURL: "https://github.com/octo/scratch/pull/9"})
	gh.commitsByPR = map[int][]PullCommit{8: {{SHA: "owner1", Login: "alvintoh"}}}
	if err := settleChecks(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "checksUnresolvedMarked count=1") || !strings.Contains(logs.String(), "checksSettled count=1") {
		t.Fatalf("logs = %s, want one unresolved mark and one settled result counted apart", logs)
	}
}

func TestSettleChecksSaysNothingOfAnUnresolvedWriteTheRowDeclined(t *testing.T) {
	d, store, gh, logs := checksFixture(CheckGreen)
	gh.commits = []PullCommit{{SHA: "owner1", Login: "alvintoh"}}
	store.stale = true
	if err := settleChecks(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"checksNoAgentCommit", "checksUnresolvedMarked"} {
		if strings.Contains(logs.String(), token) {
			t.Fatalf("logs = %s, want no %s for a row that moved on", logs, token)
		}
	}
}

func TestSettleChecksReachesANewerRunBehindTwentyUnresolvableOnes(t *testing.T) {
	var runs []UnsettledRun
	byPR := map[int][]PullCommit{}
	for i := range 20 {
		runs = append(runs, UnsettledRun{ID: fmt.Sprintf("squashed-%d", i), PRURL: fmt.Sprintf("https://github.com/octo/scratch/pull/%d", 100+i)})
		byPR[100+i] = []PullCommit{{SHA: "owner", Login: "alvintoh"}}
	}
	runs = append(runs, UnsettledRun{ID: "newer", PRURL: "https://github.com/octo/scratch/pull/200"})
	d, store, gh, _ := checksFixture(CheckGreen, runs...)
	gh.commitsByPR = byPR
	if err := settleChecks(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if len(store.recorded) != 20 {
		t.Fatalf("the first poll recorded %d results, want the twenty unresolvable runs it was capped to", len(store.recorded))
	}
	if err := settleChecks(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	last := store.recorded[len(store.recorded)-1]
	if len(store.recorded) != 21 || last != (checksRecord{"newer", "https://github.com/octo/scratch/pull/200", "agent1", CheckGreen}) {
		t.Fatalf("recorded %d results ending in %+v, want the newer run settled on the second poll", len(store.recorded), last)
	}
}

func TestSettleChecksReadsEachRepositorysRequiredChecksOncePerPoll(t *testing.T) {
	d, _, gh, _ := checksFixture(CheckGreen,
		UnsettledRun{ID: "a", PRURL: "https://github.com/octo/scratch/pull/7"},
		UnsettledRun{ID: "b", PRURL: "https://github.com/octo/scratch/pull/8"},
		UnsettledRun{ID: "c", PRURL: "https://github.com/octo/other/pull/9"})
	if err := settleChecks(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if want := []string{"octo/scratch@main", "octo/other@main"}; !slices.Equal(gh.requiredCalls, want) {
		t.Fatalf("required checks read as %v, want %v", gh.requiredCalls, want)
	}
}

func TestSettleChecksAsksForTwentyRunsAndDoesNotReadASettledOneAgain(t *testing.T) {
	d, store, gh, _ := checksFixture(CheckGreen)
	for range 2 {
		if err := settleChecks(context.Background(), d); err != nil {
			t.Fatal(err)
		}
	}
	if store.limit != 20 {
		t.Fatalf("asked for %d runs, want 20", store.limit)
	}
	if len(gh.outcomeCalls) != 1 {
		t.Fatalf("read the outcome %d times across two polls, want once", len(gh.outcomeCalls))
	}
}

func TestSettleChecksLogsAWriteThatFailsAndStillSucceeds(t *testing.T) {
	d, store, _, logs := checksFixture(CheckGreen)
	store.recordErr = errFirestore
	if err := settleChecks(context.Background(), d); err != nil {
		t.Fatalf("a failed write for one run failed the phase: %v", err)
	}
	if !strings.Contains(logs.String(), "checksRecordFailed") {
		t.Fatalf("logs = %s, want checksRecordFailed", logs)
	}
	if strings.Contains(logs.String(), "checksSettled") {
		t.Fatalf("logs = %s, want no checksSettled for a result that was not written", logs)
	}
}

func TestSettleChecksCountsOnlyTheResultsItRecorded(t *testing.T) {
	d, store, _, logs := checksFixture(CheckGreen)
	store.stale = true
	if err := settleChecks(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "checksSettled") {
		t.Fatalf("logs = %s, want no checksSettled when the store recorded nothing", logs)
	}
	d, _, _, logs = checksFixture(CheckGreen)
	if err := settleChecks(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "checksSettled count=1") {
		t.Fatalf("logs = %s, want checksSettled count=1", logs)
	}
}

func TestSettleChecksDoesNotCallARunWithNoStartStale(t *testing.T) {
	d, _, _, logs := checksFixture(CheckPending, UnsettledRun{ID: "run-1", PRURL: checksPR})
	if err := settleChecks(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "checksPendingStale") {
		t.Fatalf("logs = %s, want no stale report for a run with no start", logs)
	}
}

func TestSettleChecksStopsOnAnEndedContextWithoutFailingThePoll(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, store, gh, logs := checksFixture(CheckGreen,
		UnsettledRun{ID: "a", PRURL: "https://github.com/octo/scratch/pull/7"},
		UnsettledRun{ID: "b", PRURL: "https://github.com/octo/scratch/pull/8"},
		UnsettledRun{ID: "c", PRURL: "https://github.com/octo/scratch/pull/9"})
	gh.onCommits = cancel
	if err := settleChecks(ctx, d); err != nil {
		t.Fatalf("an ended context failed the phase: %v", err)
	}
	if gh.commitCalls != 1 {
		t.Fatalf("read %d runs after the context ended, want the one in flight", gh.commitCalls)
	}
	if got := strings.Count(logs.String(), "checksStopped"); got != 1 {
		t.Fatalf("logs = %s, want checksStopped once, got %d", logs, got)
	}
	if len(store.recorded) != 1 {
		t.Fatalf("recorded %+v, want only the run read before the stop", store.recorded)
	}
}

func TestSettleChecksReportsAFailureToListTheRuns(t *testing.T) {
	d, store, _, _ := checksFixture(CheckGreen)
	store.listErr = errFirestore
	if err := settleChecks(context.Background(), d); !errors.Is(err, errFirestore) {
		t.Fatalf("err = %v, want the failed listing", err)
	}
}

func TestPollRunsTheChecksPhaseAndReportsItsFailure(t *testing.T) {
	d := pollDeps(fakeSource{}, &fakeQueue{}, &fakeWorkflow{})
	store := &fakeChecksRuns{listErr: errFirestore}
	d.Checks.Runs = store
	if _, err := Poll(context.Background(), d, buildConfig); !errors.Is(err, errFirestore) {
		t.Fatalf("err = %v, want the checks phase's failure reported by the poll", err)
	}
}

func TestPollGivesTheChecksPhaseABudgetOfItsOwn(t *testing.T) {
	d := pollDeps(fakeSource{}, &fakeQueue{}, &fakeWorkflow{})
	store := &fakeChecksRuns{}
	d.Checks.Runs = store
	before := time.Now()
	if _, err := Poll(context.Background(), d, buildConfig); err != nil {
		t.Fatal(err)
	}
	if store.deadline.IsZero() || store.deadline.After(before.Add(checksTimeout+time.Second)) {
		t.Fatalf("the phase's deadline is %v, want about %v after the poll began", store.deadline, checksTimeout)
	}
}

func TestMissingNamesTheChecksDepsLeftUnset(t *testing.T) {
	for name, checks := range map[string]ChecksDeps{
		"no store":  {GitHub: &fakeChecksGitHub{}, Branch: "main"},
		"no GitHub": {Runs: &fakeChecksRuns{}, Branch: "main"},
		"no branch": {Runs: &fakeChecksRuns{}, GitHub: &fakeChecksGitHub{}},
	} {
		t.Run(name, func(t *testing.T) {
			d := pollDeps(fakeSource{}, &fakeQueue{}, &fakeWorkflow{})
			d.Checks = checks
			if got := d.missing(); got != "Checks" {
				t.Fatalf("missing = %q, want Checks", got)
			}
		})
	}
}
