package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fixJob is one fix-round job's setup: a repository holding the pull request's
// branch at its head, the deps and config to run in, and the input to run with.
type fixJob struct {
	repo  string
	head  string
	agent *fakeAgent
	deps  BuildDeps
	cfg   BuildConfig
	in    FixRoundInput
	now   time.Time
}

func newFixJob(t *testing.T, agent *fakeAgent) fixJob {
	t.Helper()
	repo := initRepo(t)
	mustGit(t, repo, "branch", fixBranch)
	head := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD"))
	deps, _, _ := testDeps(validObjects(), agent)
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	deps.Now = func() time.Time { return now }
	return fixJob{repo: repo, head: head, agent: agent, deps: deps, cfg: testConfig(t, repo), now: now,
		in: FixRoundInput{Branch: fixBranch, Head: head, Findings: findingRetry + "\n" + findingTimeout, OutDir: t.TempDir()}}
}

func (j fixJob) run() (FixRoundResult, error) {
	return FixRoundJob(context.Background(), j.deps, j.cfg, j.in)
}

// artifactFiles is the names of the files the job wrote to its directory.
func artifactFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func readFixState(t *testing.T, dir string) FixRoundsState {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, FixStateName))
	if err != nil {
		t.Fatal(err)
	}
	s, err := ParseFixRoundsState(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestFixRoundJobRefusesAGitHubTokenBeforeAnyWork(t *testing.T) {
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		t.Run(name, func(t *testing.T) {
			j := newFixJob(t, fixAgent())
			j.cfg.Identity = IdentityFromEnv(func(k string) string {
				return map[string]string{"WINGMAN_ACCOUNT": "octo", "GITHUB_REPOSITORY_OWNER": "octo", name: "ghs_x"}[k]
			})
			_, err := j.run()
			var stop *StopError
			if !errors.As(err, &stop) || stop.Reason != StopIdentityMismatch || !errors.Is(err, ErrIdentityMismatch) {
				t.Fatalf("err = %v, want the identity-mismatch stop", err)
			}
			if j.agent.calls != 0 {
				t.Fatalf("%d agent calls, want none", j.agent.calls)
			}
			if files := artifactFiles(t, j.in.OutDir); len(files) != 0 {
				t.Fatalf("wrote %v, want no artifact", files)
			}
			if out, _ := os.ReadDir(j.cfg.TempDir); len(out) != 0 {
				t.Fatalf("cut a worktree before the refusal: %v", out)
			}
		})
	}
}

func TestFixRoundJobBundlesTheRoundsOneCommitOnTheHead(t *testing.T) {
	j := newFixJob(t, fixAgent())
	res, err := j.run()
	if err != nil || res.Commit == "" {
		t.Fatalf("res = %+v, err = %v, want a commit", res, err)
	}
	bundle := filepath.Join(j.in.OutDir, FixBundleName)
	heads := strings.Fields(mustGit(t, j.repo, "bundle", "list-heads", bundle))
	if len(heads) != 2 || heads[0] != res.Commit || heads[1] != "refs/heads/"+fixBranch {
		t.Fatalf("bundle heads = %v, want the commit on %s alone", heads, fixBranch)
	}
	mustGit(t, j.repo, "fetch", "-q", bundle, "refs/heads/"+fixBranch+":refs/heads/check")
	if got := strings.TrimSpace(mustGit(t, j.repo, "rev-list", "--count", j.head+"..check")); got != "1" {
		t.Fatalf("bundle holds %s commits on the head, want 1", got)
	}
	if got := strings.Join(artifactFiles(t, j.in.OutDir), ","); got != "fix-state.json,fix.bundle" {
		t.Fatalf("artifact files = %s", got)
	}
}

func TestFixRoundJobSeedsTheStateOnTheFirstRound(t *testing.T) {
	j := newFixJob(t, fixAgent())
	res, err := j.run()
	if err != nil {
		t.Fatal(err)
	}
	got := readFixState(t, j.in.OutDir)
	if got.Round != 1 || res.Round != 1 || got.Calls != 1 || got.Session != "ses_1" || !got.StartedAt.Equal(j.now) {
		t.Fatalf("state = %+v, want round 1, 1 call, session ses_1 and a start at now", got)
	}
	if want := []string{findingRetry, findingTimeout}; !slices.Equal(got.Open, want) {
		t.Fatalf("open = %v, want %v", got.Open, want)
	}
	if len(got.Rounds) != 1 || got.Rounds[0].Commit != res.Commit || got.Rounds[0].Reviewed {
		t.Fatalf("rounds = %+v, want this round's own unreviewed record", got.Rounds)
	}
}

