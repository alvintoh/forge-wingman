package dispatcher

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// fakeReverts is GitHub as a red main is traced and reverted: main's
// first-parent chain, the open pull requests, and the reverts it was asked to
// open.
type fakeReverts struct {
	chain     []string
	chainErr  error
	open      []ClosedPR
	openErr   error
	opened    int
	revertErr error
	// chains, openCalls and reverts record the calls made, so a test can prove
	// one did not happen.
	chains    []string
	openCalls int
	reverts   []revertCall
}

type revertCall struct {
	pr          ClosedPR
	title, body string
}

func (f *fakeReverts) FirstParents(_ context.Context, repo, branch string) ([]string, error) {
	f.chains = append(f.chains, repo+"@"+branch)
	return f.chain, f.chainErr
}

func (f *fakeReverts) OpenPulls(context.Context, string) ([]ClosedPR, error) {
	f.openCalls++
	return f.open, f.openErr
}

func (f *fakeReverts) RevertPull(_ context.Context, pr ClosedPR, title, body string) (int, error) {
	f.reverts = append(f.reverts, revertCall{pr, title, body})
	if f.revertErr != nil {
		return 0, f.revertErr
	}
	return f.opened, nil
}

const (
	// redMergeCommit is the commit the fixture PR's auto-merge made, main's head.
	redMergeCommit = "merge7"
	// greenBase is the commit main stood at, passing, before the merge.
	greenBase = "base"
	// redRevert is the number GitHub gives the revert it opens.
	redRevert = 12
)

// autoMergedPR is a run's PR the product merged itself as redMergeCommit.
func autoMergedPR() ClosedPR {
	return ClosedPR{
		Number: 7, Title: "feat(x): add a file", Head: runBranch, MergedAt: mergedAt,
		MergedBy: "wingman[bot]", NodeID: "PR_kwDO7", MergeCommit: redMergeCommit,
	}
}

type redMainFixture struct {
	deps    Deps
	prs     *fakePullRequests
	rows    *fakeRewrites
	escapes *fakeEscapes
	reverts *fakeReverts
	checks  *fakeChecksGitHub
	logs    *bytes.Buffer
}

// redMain is a poll that finds the listed PRs merged and main's head, the
// first of them, failing its required checks over a passing parent, with no
// revert open yet.
func redMain(listed ...ClosedPR) redMainFixture {
	if len(listed) == 0 {
		listed = []ClosedPR{autoMergedPR()}
	}
	prs := &fakePullRequests{closed: map[string][]ClosedPR{scratch: listed}}
	rows := &fakeRewrites{rows: map[string]RunRow{}}
	for _, pr := range listed {
		rows.rows[pr.Head] = RunRow{RunID: runOf(pr)}
	}
	deps, _ := rewriteDeps(prs, rows)
	f := redMainFixture{
		deps: deps, prs: prs, rows: rows,
		escapes: &fakeEscapes{},
		reverts: &fakeReverts{chain: []string{listed[0].MergeCommit, greenBase}, opened: redRevert},
		checks: &fakeChecksGitHub{required: []string{"go", "web"}, outcome: CheckGreen,
			outcomeBySHA: map[string]string{listed[0].MergeCommit: CheckFailing}},
		logs: &bytes.Buffer{},
	}
	f.deps.Escapes, f.deps.Reverts = f.escapes, f.reverts
	f.deps.Checks = ChecksDeps{Runs: &fakeChecksRuns{}, GitHub: f.checks, Branch: "main"}
	f.deps.Logger = slog.New(slog.NewTextHandler(f.logs, nil))
	return f
}

// runOf is the id of the run row the fixture holds for pr.
func runOf(pr ClosedPR) string { return "run-" + pr.Head }

func (f redMainFixture) poll(t *testing.T) {
	t.Helper()
	if err := measureRewrites(context.Background(), f.deps, oneRepo); err != nil {
		t.Fatal(err)
	}
}

// opensNothing asserts the poll opened no revert, latched nothing and recorded
// no revert on a row.
func (f redMainFixture) opensNothing(t *testing.T) {
	t.Helper()
	if len(f.reverts.reverts) != 0 || len(f.escapes.escapes) != 0 || len(f.rows.reverted) != 0 {
		t.Fatalf("reverts %v, escapes %v, rows %v, want nothing opened, latched or recorded",
			f.reverts.reverts, f.escapes.escapes, f.rows.reverted)
	}
}

