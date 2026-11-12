package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	findingRetry   = "the retry never bounds its attempts"
	findingTimeout = "the timeout is hard-coded"
	findingSwallow = "the error is swallowed"
	findingLogLine = "the log line interpolates the id"
)

// fixBranch is the pull request branch every fix-round test commits to.
const fixBranch = "wingman/fix-42-1"

// fixEvents is one round's event stream: a step finish that names the session,
// then the agent's final message.
func fixEvents(text string) string {
	return `{"type":"step_finish","sessionID":"ses_1","part":{"tokens":{"input":3,"output":1}}}` + "\n" +
		`{"type":"text","part":{"type":"text","text":` + strconv.Quote(text) + `}}` + "\n"
}

// fixAgent is the build agent a fix-round test runs: it writes one new file per
// round, so every round has something to commit.
func fixAgent() *fakeAgent {
	a := &fakeAgent{events: fixEvents("")}
	a.edit = func(dir string) error {
		return os.WriteFile(filepath.Join(dir, fmt.Sprintf("fix%d.go", a.calls)), []byte("package x\n"), 0o600)
	}
	return a
}

// fixWorktree checks the pull request's branch out as a worktree of repo.
func fixWorktree(t *testing.T, repo string) Worktree {
	t.Helper()
	wt, err := AddWorktree(context.Background(), repo, filepath.Join(t.TempDir(), "wt"), fixBranch)
	if err != nil {
		t.Fatal(err)
	}
	return wt
}

// fixSeams records the commits the loop published and re-reviewed, and answers
// the re-review from blocks, one per round.
type fixSeams struct {
	t        *testing.T
	blocks   []string
	pushed   []string
	reviewed []string
}

func (s *fixSeams) push(_ context.Context, branch, commit string) error {
	if branch != fixBranch {
		return fmt.Errorf("pushed to branch %q", branch)
	}
	s.pushed = append(s.pushed, commit)
	return nil
}

func (s *fixSeams) review(_ context.Context, branch, commit string) (string, error) {
	if branch != fixBranch {
		return "", fmt.Errorf("re-reviewed branch %q", branch)
	}
	s.reviewed = append(s.reviewed, commit)
	if len(s.reviewed) > len(s.blocks) {
		s.t.Fatalf("the loop re-reviewed %d heads, want %d", len(s.reviewed), len(s.blocks))
	}
	return s.blocks[len(s.reviewed)-1], nil
}

// fixLoop is the loop a test runs over seams, findings and the round cap.
func fixLoop(t *testing.T, wt Worktree, seams *fixSeams, findings string, rounds int) FixRounds {
	t.Helper()
	head, err := wt.HeadCommit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return FixRounds{
		Branch:   fixBranch,
		Head:     head,
		Session:  "ses_0",
		Rules:    "# Rules\n",
		Findings: findings,
		Rounds:   rounds,
		Push:     seams.push,
		Review:   seams.review,
	}
}

