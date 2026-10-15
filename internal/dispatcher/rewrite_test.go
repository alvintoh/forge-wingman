package dispatcher

import (
	"math"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

func TestRewriteOfCountsTheRunnersCommitsForTheAgent(t *testing.T) {
	for name, tt := range map[string]struct {
		commits []Commit
		want    Rewrite
	}{
		"the runner's pair": {
			[]Commit{
				{Login: runner.CommitAuthorLogin, Additions: 10, Deletions: 2},
				{Name: "the owner", Email: "owner@example.com", Additions: 3, Deletions: 1},
			},
			Rewrite{AgentLines: 12, OwnerLines: 4, UnlabelledLines: 4},
		},
		"the bot's name and email on an owner's login": {
			[]Commit{{Name: runner.CommitAuthorName, Email: runner.CommitAuthorEmail, Login: "the-owner", Additions: 10}},
			Rewrite{OwnerLines: 10, UnlabelledLines: 10},
		},
		"the bot's name and email with no login": {
			[]Commit{{Name: runner.CommitAuthorName, Email: runner.CommitAuthorEmail, Additions: 7}},
			Rewrite{OwnerLines: 7, UnlabelledLines: 7},
		},
		"a login differing only in case": {
			[]Commit{{Login: "GitHub-Actions[bot]", Additions: 5}},
			Rewrite{AgentLines: 5},
		},
		"the owner alone": {
			[]Commit{{Name: "the owner", Email: "owner@example.com", Additions: 20, Deletions: 4}},
			Rewrite{OwnerLines: 24, UnlabelledLines: 24},
		},
		"a commit with no author": {
			[]Commit{{Additions: 3}},
			Rewrite{OwnerLines: 3, UnlabelledLines: 3},
		},
		"no commits at all": {nil, Rewrite{}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := RewriteOf(tt.commits); got != tt.want {
				t.Fatalf("RewriteOf = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestRateIsTheOwnersLinesOverTheAgents(t *testing.T) {
	for name, tt := range map[string]struct {
		lines Rewrite
		want  float64
	}{
		"nothing rewritten":   {Rewrite{AgentLines: 100}, 0},
		"a tenth rewritten":   {Rewrite{AgentLines: 100, OwnerLines: 10}, 0.1},
		"all of it rewritten": {Rewrite{AgentLines: 100, OwnerLines: 100}, 1},
		"no lines at all":     {Rewrite{}, 0},
		"no agent lines":      {Rewrite{OwnerLines: 5}, math.Inf(1)},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tt.lines.Rate(); got != tt.want {
				t.Fatalf("Rate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWindowCapsTheTrailingSamples(t *testing.T) {
	trailing := samples(scratch, 5, 100, 0)
	for name, tt := range map[string]struct {
		n    int
		want int
	}{
		"a window under five": {3, 3},
		"a window over five":  {10, 5},
		"a window of none":    {0, 0},
		"a window of five":    {5, 5},
	} {
		t.Run(name, func(t *testing.T) {
			got := Evidence{Trailing: trailing}.Window(tt.n)
			if len(got) != tt.want {
				t.Fatalf("Window(%d) = %d samples, want %d", tt.n, len(got), tt.want)
			}
		})
	}
}

func TestLinesAndRateSumTheWindow(t *testing.T) {
	window := []Sample{
		sample(scratch, 100, 10, true, false),
		sample(scratch, 100, 0, true, false),
	}
	if got := Lines(window); got != (Rewrite{AgentLines: 200, OwnerLines: 10}) {
		t.Fatalf("Lines() = %+v, want 200 agent lines and 10 owner lines", got)
	}
	rate, prs := Rate(window)
	if rate != 0.05 || prs != 2 {
		t.Fatalf("Rate() = %v over %d PRs, want 0.05 over 2", rate, prs)
	}
}

func TestGateMissesAndRevertsCountWhatTheProductOwns(t *testing.T) {
	window := []Sample{
		sample(scratch, 100, 0, true, true),
		sample(scratch, 100, 0, false, true),
		{Repo: scratch, GateMiss: true},
		{Repo: scratch, GateMiss: true},
	}
	if got := Reverts(window); got != 1 {
		t.Fatalf("Reverts() = %d, want the auto-merged revert alone", got)
	}
	if got := GateMisses(window); got != 2 {
		t.Fatalf("GateMisses() = %d, want 2", got)
	}
	if first := FirstReverted(window); first == nil || !first.AutoMerged || !first.Reverted {
		t.Fatalf("FirstReverted() = %+v, want the first reverted auto-merge", first)
	}
	if none := FirstReverted(window[1:]); none != nil {
		t.Fatalf("FirstReverted() = %+v, want nil when only a hand-merged PR was reverted", none)
	}
}

func TestReposIsEverySampledRepositorySorted(t *testing.T) {
	window := []Sample{
		{Repo: "octo/scratch"},
		{Repo: "octo/other"},
		{Repo: "octo/scratch"},
		{},
	}
	got := Repos(window)
	if len(got) != 2 || got[0] != "octo/other" || got[1] != "octo/scratch" {
		t.Fatalf("Repos() = %v, want the two sampled repositories once each, sorted", got)
	}
}