// reverted asserts the poll opened a revert of pr alone, latched it with that
// revert and recorded the revert on its row.
func (f redMainFixture) reverted(t *testing.T, pr ClosedPR) {
	t.Helper()
	if len(f.reverts.reverts) != 1 || f.reverts.reverts[0].pr.Number != pr.Number {
		t.Fatalf("reverts = %+v, want PR %d reverted once", f.reverts.reverts, pr.Number)
	}
	want := MergeEscape{Repo: scratch, PR: pr.Number, By: redRevert, Reason: EscapeRedMain, At: mergedAt}
	if len(f.escapes.escapes) != 1 || f.escapes.escapes[0] != want {
		t.Fatalf("escapes = %+v, want %+v", f.escapes.escapes, want)
	}
	if len(f.rows.reverted) != 1 || f.rows.reverted[runOf(pr)] != redRevert {
		t.Fatalf("reverted = %v, want revert %d recorded on %s", f.rows.reverted, redRevert, runOf(pr))
	}
}

func TestRedMainOpensARevertOfTheAutoMergeAndLatches(t *testing.T) {
	f := redMain()
	f.poll(t)
	if len(f.reverts.chains) != 1 || f.reverts.chains[0] != "octo/scratch@main" {
		t.Fatalf("chains read = %v, want main's first parents", f.reverts.chains)
	}
	f.reverted(t, autoMergedPR())
	got := f.reverts.reverts[0]
	if got.pr.NodeID != "PR_kwDO7" || got.title != `Revert "feat(x): add a file"` ||
		!strings.HasPrefix(got.body, "Reverts octo/scratch#7\n") {
		t.Fatalf("revert = %+v, want PR 7 reverted under GitHub's own title and body line", got)
	}
	// The next poll finds the row naming its revert and its escape latched, and
	// neither opens a second revert nor asks for the latch again.
	calls := f.escapes.calls
	f.poll(t)
	if len(f.reverts.reverts) != 1 || f.reverts.openCalls != 1 || f.escapes.calls != calls {
		t.Fatalf("reverts = %d after %d open listings and %d latch calls, want one revert and no second latch",
			len(f.reverts.reverts), f.reverts.openCalls, f.escapes.calls)
	}
}

// main's head is not always the commit that broke it: with a non-strict
// ruleset a run PR can merge on top of a red one.
func TestRedMainRevertsTheFirstRedCommitNotTheHead(t *testing.T) {
	first := autoMergedPR()
	second := autoMergedPR()
	second.Number, second.Title, second.Head, second.NodeID, second.MergeCommit =
		8, "feat(y): add another", runner.BranchPrefix+"FRG-2-124-1", "PR_kwDO8", "merge8"
	f := redMain(second, first)
	f.reverts.chain = []string{"merge8", redMergeCommit, greenBase}
	f.checks.outcomeBySHA = map[string]string{"merge8": CheckFailing, redMergeCommit: CheckFailing}
	f.poll(t)
	f.reverted(t, first)
}

func TestRedMainRevertsAnAutoMergeUnderTheOwnersCommits(t *testing.T) {
	f := redMain()
	f.reverts.chain = []string{"owners", redMergeCommit, greenBase}
	f.checks.outcomeBySHA = map[string]string{"owners": CheckFailing, redMergeCommit: CheckFailing}
	f.poll(t)
	f.reverted(t, autoMergedPR())
}

// The owner's commit that turned main red is the owner's to mend, whatever
// auto-merged on top of it.
func TestRedMainOpensNothingWhenTheOwnersCommitTurnedItRed(t *testing.T) {
	f := redMain()
	f.reverts.chain = []string{redMergeCommit, "owners", greenBase}
	f.checks.outcomeBySHA = map[string]string{redMergeCommit: CheckFailing, "owners": CheckFailing}
	f.poll(t)
	f.opensNothing(t)
	if !strings.Contains(f.logs.String(), "msg=mainRedNotAutoMerged repo=octo/scratch sha=owners") {
		t.Fatalf("log = %q, want the owner's first red commit named", f.logs.String())
	}
}

