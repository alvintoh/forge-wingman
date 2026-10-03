package modelprobe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

const shapeDiff = `diff --git a/internal/collect/collector.go b/internal/collect/collector.go
--- a/internal/collect/collector.go
+++ b/internal/collect/collector.go
@@ -1,3 +1,3 @@
 func NewCollector() *Collector {
-	return &Collector{Interval: time.Second}
+	return &Collector{Interval: 2 * time.Second}
 }
`

var shapeTicket = runner.Ticket{
	ID:    "T-1",
	Title: "slow the collector's poll interval",
	Size:  "S",
	Body:  "Acceptance criteria:\n- the collector polls every two seconds",
}

// ShapeObservation is what one model's run under the review and plan agents' shape left behind.
//
// ErrorText is raw provider output: the judge reads it and nothing may store it.
type ShapeObservation struct {
	ErrorText  string
	Steps      int
	Errored    bool
	TimedOut   bool
	Capped     bool
	Unreadable bool
	HasBlock   bool
}

// ProbeShape runs one model under the restricted review agent on a fixed review input, in an empty directory.
//
// As with Probe, only infrastructure faults are errors.
func ProbeShape(ctx context.Context, d Deps, model string, cfg Config) (ShapeObservation, error) {
	if runner.Provider(model) != runner.ZenProvider {
		return ShapeObservation{}, fmt.Errorf("probing %s: %w", model, ErrForeignProvider)
	}
	dir, err := os.MkdirTemp("", "shape-")
	if err != nil {
		return ShapeObservation{}, fmt.Errorf("creating shape directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.ShapeTimeoutSeconds)*time.Second)
	defer cancel()
	w := &eventCounter{max: cfg.ShapeMaxToolCalls, cancel: cancel}
	var stderr bytes.Buffer
	runErr := d.NewShapeAgent(model).Run(runCtx, dir, "", runner.ReviewPrompt(shapeDiff, shapeTicket), w, &stderr)
	if ctx.Err() != nil {
		return ShapeObservation{}, fmt.Errorf("probing %s: %w", model, ctx.Err())
	}

	transcript := w.buf.Bytes()
	usage, usageErr := runner.SumUsage(bytes.NewReader(transcript))
	text, textErr := runner.FinalText(bytes.NewReader(transcript))
	_, blockErr := runner.ParseReviewFindings(text)
	timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)
	return ShapeObservation{
		ErrorText:  errorEvents(transcript) + "\n" + stderr.String(),
		Steps:      usage.Steps,
		Errored:    w.errored || (runErr != nil && !w.capped && !timedOut),
		TimedOut:   timedOut,
		Capped:     w.capped,
		Unreadable: usageErr != nil || textErr != nil,
		HasBlock:   blockErr == nil,
	}, nil
}

// JudgeShape grades obs against the review contract: no error, at least one
// step and a parseable review-findings block. The reason is a constant, never
// provider text, since the results file is public.
func JudgeShape(obs ShapeObservation) (Verdict, string) {
	switch {
	case obs.Errored && namesFreeTierRefusal(obs.ErrorText):
		return VerdictFail, ReasonRefusedFreeTier
	case obs.Errored:
		return VerdictFail, ReasonRunErrored
	case obs.Unreadable:
		return VerdictFail, ReasonTranscript
	case obs.TimedOut:
		return VerdictFail, ReasonTimedOut
	case obs.Capped:
		return VerdictFail, ReasonToolCallCap
	case obs.Steps == 0:
		return VerdictFail, ReasonNoSteps
	case !obs.HasBlock:
		return VerdictFail, ReasonNoFindingsBlock
	}
	return VerdictPass, ""
}

// namesFreeTierRefusal reports whether text reads as a free-tier refusal. The
// wording is an unverified guess until a live sweep has shown a real refusal.
func namesFreeTierRefusal(text string) bool {
	t := strings.ToLower(text)
	return strings.Contains(t, "free") && (strings.Contains(t, "403") || strings.Contains(t, "forbidden"))
}
