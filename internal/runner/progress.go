package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// DEFAULT_NO_PROGRESS_STEPS is how many requests a build phase may complete in a row with no file change before it is stopped; a judgment call, to be tuned from recorded runs.
const DEFAULT_NO_PROGRESS_STEPS = 60

const toolRead = "read"

// fileChangingTools are the tool names the adapters emit for a call that changes files.
var fileChangingTools = map[string]bool{
	"edit": true, "write": true, "patch": true, "multiedit": true, "apply_patch": true, "ast_edit": true,
}

// agentProgress is what one agent invocation's event stream says it did.
type agentProgress struct {
	reads       []string
	readSeen    map[string]bool
	noProgress  int
	peakContext int64
}

// readProgress scans the finished event file for the summary: files read and peak context; an unreadable stream is a usage warning, never a stop.
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
	if n := len(sum.Steps); n > 0 {
		sum.Steps[n-1].PeakContextTokens = p.peakContext
	}
	for _, st := range sum.Steps {
		sum.PeakContextTokens = max(sum.PeakContextTokens, st.PeakContextTokens)
	}
	return p
}

// observe records one step: files read (distinct, capped), peak context, and the trailing count of steps with no file change.
func (p *agentProgress) observe(e event) {
	if e.Type != "step_finish" {
		return
	}
	progressed := false
	for _, t := range e.Part.Tools {
		switch {
		case t.Tool == toolRead:
			if t.Path != "" && !p.readSeen[t.Path] && len(p.reads) < maxEditedFiles {
				if p.readSeen == nil {
					p.readSeen = map[string]bool{}
				}
				p.readSeen[t.Path] = true
				p.reads = append(p.reads, t.Path)
			}
		case fileChangingTools[t.Tool]:
			progressed = true
		}
	}
	// The count is the trailing run, so a stall broken by a change restarts from zero and never stops the run.
	if progressed {
		p.noProgress = 0
	} else {
		p.noProgress++
	}
	if c := contextTokens(e); c > p.peakContext {
		p.peakContext = c
	}
}

// progressTap observes an agent's event stream as it is written and calls stop once a build phase stalls; it never fails a write, so an unreadable stream cannot stop a run.
type progressTap struct {
	mu      sync.Mutex
	phase   Phase
	stop    func()
	p       agentProgress
	buf     []byte
	skip    bool
	unknown bool
	stopped bool
}

func newProgressTap(phase Phase, stop func()) *progressTap {
	return &progressTap{phase: phase, stop: stop}
}

// Write implements io.Writer over the stream's newline-delimited events.
func (t *progressTap) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range b {
		if c != '\n' {
			if !t.skip {
				t.buf = append(t.buf, c)
				if len(t.buf) > maxEventLine {
					// An overlong step cannot be read for its tools, so it counts as unknown and fails open.
					t.unknown = bytes.Contains(t.buf[:min(len(t.buf), 4096)], []byte(`"step_finish"`))
					t.buf, t.skip = nil, true
				}
			}
			continue
		}
		t.line()
		t.buf, t.skip, t.unknown = t.buf[:0], false, false
	}
	return len(b), nil
}

// finish observes a final line the stream ended without a newline.
func (t *progressTap) finish() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.line()
	t.buf = nil
}

func (t *progressTap) line() {
	if t.skip {
		if t.unknown {
			t.p.noProgress = 0
		}
		return
	}
	if len(t.buf) == 0 {
		return
	}
	var e event
	if json.Unmarshal(t.buf, &e) != nil {
		return
	}
	t.p.observe(e)
	if !t.stopped && t.p.stalled(t.phase) {
		t.stopped = true
		t.stop()
	}
}

// didStop reports whether the tap stopped the run.
func (t *progressTap) didStop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stopped
}

// stalled reports whether a build phase is at the limit; plan and review passes never edit, so they are exempt.
func (p agentProgress) stalled(phase Phase) bool {
	return phase == PhaseBuild && p.noProgress >= DEFAULT_NO_PROGRESS_STEPS
}

// stalledDetail is the stop's detail: the idle steps and the largest context read.
func (p agentProgress) stalledDetail() string {
	return fmt.Sprintf("no file change in %d steps (peak context %d tokens)", p.noProgress, p.peakContext)
}

// contextTokens is a request's context: the translated field, else its fresh input plus its cache reads.
func contextTokens(e event) int64 {
	if e.Part.ContextTokens != 0 {
		return e.Part.ContextTokens
	}
	return e.Part.Tokens.Input + e.Part.Tokens.Cache.Read
}