func TestRedMainWaitsOnAWalkItCannotFinish(t *testing.T) {
	for name, tt := range map[string]struct {
		parent  string
		errAt   string
		chain   []string
		logWant string
	}{
		"parent pending":    {parent: CheckPending, logWant: "reason=parent-pending"},
		"parent unreadable": {parent: CheckGreen, errAt: greenBase, logWant: "reason=checks"},
		"walk exhausted":    {parent: CheckFailing, logWant: "reason=walk-exhausted"},
		"head alone":        {parent: CheckGreen, chain: []string{redMergeCommit}, logWant: "reason=walk-exhausted"},
	} {
		t.Run(name, func(t *testing.T) {
			f := redMain()
			if tt.chain != nil {
				f.reverts.chain = tt.chain
			}
			f.checks.outcomeBySHA[greenBase] = tt.parent
			if tt.errAt != "" {
				f.checks.outcomeErr, f.checks.outcomeErrAt = errFirestore, tt.errAt
			}
			f.poll(t)
			f.opensNothing(t)
			if !strings.Contains(f.logs.String(), tt.logWant) {
				t.Fatalf("log = %q, want %s", f.logs.String(), tt.logWant)
			}
		})
	}
}

func TestRedMainOpensNothingWhileTheHeadIsNotFailing(t *testing.T) {
	for _, outcome := range []string{CheckPending, CheckGreen} {
		t.Run(outcome, func(t *testing.T) {
			f := redMain()
			f.checks.outcomeBySHA[redMergeCommit] = outcome
			f.poll(t)
			f.opensNothing(t)
		})
	}
}

func TestRedMainOpensNothingForAFirstRedCommitThatIsNotAnAutoMerge(t *testing.T) {
	ownersMerge := autoMergedPR()
	ownersMerge.MergedBy = "alvintoh"
	for name, tt := range map[string]struct {
		pr   ClosedPR
		rows map[string]RunRow
	}{
		"the owner merged it": {ownersMerge, nil},
		"no run row names it": {autoMergedPR(), map[string]RunRow{}},
	} {
		t.Run(name, func(t *testing.T) {
			f := redMain(tt.pr)
			if tt.rows != nil {
				f.rows.rows = tt.rows
			}
			f.poll(t)
			f.opensNothing(t)
		})
	}
}

// A main whose recent commits hold no run merge reads no checks, and a listing
// with no run merge does not read main at all.
func TestRedMainReadsNothingMoreWithoutARunMerge(t *testing.T) {
	f := redMain()
	f.reverts.chain = []string{"owners", greenBase}
	f.poll(t)
	if len(f.checks.outcomeCalls) != 0 {
		t.Fatalf("outcome calls = %+v, want none while main holds no run merge", f.checks.outcomeCalls)
	}
	byHand := autoMergedPR()
	byHand.Head = "fix/by-hand"
	unmerged := autoMergedPR()
	unmerged.MergeCommit = ""
	for _, pr := range []ClosedPR{byHand, unmerged} {
		f = redMain(pr)
		f.poll(t)
		if len(f.reverts.chains) != 0 {
			t.Fatalf("chains read = %v for %+v, want none while no run PR carries a merge commit", f.reverts.chains, pr)
		}
	}
}

// ownRevert is the head GitHub's revertPullRequest gives a revert of PR 7: a
// revert-7- branch of the repository itself.
func ownRevert(pr ClosedPR) ClosedPR {
	pr.Head, pr.HeadRepo = "revert-7-"+runBranch, scratch
	return pr
}

func TestRedMainOpensNothingWhenARevertIsAlreadyOpen(t *testing.T) {
	for name, open := range map[string]ClosedPR{
		"named in its body":  ownRevert(ClosedPR{Number: 15, Title: "take it back", Body: "Reverts octo/scratch#7"}),
		"titled as a revert": ownRevert(ClosedPR{Number: 15, Title: `Revert "feat(x): add a file"`}),
	} {
		t.Run(name, func(t *testing.T) {
			f := redMain()
			f.reverts.open = []ClosedPR{ownRevert(ClosedPR{Number: 3, Title: "unrelated", Body: "Reverts octo/scratch#2"}), open}
			f.poll(t)
			if len(f.reverts.reverts) != 0 {
				t.Fatalf("reverts = %+v, want none opened beside the open one", f.reverts.reverts)
			}
			if f.rows.reverted[runOf(autoMergedPR())] != 15 || len(f.escapes.escapes) != 1 || f.escapes.escapes[0].By != 15 {
				t.Fatalf("reverted %v, escapes %+v, want the open revert recorded and latched", f.rows.reverted, f.escapes.escapes)
			}
		})
	}
}