func TestFixRoundJobContinuesFromTheStateAgainstItsOwnStart(t *testing.T) {
	j := newFixJob(t, fixAgent())
	started := j.now.Add(-20 * time.Minute)
	declined := []DeclinedFinding{{Finding: findingSwallow, Reason: "intended"}}
	prev := NewFixRoundsState(1, 1, started, "ses_0", []string{findingRetry, findingTimeout}, declined).
		WithRound(ReviewFixRound{Step: Step{Phase: PhaseFix, Round: 1}, Findings: []string{findingRetry, findingTimeout}, Commit: "c1"})
	j.in.State = &prev
	j.in.Findings = findingTimeout + "\n" + findingLogLine + "\n" + findingSwallow

	res, err := j.run()
	if err != nil || res.Round != 2 {
		t.Fatalf("res = %+v, err = %v, want round 2", res, err)
	}
	if j.agent.sessions[0] != "ses_0" {
		t.Fatalf("agent resumed %q, want the state's session", j.agent.sessions[0])
	}
	if strings.Contains(j.agent.prompt, findingSwallow) || !strings.Contains(j.agent.prompt, findingLogLine) {
		t.Fatalf("prompt = %q, want the declined finding left out and the new one given", j.agent.prompt)
	}
	got := readFixState(t, j.in.OutDir)
	if !got.StartedAt.Equal(started) || got.Round != 2 || got.Calls != 2 {
		t.Fatalf("state = %+v, want round 2, 2 calls and the first round's start", got)
	}
	if want := []string{findingTimeout, findingLogLine}; !slices.Equal(got.Open, want) {
		t.Fatalf("open = %v, want the findings the round was given %v", got.Open, want)
	}
	if len(got.Declined) != 1 || got.Declined[0] != declined[0] {
		t.Fatalf("declined = %+v, want the earlier decline kept", got.Declined)
	}
	if len(got.Rounds) != 2 || !got.Rounds[0].Reviewed || got.Rounds[0].Resolved != 1 || got.Rounds[0].New != 1 {
		t.Fatalf("rounds = %+v, want the first round filled from the review (1 resolved, 1 new)", got.Rounds)
	}
}

func TestFixRoundJobBudgetPairAroundTheStartRoom(t *testing.T) {
	tests := []struct {
		name  string
		left  time.Duration
		calls int
		stop  FixStop
	}{
		{"2m59s left does not start", 2*time.Minute + 59*time.Second, 0, FixStopBudget},
		{"3m01s left starts", 3*time.Minute + time.Second, 1, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := newFixJob(t, fixAgent())
			state := NewFixRoundsState(1, 1, j.now.Add(tt.left-fixRoundsBudget), "ses_0", []string{findingRetry}, nil)
			j.in.State = &state
			res, err := j.run()
			if err != nil || res.Stop != tt.stop || j.agent.calls != tt.calls {
				t.Fatalf("res = %+v, err = %v, %d agent calls, want stop %q and %d calls", res, err, j.agent.calls, tt.stop, tt.calls)
			}
			if tt.calls == 0 {
				if res.Round != 1 || len(artifactFiles(t, j.in.OutDir)) != 0 {
					t.Fatalf("round %d with files %v, want the state's round and no artifact", res.Round, artifactFiles(t, j.in.OutDir))
				}
			}
		})
	}
}

func TestFixRoundJobEndsTheLoopAtTheCap(t *testing.T) {
	j := newFixJob(t, fixAgent())
	state := NewFixRoundsState(DefaultReviewFixRounds, DefaultReviewFixRounds, j.now, "ses_0", []string{findingRetry}, nil)
	j.in.State = &state
	res, err := j.run()
	if err != nil || res.Stop != FixStopCap || res.Round != DefaultReviewFixRounds || j.agent.calls != 0 {
		t.Fatalf("res = %+v, err = %v, %d agent calls, want the cap reached with no round run", res, err, j.agent.calls)
	}
}

func TestFixRoundJobReportsAnEmptyCommitForARoundThatChangesNoFile(t *testing.T) {
	j := newFixJob(t, &fakeAgent{events: fixEvents("")})
	res, err := j.run()
	if err != nil || res.Commit != "" || res.Stop != FixStopNoChange || res.Round != 1 {
		t.Fatalf("res = %+v, err = %v, want no commit, the no-change stop and round 1", res, err)
	}
	if got := strings.Join(artifactFiles(t, j.in.OutDir), ","); got != "fix-state.json" {
		t.Fatalf("artifact files = %s, want the state alone", got)
	}
	if got := readFixState(t, j.in.OutDir); got.Round != 1 || len(got.Rounds) != 1 || got.Rounds[0].Commit != "" {
		t.Fatalf("state = %+v, want round 1 with an empty-commit record", got)
	}
}

func TestFixRoundJobStopsWhenTheBranchIsNotAtTheHead(t *testing.T) {
	j := newFixJob(t, fixAgent())
	j.in.Head = strings.Repeat("a", 40)
	_, err := j.run()
	var stop *StopError
	if !errors.As(err, &stop) || stop.Reason != StopHeadMoved || j.agent.calls != 0 {
		t.Fatalf("err = %v with %d agent calls, want the head-moved stop and no agent run", err, j.agent.calls)
	}
}
