package runner

import (
	"fmt"
	"io"
)

// DEFAULT_NO_PROGRESS_STEPS is how many model requests a build phase may
// complete in a row with no edit or write before its run stops as stalled: the
// guard a build that loops over reading needs beside the wall clock alone,
// which a long stall otherwise spends in full. The number is a judgment call,
// to be tuned from recorded runs.
const DEFAULT_NO_PROGRESS_STEPS = 60

// The tool names the runner reads: an edit or a write is the progress the guard
// waits for, and a read is work that stops counting as progress once it runs
// long.
const (
	toolRead  = "read"
	toolEdit  = "edit"
	toolWrite = "write"
)

// agentProgress is what one agent invocation's event stream says it did: the
// files its read tool calls touched, the largest context any one request read,
// and how many requests in a row it completed with no edit or write.
type agentProgress struct {
	reads       []string
	noProgress  int
	peakContext int64
}

// readProgress scans the events an agent wrote and records what the stream says
// it did in sum — the files it read and the largest context a request read. A
// stream it cannot read is a usage warning, never a stop: the run's record is
// still worth writing without it.
func readProgress(out io.ReadSeeker, sum *Summary) agentProgress {
	var p agentProgress
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		addUsageWarning(sum, err.Error())
		return p
	}
	if err := scanEvents(out, p.observe); err != nil {
		addUsageWarning(sum, err.Error())
		return p
	}
	sum.ReadFiles = mergePaths(sum.ReadFiles, p.reads)
	sum.PeakContextTokens = max(sum.PeakContextTokens, p.peakContext)
	return p
}

// observe records one step of the agent's stream: the files its request read,
// the context that request carried, and whether it made an edit or a write. A
// step that made one resets the no-progress count; a step that made none
// extends it, so the count is of requests that changed nothing.
func (p *agentProgress) observe(e event) {
	if e.Type != "step_finish" {
		return
	}
	progressed := false
	for _, t := range e.Part.Tools {
		switch t.Tool {
		case toolRead:
			if t.Path != "" {
				p.reads = append(p.reads, t.Path)
			}
		case toolEdit, toolWrite:
			progressed = true
		}
	}
	if progressed {
		p.noProgress = 0
	} else {
		p.noProgress++
	}
	if c := contextTokens(e); c > p.peakContext {
		p.peakContext = c
	}
}

// stalled reports whether phase ended having run DEFAULT_NO_PROGRESS_STEPS
// requests in a row with no edit or write. Only the build phase edits: a plan
// or review pass reading its way through the repository is doing its job.
func (p agentProgress) stalled(phase Phase) bool {
	return phase == PhaseBuild && p.noProgress >= DEFAULT_NO_PROGRESS_STEPS
}

// stalledDetail is what a stalled build's stop reports: the steps it made no
// progress over, and the largest context a request read.
func (p agentProgress) stalledDetail() string {
	return fmt.Sprintf("no edit or write in %d steps (peak context %d tokens)", p.noProgress, p.peakContext)
}

// contextTokens is what one request read: its fresh input plus what the
// provider served from its cache, which together are the context the model saw.
func contextTokens(e event) int64 {
	return e.Part.Tokens.Input + e.Part.Tokens.Cache.Read
}