// A PR worded as the revert is not taken as it unless GitHub's revert made it:
// one from a fork, or from another branch, is ignored and a revert is opened.
func TestRedMainIgnoresAPRThatOnlyLooksLikeTheRevert(t *testing.T) {
	looks := ClosedPR{Number: 15, Title: `Revert "feat(x): add a file"`, Body: "Reverts octo/scratch#7"}
	fork := ownRevert(looks)
	fork.HeadRepo = "mallory/scratch"
	gone := ownRevert(looks)
	gone.HeadRepo = ""
	otherBranch := ownRevert(looks)
	otherBranch.Head = "take-it-back"
	otherRevert := ownRevert(looks)
	otherRevert.Head = "revert-71-" + runBranch
	for name, open := range map[string]ClosedPR{
		"from a fork":               fork,
		"from a deleted repository": gone,
		"from another branch":       otherBranch,
		"from another PR's revert":  otherRevert,
	} {
		t.Run(name, func(t *testing.T) {
			f := redMain()
			f.reverts.open = []ClosedPR{open}
			f.poll(t)
			f.reverted(t, autoMergedPR())
		})
	}
}

// A refusal latches auto-merge off and fails no poll. It is raised at error the
// first time and at warn while GitHub repeats it, and a revert that opens on a
// later poll is named in the latch already held.
func TestRedMainRaisesARefusalOnceAndNamesALaterRevert(t *testing.T) {
	f := redMain()
	f.reverts.revertErr = errors.New("GitHub refused to revert pull request #7: Resource not accessible by personal access token")
	f.poll(t)
	if !strings.Contains(f.logs.String(), "level=ERROR msg=revertNotOpened reason=refused") {
		t.Fatalf("log = %q, want the first refusal raised at error", f.logs.String())
	}
	if len(f.escapes.escapes) != 1 || f.escapes.escapes[0].By != 0 || len(f.rows.reverted) != 0 {
		t.Fatalf("escapes %+v, reverted %v, want the latch set and no revert recorded", f.escapes.escapes, f.rows.reverted)
	}
	f.logs.Reset()
	f.poll(t)
	if strings.Contains(f.logs.String(), "level=ERROR") || !strings.Contains(f.logs.String(), "level=WARN msg=revertNotOpened reason=refused") {
		t.Fatalf("log = %q, want the repeated refusal at warn", f.logs.String())
	}
	f.logs.Reset()
	f.reverts.revertErr = errors.New("GitHub refused to revert pull request #7: something else")
	f.poll(t)
	if !strings.Contains(f.logs.String(), "level=ERROR msg=revertNotOpened") {
		t.Fatalf("log = %q, want a new refusal raised at error", f.logs.String())
	}
	f.reverts.revertErr = nil
	f.poll(t)
	if f.escapes.escapes[0].By != redRevert || f.rows.reverted[runOf(autoMergedPR())] != redRevert {
		t.Fatalf("escapes %+v, reverted %v, want the later revert named in the latch and on the row", f.escapes.escapes, f.rows.reverted)
	}
}

// Open pull requests that cannot be listed open nothing, since a revert may be
// among them.
func TestRedMainOpensNothingWhenTheOpenPullsCannotBeListed(t *testing.T) {
	f := redMain()
	f.reverts.openErr = errFirestore
	f.poll(t)
	if len(f.reverts.reverts) != 0 || !strings.Contains(f.logs.String(), "reason=open-pulls-unread") {
		t.Fatalf("reverts %+v, log %q, want none opened and the gap named", f.reverts.reverts, f.logs.String())
	}
}

// An escape an earlier latch absorbed latches once the owner clears that one,
// with the revert already opened and no second one.
func TestRedMainLatchesAnAbsorbedEscapeOnceTheOtherIsCleared(t *testing.T) {
	f := redMain()
	other := MergeEscape{Repo: scratch, PR: 3, Reason: EscapeReverted}
	f.escapes.escapes = []MergeEscape{other}
	f.poll(t)
	if f.rows.reverted[runOf(autoMergedPR())] != redRevert || len(f.escapes.escapes) != 1 || f.escapes.escapes[0] != other {
		t.Fatalf("reverted %v, escapes %+v, want the revert recorded and the other latch kept", f.rows.reverted, f.escapes.escapes)
	}
	f.escapes.escapes, f.escapes.cleared = nil, []string{EscapeKey(scratch, 3)}
	f.poll(t)
	want := MergeEscape{Repo: scratch, PR: 7, By: redRevert, Reason: EscapeRedMain, At: mergedAt}
	if len(f.escapes.escapes) != 1 || f.escapes.escapes[0] != want || len(f.reverts.reverts) != 1 || f.rows.revertCalls != 1 {
		t.Fatalf("escapes %+v after %d reverts and %d records, want %+v from the one revert, recorded once",
			f.escapes.escapes, len(f.reverts.reverts), f.rows.revertCalls, want)
	}
}