// TestFixRoundsCommitsOneRoundPerReviewOnThePullRequestBranch is AC1: at most the
// cap's rounds, each one commit on the pull request's own branch authored as the
// runner, each fed the findings still open.
func TestFixRoundsCommitsOneRoundPerReviewOnThePullRequestBranch(t *testing.T) {
	repo := initRepo(t)
	wt := fixWorktree(t, repo)
	agent := fixAgent()
	deps, completions, _ := testDeps(validObjects(), agent)
	clock := time.Now()
	deps.Now = func() time.Time { clock = clock.Add(10 * time.Millisecond); return clock }
	seams := &fixSeams{t: t, blocks: []string{findingTimeout, ""}}

	res, err := RunFixRounds(context.Background(), deps, testConfig(t, repo), wt,
		fixLoop(t, wt, seams, findingRetry+"\n"+findingTimeout, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ready || res.Stop != "" || res.Detail != "" {
		t.Fatalf("result = %+v, want a clean pull request", res)
	}
	if res.Session != "ses_1" {
		t.Fatalf("session = %q, want the last round's", res.Session)
	}
	if len(res.Rounds) != 2 {
		t.Fatalf("%d rounds, want 2", len(res.Rounds))
	}
	if !slices.Equal(res.Rounds[0].Findings, []string{findingRetry, findingTimeout}) || res.Rounds[0].Resolved != 1 || res.Rounds[0].New != 0 {
		t.Fatalf("round 1 = %+v, want the two findings it was given, one resolved", res.Rounds[0])
	}
	if !slices.Equal(res.Rounds[1].Findings, []string{findingTimeout}) || res.Rounds[1].Resolved != 1 || res.Rounds[1].New != 0 {
		t.Fatalf("round 2 = %+v, want only the finding still open, now resolved", res.Rounds[1])
	}
	if len(res.Steps) != 2 || res.Steps[0] != res.Rounds[0].Step || res.Steps[1] != res.Rounds[1].Step {
		t.Fatalf("steps = %+v, want each round's own step for the caller's summary", res.Steps)
	}
	for i, round := range res.Rounds {
		if !round.Reviewed || round.Step.Round != i+1 || round.Commit == "" || round.Step.Tokens.Input != 3 || round.Step.DurationMS <= 0 {
			t.Fatalf("round %d = %+v, want a numbered round with its commit, tokens and duration", i+1, round)
		}
	}
	for _, round := range res.Rounds {
		// The fix phase is what keeps a round's transcript off the build's own
		// object of the same attempt and number, which the bucket refuses to
		// overwrite.
		object := fmt.Sprintf("completions/42-1-fix-%d.jsonl", round.Step.Round)
		if _, ok := completions[object]; !ok {
			t.Fatalf("completions hold no %s, want each round's own transcript", object)
		}
	}
	if !slices.Equal(seams.pushed, []string{res.Rounds[0].Commit, res.Rounds[1].Commit}) {
		t.Fatalf("pushed = %q, want each round's own commit in order", seams.pushed)
	}
	if !slices.Equal(seams.reviewed, seams.pushed) {
		t.Fatalf("re-reviewed = %q, want the heads that were published", seams.reviewed)
	}
	// The base commit plus one per round, the last authored as the runner.
	if got := strings.TrimSpace(mustGit(t, wt.Dir, "rev-list", "--count", "HEAD")); got != "3" {
		t.Fatalf("branch holds %s commits, want the base and one per round", got)
	}
	if got := strings.TrimSpace(mustGit(t, wt.Dir, "log", "-1", "--format=%an <%ae>")); got != CommitAuthorName+" <"+CommitAuthorEmail+">" {
		t.Fatalf("last commit's author = %q, want the runner", got)
	}
	if agent.calls != 2 {
		t.Fatalf("the agent ran %d rounds, want 2", agent.calls)
	}
	if !slices.Equal(agent.sessions, []string{"ses_0", "ses_1"}) {
		t.Fatalf("sessions = %q, want the caller's, then the loop's own on every round after", agent.sessions)
	}
	for i, rules := range agent.rules {
		if rules != "# Rules\n" {
			t.Fatalf("round %d rules = %q, want the caller's on every round", i+1, rules)
		}
	}
	if !strings.Contains(agent.prompts[0], findingRetry) || !strings.Contains(agent.prompts[0], findingTimeout) {
		t.Fatalf("round 1 prompt = %q, want both findings", agent.prompts[0])
	}
	if strings.Contains(agent.prompts[1], findingRetry) || !strings.Contains(agent.prompts[1], findingTimeout) {
		t.Fatalf("round 2 prompt = %q, want only the finding still open", agent.prompts[1])
	}
	if !strings.Contains(agent.prompts[0], "declined-findings") || !strings.Contains(agent.prompts[0], commitMessageInstruction) {
		t.Fatalf("round 1 prompt = %q, want the decline and commit-message blocks asked for", agent.prompts[0])
	}
}

// TestFixRoundsHandsOverWhenTheReviewReportsTheSameFindings is AC4's unchanged
// half: a round the re-review gained nothing on ends the loop.
func TestFixRoundsHandsOverWhenTheReviewReportsTheSameFindings(t *testing.T) {
	repo := initRepo(t)
	agent := fixAgent()
	deps, _, _ := testDeps(validObjects(), agent)
	seams := &fixSeams{t: t, blocks: []string{findingRetry}}

	wt := fixWorktree(t, repo)
	res, err := RunFixRounds(context.Background(), deps, testConfig(t, repo), wt,
		fixLoop(t, wt, seams, findingRetry, 0))
	if err != nil {
		t.Fatal(err)
	}
	if res.Ready || res.Stop != FixStopUnchanged {
		t.Fatalf("result = %+v, want the pull request handed over with the findings unchanged", res)
	}
	if len(res.Rounds) != 1 || res.Rounds[0].Resolved != 0 || res.Rounds[0].New != 0 {
		t.Fatalf("rounds = %+v, want one round that resolved and added nothing", res.Rounds)
	}
	if agent.calls != 1 {
		t.Fatalf("the agent ran %d rounds, want 1", agent.calls)
	}
	if !strings.Contains(res.Detail, findingRetry) || !strings.Contains(res.Detail, "left the findings unchanged") {
		t.Fatalf("detail = %q, want the finding and why the loop stopped", res.Detail)
	}
}

// TestFixRoundsHandsOverWhenARoundAddsAFinding is AC4's added half: a round that
// makes a new finding ends the loop, whatever it resolved.
func TestFixRoundsHandsOverWhenARoundAddsAFinding(t *testing.T) {
	repo := initRepo(t)
	agent := fixAgent()
	deps, _, _ := testDeps(validObjects(), agent)
	seams := &fixSeams{t: t, blocks: []string{findingTimeout + "\n" + findingSwallow}}

	wt := fixWorktree(t, repo)
	res, err := RunFixRounds(context.Background(), deps, testConfig(t, repo), wt,
		fixLoop(t, wt, seams, findingRetry+"\n"+findingTimeout, 0))
	if err != nil {
		t.Fatal(err)
	}
	if res.Ready || res.Stop != FixStopNew {
		t.Fatalf("result = %+v, want the pull request handed over with a new finding", res)
	}
	if len(res.Rounds) != 1 || res.Rounds[0].Resolved != 1 || res.Rounds[0].New != 1 {
		t.Fatalf("rounds = %+v, want one round that resolved one finding and added one", res.Rounds)
	}
	if !strings.Contains(res.Detail, findingSwallow) {
		t.Fatalf("detail = %q, want the finding the round added", res.Detail)
	}
}

// TestFixRoundsHandsOverWhenARoundChangesNoFile is AC4's empty half: a round that
// edits nothing resolves nothing, so the loop stops without publishing or
// re-reviewing it.
func TestFixRoundsHandsOverWhenARoundChangesNoFile(t *testing.T) {
	repo := initRepo(t)
	agent := &fakeAgent{events: fixEvents("")}
	deps, _, _ := testDeps(validObjects(), agent)
	seams := &fixSeams{t: t}

	wt := fixWorktree(t, repo)
	res, err := RunFixRounds(context.Background(), deps, testConfig(t, repo), wt,
		fixLoop(t, wt, seams, findingRetry, 0))
	if err != nil {
		t.Fatal(err)
	}
	if res.Ready || res.Stop != FixStopNoChange {
		t.Fatalf("result = %+v, want the pull request handed over with no change", res)
	}
	if len(res.Rounds) != 1 || res.Rounds[0].Commit != "" || res.Rounds[0].Resolved != 0 {
		t.Fatalf("rounds = %+v, want one round with no commit", res.Rounds)
	}
	if len(seams.pushed) != 0 || len(seams.reviewed) != 0 {
		t.Fatalf("pushed %q and re-reviewed %q, want neither for a round that changed no file", seams.pushed, seams.reviewed)
	}
	if !strings.Contains(res.Detail, findingRetry) || !strings.Contains(res.Detail, "changed no file") {
		t.Fatalf("detail = %q, want the finding and why the loop stopped", res.Detail)
	}
}

// TestFixRoundsRecordsADeclinedFindingWithItsReason is AC3: the finding the round
// declined is recorded with its reason and left for the owner, and the loop does
// not spend another round on it.
func TestFixRoundsRecordsADeclinedFindingWithItsReason(t *testing.T) {
	const reason = "the caller owns the timeout"
	repo := initRepo(t)
	agent := fixAgent()
	agent.events = fixEvents("```declined-findings\n" + findingTimeout + " — " + reason + "\n```")
	deps, _, _ := testDeps(validObjects(), agent)
	seams := &fixSeams{t: t, blocks: []string{findingTimeout}}

	wt := fixWorktree(t, repo)
	res, err := RunFixRounds(context.Background(), deps, testConfig(t, repo), wt,
		fixLoop(t, wt, seams, findingRetry+"\n"+findingTimeout, 0))
	if err != nil {
		t.Fatal(err)
	}
	if res.Ready || res.Stop != FixStopDeclined {
		t.Fatalf("result = %+v, want the pull request handed over on a decline", res)
	}
	want := []DeclinedFinding{{Finding: findingTimeout, Reason: reason}}
	if len(res.Rounds) != 1 || !slices.Equal(res.Rounds[0].Declined, want) {
		t.Fatalf("rounds = %+v, want the decline with its reason", res.Rounds)
	}
	if agent.calls != 1 {
		t.Fatalf("the agent ran %d rounds, want 1: a declined finding is the owner's", agent.calls)
	}
	if !strings.Contains(res.Detail, findingTimeout) || !strings.Contains(res.Detail, reason) {
		t.Fatalf("detail = %q, want the declined finding and its reason", res.Detail)
	}
}

// TestFixRoundsDoesNotGiveALaterRoundAFindingAnEarlierOneDeclined is AC3 across
// rounds: round 1 declines one finding and fixes another, the re-review reports
// the declined one again, and round 2 is given only what it can act on, with the
// declined one counted as neither resolved nor new.
func TestFixRoundsDoesNotGiveALaterRoundAFindingAnEarlierOneDeclined(t *testing.T) {
	const reason = "the caller owns the retry"
	repo := initRepo(t)
	agent := fixAgent()
	agent.eventsFn = func(call int) string {
		if call == 1 {
			return fixEvents("```declined-findings\n" + findingRetry + " — " + reason + "\n```")
		}
		return fixEvents("")
	}
	deps, _, _ := testDeps(validObjects(), agent)
	seams := &fixSeams{t: t, blocks: []string{findingRetry + "\n" + findingSwallow, findingRetry}}

	wt := fixWorktree(t, repo)
	res, err := RunFixRounds(context.Background(), deps, testConfig(t, repo), wt,
		fixLoop(t, wt, seams, findingRetry+"\n"+findingTimeout+"\n"+findingSwallow, 0))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(agent.prompts[1], findingRetry) || !strings.Contains(agent.prompts[1], findingSwallow) {
		t.Fatalf("round 2 prompt = %q, want only the finding the round can act on", agent.prompts[1])
	}
	if res.Ready || res.Stop != FixStopDeclined || len(res.Rounds) != 2 {
		t.Fatalf("result = %+v, want two rounds and the pull request handed over on the decline", res)
	}
	if !slices.Equal(res.Rounds[1].Findings, []string{findingSwallow}) || res.Rounds[1].Resolved != 1 || res.Rounds[1].New != 0 {
		t.Fatalf("round 2 = %+v, want the one finding it was given, resolved, and nothing new", res.Rounds[1])
	}
	if !strings.Contains(res.Detail, findingRetry) || !strings.Contains(res.Detail, reason) {
		t.Fatalf("detail = %q, want the declined finding and its reason", res.Detail)
	}
}

// TestFixRoundsReturnsTheRoundsAlreadyMadeWhenALaterStepStops: round 1's commit is
// on the branch, so an error in round 2 still returns its record, its decline,
// the steps both rounds ran and the session.
func TestFixRoundsReturnsTheRoundsAlreadyMadeWhenALaterStepStops(t *testing.T) {
	const reason = "the caller owns the retry"
	tests := []struct {
		name       string
		stop       func(agent *fakeAgent, seams *fixSeams, loop *FixRounds, cancel context.CancelFunc)
		wantRounds int
		wantSteps  int
		wantErr    func(error) bool
	}{
		{"the agent fails", func(agent *fakeAgent, _ *fixSeams, _ *FixRounds, _ context.CancelFunc) {
			agent.errFn = func(call int) error {
				if call == 2 {
					return errors.New("exit status 1")
				}
				return nil
			}
		}, 1, 2, func(err error) bool {
			var stop *StopError
			return errors.As(err, &stop) && stop.Reason == StopAgentExit
		}},
		{"the commit is refused", func(agent *fakeAgent, _ *fixSeams, _ *FixRounds, _ context.CancelFunc) {
			edit := agent.edit
			agent.edit = func(dir string) error {
				if agent.calls == 2 {
					if err := os.MkdirAll(filepath.Join(dir, workflowDir), 0o700); err != nil {
						return err
					}
					return os.WriteFile(filepath.Join(dir, workflowDir, "ci.yml"), []byte("name: ci\n"), 0o600)
				}
				return edit(dir)
			}
		}, 1, 2, func(err error) bool {
			var stop *StopError
			return errors.As(err, &stop) && stop.Reason == StopWorkflowChange
		}},
		{"the push fails", func(_ *fakeAgent, seams *fixSeams, loop *FixRounds, _ context.CancelFunc) {
			loop.Push = func(ctx context.Context, branch, commit string) error {
				if len(seams.pushed) == 1 {
					return errors.New("push refused")
				}
				return seams.push(ctx, branch, commit)
			}
		}, 2, 2, func(err error) bool { return err != nil && strings.Contains(err.Error(), "push refused") }},
		{"the re-review fails", func(_ *fakeAgent, seams *fixSeams, loop *FixRounds, _ context.CancelFunc) {
			loop.Review = func(ctx context.Context, branch, commit string) (string, error) {
				if len(seams.reviewed) == 1 {
					return "", errors.New("review down")
				}
				return seams.review(ctx, branch, commit)
			}
		}, 2, 2, func(err error) bool { return err != nil && strings.Contains(err.Error(), "review down") }},
		{"the context is cancelled", func(_ *fakeAgent, seams *fixSeams, loop *FixRounds, cancel context.CancelFunc) {
			loop.Review = func(ctx context.Context, branch, commit string) (string, error) {
				found, err := seams.review(ctx, branch, commit)
				cancel()
				return found, err
			}
		}, 1, 1, func(err error) bool { return errors.Is(err, context.Canceled) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := initRepo(t)
			agent := fixAgent()
			agent.eventsFn = func(call int) string {
				if call == 1 {
					return fixEvents("```declined-findings\n" + findingRetry + " — " + reason + "\n```")
				}
				return fixEvents("")
			}
			deps, _, _ := testDeps(validObjects(), agent)
			seams := &fixSeams{t: t, blocks: []string{findingRetry + "\n" + findingSwallow, findingRetry}}
			wt := fixWorktree(t, repo)
			loop := fixLoop(t, wt, seams, findingRetry+"\n"+findingTimeout+"\n"+findingSwallow, 0)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tt.stop(agent, seams, &loop, cancel)

			res, err := RunFixRounds(ctx, deps, testConfig(t, repo), wt, loop)
			if !tt.wantErr(err) {
				t.Fatalf("err = %v, want the stopped step's own error", err)
			}
			if len(res.Rounds) != tt.wantRounds || len(res.Steps) != tt.wantSteps || res.Session != "ses_1" {
				t.Fatalf("result = %+v, want %d rounds, %d steps and round 1's session", res, tt.wantRounds, tt.wantSteps)
			}
			want := []DeclinedFinding{{Finding: findingRetry, Reason: reason}}
			if first := res.Rounds[0]; first.Commit == "" || !slices.Equal(first.Declined, want) || first.Resolved != 1 {
				t.Fatalf("round 1 = %+v, want its commit, its decline and one finding resolved", first)
			}
		})
	}
}

// TestFixRoundsStopsWhenTheWorktreeIsTamperedWith: a round that changed the git
// configuration or moved HEAD off the pull request's branch is stopped before it
// commits, and nothing is published.
func TestFixRoundsStopsWhenTheWorktreeIsTamperedWith(t *testing.T) {
	tests := []struct {
		name   string
		tamper func(t *testing.T, repo, dir string)
		want   StopReason
	}{
		{"the git configuration changes", func(t *testing.T, repo, _ string) {
			appendFile(t, filepath.Join(repo, ".git", "config"), "[core]\n\tfsmonitor = /bin/true\n")
		}, StopGitTampered},
		{"HEAD moves to another branch", func(t *testing.T, _, dir string) {
			mustGit(t, dir, "checkout", "-q", "-b", "other")
		}, StopHeadMoved},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := initRepo(t)
			agent := fixAgent()
			edit := agent.edit
			agent.edit = func(dir string) error {
				tt.tamper(t, repo, dir)
				return edit(dir)
			}
			deps, _, _ := testDeps(validObjects(), agent)
			seams := &fixSeams{t: t}
			wt := fixWorktree(t, repo)

			_, err := RunFixRounds(context.Background(), deps, testConfig(t, repo), wt, fixLoop(t, wt, seams, findingRetry, 0))
			var stop *StopError
			if !errors.As(err, &stop) || stop.Reason != tt.want {
				t.Fatalf("err = %v, want a %s stop", err, tt.want)
			}
			if len(seams.pushed) != 0 {
				t.Fatalf("pushed %q, want nothing published from a tampered worktree", seams.pushed)
			}
		})
	}
}

