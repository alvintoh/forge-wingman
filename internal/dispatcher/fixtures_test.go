package dispatcher

// scratch is the allowlisted repository the dispatcher tests dispatch into.
const scratch = "octo/scratch"

// samples is n merged PRs of repo, each carrying agentLines the agent wrote and
// ownerLines the owner changed, none of them auto-merged or reverted.
func samples(repo string, n int, agentLines, ownerLines int64) []Sample {
	out := make([]Sample, 0, n)
	for i := range n {
		out = append(out, Sample{
			Repo:    repo,
			PR:      1000 + i,
			Rewrite: Rewrite{AgentLines: agentLines, OwnerLines: ownerLines},
		})
	}
	return out
}

// sample is one merged PR of repo with the lines the agent and the owner wrote
// and the merge flags a test names.
func sample(repo string, agentLines, ownerLines int64, autoMerged, reverted bool) Sample {
	return Sample{
		Repo:       repo,
		Rewrite:    Rewrite{AgentLines: agentLines, OwnerLines: ownerLines},
		AutoMerged: autoMerged,
		Reverted:   reverted,
	}
}

// joined is a's samples followed by b's: the newest merges first.
func joined(a, b []Sample) []Sample {
	return append(append([]Sample{}, a...), b...)
}