// The owner merging the revert after clearing its latch latches nothing again.
func TestRedMainMergedRevertDoesNotLatchAgain(t *testing.T) {
	f := redMain()
	f.poll(t)
	f.escapes.escapes, f.escapes.cleared = nil, []string{EscapeKey(scratch, 7)}
	revert := ClosedPR{Number: redRevert, Title: `Revert "feat(x): add a file"`, Body: "Reverts octo/scratch#7\n",
		Head: "revert-7-" + runBranch, MergedAt: mergedAt.Add(2 * time.Hour), MergeCommit: "merge12"}
	f.prs.closed[scratch] = []ClosedPR{revert, autoMergedPR()}
	f.reverts.chain = []string{"merge12", redMergeCommit, greenBase}
	f.checks.outcomeBySHA["merge12"] = CheckGreen
	f.poll(t)
	if len(f.escapes.escapes) != 0 || len(f.reverts.reverts) != 1 {
		t.Fatalf("escapes %+v, reverts %d, want nothing latched or opened again", f.escapes.escapes, len(f.reverts.reverts))
	}
}

// A store write that fails is returned, and a latch that cannot be written
// leaves the row naming no revert, so the next poll latches again.
func TestRedMainReturnsAStoreWriteThatFails(t *testing.T) {
	for name, fail := range map[string]func(*redMainFixture){
		"latch":  func(f *redMainFixture) { f.escapes.err = errFirestore },
		"revert": func(f *redMainFixture) { f.rows.revertErr = errFirestore },
		"refusal": func(f *redMainFixture) {
			f.reverts.revertErr, f.rows.refusalErr = errors.New("refused"), errFirestore
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := redMain()
			fail(&f)
			if err := measureRewrites(context.Background(), f.deps, oneRepo); !errors.Is(err, errFirestore) {
				t.Fatalf("err = %v, want the store's failure", err)
			}
			if len(f.rows.reverted) != 0 {
				t.Fatalf("reverted = %v, want nothing recorded", f.rows.reverted)
			}
			// Only a latch that itself failed is missing: a refusal that could not
			// be kept still latches.
			if latched := len(f.escapes.escapes) == 1; latched != (name != "latch") {
				t.Fatalf("escapes = %+v, want the latch set unless its own write failed", f.escapes.escapes)
			}
		})
	}
}

// A GitHub read that fails opens nothing, fails no poll and names what it could
// not read, so the next poll tries again. Each fake answers a value beside its
// error, so only the check of the error keeps the revert from opening.
func TestRedMainOpensNothingWhenAReadFails(t *testing.T) {
	for reason, fail := range map[string]func(*redMainFixture){
		"commits":         func(f *redMainFixture) { f.reverts.chainErr = errFirestore },
		"required-checks": func(f *redMainFixture) { f.checks.requiredErr = errFirestore },
		"checks":          func(f *redMainFixture) { f.checks.outcomeErr = errFirestore },
	} {
		t.Run(reason, func(t *testing.T) {
			f := redMain()
			fail(&f)
			f.poll(t)
			f.opensNothing(t)
			if !strings.Contains(f.logs.String(), "msg=redMainUnread reason="+reason) {
				t.Fatalf("log = %q, want the failed read named", f.logs.String())
			}
		})
	}
}

func TestRefusalTextIsCutOnACharacterBoundary(t *testing.T) {
	long := strings.Repeat("é", maxErrorBytes)
	got := refusalText(errors.New(long))
	if len(got) > maxErrorBytes || len(got) < maxErrorBytes-1 || !strings.HasPrefix(long, got) {
		t.Fatalf("refusalText = %d bytes, want a whole-character prefix within %d", len(got), maxErrorBytes)
	}
}

func TestMergeEscapeWordsNamesTheRevertOfARedMain(t *testing.T) {
	got := MergeEscape{Repo: scratch, PR: 7, By: 12, Reason: EscapeRedMain}.Words()
	want := "auto-merge is off until you switch it on again: octo/scratch#7 left main failing its required checks; revert #12 waits for you"
	if got != want {
		t.Fatalf("Words = %q, want %q", got, want)
	}
}

func TestMissingNamesTheRevertsLeftUnset(t *testing.T) {
	d := pollDeps(fakeSource{}, &fakeQueue{}, &fakeWorkflow{})
	d.Reverts = nil
	if got := d.missing(); got != "Reverts" {
		t.Fatalf("missing = %q, want Reverts", got)
	}
}