// TestFixRoundsStopsWhenTheAgentMovesTheBranchToAnotherCommit: a round whose
// agent reset the branch would commit its edits onto the wrong base, so it stops
// before committing or publishing, whether the branch is the pull request's head
// or the last round's commit.
func TestFixRoundsStopsWhenTheAgentMovesTheBranchToAnotherCommit(t *testing.T) {
	for _, movedAt := range []int{1, 2} {
		t.Run(fmt.Sprintf("round %d", movedAt), func(t *testing.T) {
			repo := initRepo(t)
			agent := fixAgent()
			edit := agent.edit
			wt := fixWorktree(t, repo)
			mustGit(t, wt.Dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--allow-empty", "-qm", "pr commit")
			agent.edit = func(dir string) error {
				if agent.calls == movedAt {
					mustGit(t, dir, "reset", "-q", "--hard", "HEAD~1")
				}
				return edit(dir)
			}
			deps, _, _ := testDeps(validObjects(), agent)
			seams := &fixSeams{t: t, blocks: []string{findingSwallow}}

			res, err := RunFixRounds(context.Background(), deps, testConfig(t, repo), wt,
				fixLoop(t, wt, seams, findingRetry+"\n"+findingSwallow, 0))
			var stop *StopError
			if !errors.As(err, &stop) || stop.Reason != StopHeadMoved {
				t.Fatalf("err = %v, want a head-moved stop", err)
			}
			if len(seams.pushed) != movedAt-1 || len(res.Rounds) != movedAt-1 {
				t.Fatalf("pushed %q and %d rounds, want only the rounds before the branch moved", seams.pushed, len(res.Rounds))
			}
		})
	}
}

// TestFixRoundsMarksARoundWhoseReReviewDidNotComplete: counts of zero from a round
// whose re-review failed would read as a round that resolved nothing.
func TestFixRoundsMarksARoundWhoseReReviewDidNotComplete(t *testing.T) {
	repo := initRepo(t)
	deps, _, _ := testDeps(validObjects(), fixAgent())
	seams := &fixSeams{t: t, blocks: []string{findingSwallow}}
	wt := fixWorktree(t, repo)
	loop := fixLoop(t, wt, seams, findingRetry+"\n"+findingSwallow, 0)
	loop.Review = func(ctx context.Context, branch, commit string) (string, error) {
		if len(seams.reviewed) == 1 {
			return "", errors.New("review down")
		}
		return seams.review(ctx, branch, commit)
	}

	res, err := RunFixRounds(context.Background(), deps, testConfig(t, repo), wt, loop)
	if err == nil || len(res.Rounds) != 2 {
		t.Fatalf("err = %v with %d rounds, want the re-review's error and both rounds", err, len(res.Rounds))
	}
	if first := res.Rounds[0]; !first.Reviewed || first.Resolved != 1 {
		t.Fatalf("round 1 = %+v, want a reviewed round with one finding resolved", first)
	}
	if second := res.Rounds[1]; second.Commit == "" || second.Reviewed || second.Resolved != 0 || second.New != 0 {
		t.Fatalf("round 2 = %+v, want its commit, not reviewed and no counts", second)
	}
}

// TestFixRoundsNamesOnlyTheDeclinesTheLastReviewStillReports: a finding an earlier
// round declined that the review no longer reports is not left for the owner.
func TestFixRoundsNamesOnlyTheDeclinesTheLastReviewStillReports(t *testing.T) {
	repo := initRepo(t)
	agent := fixAgent()
	agent.eventsFn = func(call int) string {
		if call == 1 {
			return fixEvents("```declined-findings\n" + findingRetry + " — the caller owns the retry\n```")
		}
		return fixEvents("```declined-findings\n" + findingSwallow + " — reported upstream\n```")
	}
	deps, _, _ := testDeps(validObjects(), agent)
	seams := &fixSeams{t: t, blocks: []string{findingSwallow, findingSwallow}}

	wt := fixWorktree(t, repo)
	res, err := RunFixRounds(context.Background(), deps, testConfig(t, repo), wt,
		fixLoop(t, wt, seams, findingRetry+"\n"+findingTimeout+"\n"+findingSwallow, 0))
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != FixStopDeclined || len(res.Rounds) != 2 {
		t.Fatalf("result = %+v, want two rounds and the pull request handed over on a decline", res)
	}
	if !strings.Contains(res.Detail, "declined: "+findingSwallow+" — reported upstream") || strings.Contains(res.Detail, "the caller owns the retry") {
		t.Fatalf("detail = %q, want the decline still reported and not the one that went away", res.Detail)
	}
}

// TestFixRoundsStopsOnASecretInTheBranch: a round whose commit carries one of the
// run's secrets never reaches the pull request's branch.
func TestFixRoundsStopsOnASecretInTheBranch(t *testing.T) {
	const secret = "sk-test-0123456789"
	repo := initRepo(t)
	agent := fixAgent()
	agent.edit = func(dir string) error {
		return os.WriteFile(filepath.Join(dir, "leak.go"), []byte("package x\nconst k = \""+secret+"\"\n"), 0o600)
	}
	deps, _, _ := testDeps(validObjects(), agent)
	seams := &fixSeams{t: t}
	wt := fixWorktree(t, repo)
	cfg := testConfig(t, repo)
	cfg.Secrets = []string{secret}

	_, err := RunFixRounds(context.Background(), deps, cfg, wt, fixLoop(t, wt, seams, findingRetry, 0))
	var stop *StopError
	if !errors.As(err, &stop) || stop.Reason != StopSecretInBranch {
		t.Fatalf("err = %v, want a secret-in-branch stop", err)
	}
	if len(seams.pushed) != 0 {
		t.Fatalf("pushed %q, want a commit holding a secret left unpublished", seams.pushed)
	}
}

// TestFixRoundsCapsAtTheDefault is AC1's ceiling: progress on every round still
// stops at DefaultReviewFixRounds, with the last finding left open.
func TestFixRoundsCapsAtTheDefault(t *testing.T) {
	repo := initRepo(t)
	agent := fixAgent()
	deps, _, _ := testDeps(validObjects(), agent)
	seams := &fixSeams{t: t, blocks: []string{
		findingTimeout + "\n" + findingSwallow + "\n" + findingLogLine,
		findingSwallow + "\n" + findingLogLine,
		findingLogLine,
	}}
	findings := strings.Join([]string{findingRetry, findingTimeout, findingSwallow, findingLogLine}, "\n")

	wt := fixWorktree(t, repo)
	res, err := RunFixRounds(context.Background(), deps, testConfig(t, repo), wt,
		fixLoop(t, wt, seams, findings, 0))
	if err != nil {
		t.Fatal(err)
	}
	if res.Ready || res.Stop != FixStopCap {
		t.Fatalf("result = %+v, want the pull request handed over at the cap", res)
	}
	if len(res.Rounds) != DefaultReviewFixRounds || agent.calls != DefaultReviewFixRounds {
		t.Fatalf("%d rounds and %d agent calls, want the default cap of %d", len(res.Rounds), agent.calls, DefaultReviewFixRounds)
	}
	for i, round := range res.Rounds {
		if round.Resolved != 1 || round.New != 0 {
			t.Fatalf("round %d = %+v, want one finding resolved and none added", i+1, round)
		}
	}
	if !strings.Contains(res.Detail, findingLogLine) || !strings.Contains(res.Detail, "cap of 3") {
		t.Fatalf("detail = %q, want the finding left open and the cap", res.Detail)
	}
}

// TestFixRoundsStopsWithItsFindingsWhenTheBudgetIsSpent is AC2's budget half: no
// round starts once the loop's own share of the run cap is spent.
func TestFixRoundsStopsWithItsFindingsWhenTheBudgetIsSpent(t *testing.T) {
	repo := initRepo(t)
	agent := fixAgent()
	deps, _, _ := testDeps(validObjects(), agent)
	base := time.Now()
	calls := 0
	deps.Now = func() time.Time {
		calls++
		if calls == 1 {
			return base
		}
		return base.Add(fixRoundsBudget + time.Minute)
	}
	seams := &fixSeams{t: t}

	wt := fixWorktree(t, repo)
	res, err := RunFixRounds(context.Background(), deps, testConfig(t, repo), wt,
		fixLoop(t, wt, seams, findingRetry, 0))
	if err != nil {
		t.Fatal(err)
	}
	if res.Ready || res.Stop != FixStopBudget || len(res.Rounds) != 0 {
		t.Fatalf("result = %+v, want no round and the findings left open", res)
	}
	if agent.calls != 0 || len(seams.pushed) != 0 {
		t.Fatalf("the agent ran %d rounds and %d commits were published, want none", agent.calls, len(seams.pushed))
	}
	if !strings.Contains(res.Detail, findingRetry) || !strings.Contains(res.Detail, "budget was spent") {
		t.Fatalf("detail = %q, want the findings and the spent budget", res.Detail)
	}
}

// TestFixRoundsRefusesAWorktreeOnAnotherBranch keeps AC1's one commit on the pull
// request's branch: a worktree on any other branch is refused before a round runs.
func TestFixRoundsRefusesAWorktreeOnAnotherBranch(t *testing.T) {
	repo := initRepo(t)
	agent := fixAgent()
	deps, _, _ := testDeps(validObjects(), agent)
	wt := fixWorktree(t, repo)
	loop := fixLoop(t, wt, &fixSeams{t: t}, findingRetry, 0)
	loop.Branch = "wingman/some-other-ticket"

	if _, err := RunFixRounds(context.Background(), deps, testConfig(t, repo), wt, loop); err == nil {
		t.Fatal("err = nil, want a worktree on another branch refused")
	}
	if agent.calls != 0 {
		t.Fatalf("the agent ran %d rounds, want 0", agent.calls)
	}
}

// TestFixRoundsRefusesAWorktreeAtAnotherCommit: a worktree reset to some other
// commit is not the pull request's branch, and committing there would lose the
// commits the pull request holds when the round is published.
func TestFixRoundsRefusesAWorktreeAtAnotherCommit(t *testing.T) {
	repo := initRepo(t)
	agent := fixAgent()
	deps, _, _ := testDeps(validObjects(), agent)
	wt := fixWorktree(t, repo)
	loop := fixLoop(t, wt, &fixSeams{t: t}, findingRetry, 0)
	loop.Head = testSHA

	if _, err := RunFixRounds(context.Background(), deps, testConfig(t, repo), wt, loop); err == nil {
		t.Fatal("err = nil, want a worktree on another commit refused")
	}
	if agent.calls != 0 {
		t.Fatalf("the agent ran %d rounds, want 0", agent.calls)
	}
}

// TestFixRoundsNeedsBothOfItsSteps: a loop without a way to publish a commit or
// re-review its head would spend rounds on commits nothing reads.
func TestFixRoundsNeedsBothOfItsSteps(t *testing.T) {
	repo := initRepo(t)
	deps, _, _ := testDeps(validObjects(), fixAgent())
	wt := fixWorktree(t, repo)
	both := fixLoop(t, wt, &fixSeams{t: t}, findingRetry, 0)
	noPush, noReview := both, both
	noPush.Push, noReview.Review = nil, nil

	for name, loop := range map[string]FixRounds{"no push": noPush, "no review": noReview} {
		t.Run(name, func(t *testing.T) {
			if _, err := RunFixRounds(context.Background(), deps, testConfig(t, repo), wt, loop); err == nil {
				t.Fatal("err = nil, want the missing step refused")
			}
		})
	}
}

// TestFixRoundsDoesNothingOnACleanReview: findings are the only input, so a
// review that made none opens no round.
func TestFixRoundsDoesNothingOnACleanReview(t *testing.T) {
	repo := initRepo(t)
	agent := fixAgent()
	deps, _, _ := testDeps(validObjects(), agent)
	wt := fixWorktree(t, repo)

	res, err := RunFixRounds(context.Background(), deps, testConfig(t, repo), wt,
		fixLoop(t, wt, &fixSeams{t: t}, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ready || len(res.Rounds) != 0 || agent.calls != 0 {
		t.Fatalf("result = %+v with %d agent calls, want a clean review and no round", res, agent.calls)
	}
}

// TestFixRoundsStopsOnAnAgentFailure: a round the agent could not finish stops
// the loop with the agent's own classification, as a build does.
func TestFixRoundsStopsOnAnAgentFailure(t *testing.T) {
	repo := initRepo(t)
	agent := fixAgent()
	agent.err = errors.New("exit status 1")
	deps, _, _ := testDeps(validObjects(), agent)
	wt := fixWorktree(t, repo)

	_, err := RunFixRounds(context.Background(), deps, testConfig(t, repo), wt,
		fixLoop(t, wt, &fixSeams{t: t}, findingRetry, 0))
	var stop *StopError
	if !errors.As(err, &stop) || stop.Reason != StopAgentExit {
		t.Fatalf("err = %v, want an agent-exit stop", err)
	}
}

// TestFixRoundsStopsOnAWorkflowChange: the run's App cannot push a workflow
// change, so a round that touches one never reaches the branch.
func TestFixRoundsStopsOnAWorkflowChange(t *testing.T) {
	repo := initRepo(t)
	agent := fixAgent()
	agent.edit = func(dir string) error {
		if err := os.MkdirAll(filepath.Join(dir, workflowDir), 0o700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, workflowDir, "ci.yml"), []byte("name: ci\n"), 0o600)
	}
	deps, _, _ := testDeps(validObjects(), agent)
	seams := &fixSeams{t: t}

	wt := fixWorktree(t, repo)
	_, err := RunFixRounds(context.Background(), deps, testConfig(t, repo), wt,
		fixLoop(t, wt, seams, findingRetry, 0))
	var stop *StopError
	if !errors.As(err, &stop) || stop.Reason != StopWorkflowChange {
		t.Fatalf("err = %v, want a workflow-change stop", err)
	}
	if len(seams.pushed) != 0 {
		t.Fatalf("pushed %q, want a workflow change left unpublished", seams.pushed)
	}
}

// TestParseFixDeclinesRecordsEveryDecline: a line the model wrote with a plain
// hyphen or with no reason is still the owner's to read, never dropped.
func TestParseFixDeclinesRecordsEveryDecline(t *testing.T) {
	texts := []string{
		"the code is fixed",
		"```declined-findings\n\n```",
		"```declined-findings\n" + findingTimeout + " — the caller owns it\n```",
		"```declined-findings\n" + findingSwallow + " - reported upstream\n```",
		"```declined-findings\n" + findingLogLine + "\n```",
		"```declined-findings\n" + findingTimeout + " — the caller owns it\n" + findingSwallow + " — reported upstream\n```",
	}
	want := [][]DeclinedFinding{
		nil,
		nil,
		{{Finding: findingTimeout, Reason: "the caller owns it"}},
		{{Finding: findingSwallow, Reason: "reported upstream"}},
		{{Finding: findingLogLine, Reason: noDeclineReason}},
		{
			{Finding: findingTimeout, Reason: "the caller owns it"},
			{Finding: findingSwallow, Reason: "reported upstream"},
		},
	}
	for i := range texts {
		if got := parseFixDeclines(texts[i]); !slices.Equal(got, want[i]) {
			t.Errorf("declines of %q = %+v, want %+v", texts[i], got, want[i])
		}
	}
}
