package modelprobe

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// modelName is the shape of a model id the probe will print or trust.
var modelName = regexp.MustCompile("^" + regexp.QuoteMeta(runner.ZenProvider) + "/[A-Za-z0-9][A-Za-z0-9._:-]*$")

// knownReasons are every reason a result or a decision may carry.
var knownReasons = []string{
	"", ReasonFabricatedBurst, ReasonBurstUnreadable, ReasonNoWork, ReasonRunErrored, ReasonEventShape,
	ReasonTranscript, ReasonTimedOut, ReasonToolCallCap, ReasonOverToolCallBar,
	DecisionChallengerWins, DecisionWithinMargin, DecisionIncumbentBest, DecisionIncumbentExcluded,
	DecisionNoSurvivors, DecisionFallbacksReordered,
}

// Report is one probe run's full record, kept as the results file.
type Report struct {
	ProbedAt string       `json:"probed_at"`
	Config   Config       `json:"config"`
	Results  []Result     `json:"results"`
	Skipped  []string     `json:"skipped,omitempty"`
	Decision *Decision    `json:"decision,omitempty"`
	Usage    runner.Usage `json:"usage"`
}

// NewReport totals the run's spend; decision is nil when the run made none.
func NewReport(probedAt string, cfg Config, sw Sweep, decision *Decision) Report {
	r := Report{ProbedAt: probedAt, Config: cfg, Results: sw.Results, Skipped: sw.Skipped, Decision: decision}
	for _, res := range sw.Results {
		r.Usage.Input += res.Usage.Input
		r.Usage.Output += res.Usage.Output
		r.Usage.Reasoning += res.Usage.Reasoning
		r.Usage.CacheRead += res.Usage.CacheRead
		r.Usage.CacheWrite += res.Usage.CacheWrite
		r.Usage.Cost += res.Usage.Cost
		r.Usage.Steps += res.Usage.Steps
	}
	return r
}

// Marshal renders the report as the indented, newline-terminated results file.
func (r Report) Marshal() ([]byte, error) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding report: %w", err)
	}
	return append(b, '\n'), nil
}

// ReadReport decodes a results file and rejects anything the probe could not have written.
func ReadReport(b []byte) (Report, error) {
	var r Report
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return Report{}, fmt.Errorf("decoding report: %w", err)
	}
	for _, res := range r.Results {
		switch {
		case !modelName.MatchString(res.Model):
			return Report{}, fmt.Errorf("result model %q is not a %s model", res.Model, runner.ZenProvider)
		case res.Verdict != VerdictPass && res.Verdict != VerdictFail && res.Verdict != VerdictVoid:
			return Report{}, fmt.Errorf("result for %s has verdict %q", res.Model, res.Verdict)
		case !slices.Contains(knownReasons, res.Reason):
			return Report{}, fmt.Errorf("result for %s has reason %q", res.Model, res.Reason)
		}
	}
	for _, m := range r.Skipped {
		if !modelName.MatchString(m) {
			return Report{}, fmt.Errorf("skipped model %q is not a %s model", m, runner.ZenProvider)
		}
	}
	if r.Decision != nil {
		if !slices.Contains(knownReasons, r.Decision.Reason) {
			return Report{}, fmt.Errorf("decision has reason %q", r.Decision.Reason)
		}
		if err := r.Decision.Set.Validate(); err != nil {
			return Report{}, fmt.Errorf("decision set: %w", err)
		}
	}
	return r, nil
}

// Verify checks that set is exactly the decision r records and that every model it
// names is on the free roster and passed this run.
func Verify(r Report, set runner.ModelSet, free []string) error {
	if r.Decision == nil {
		return errors.New("report records no decision")
	}
	want, err := r.Decision.Set.Marshal()
	if err != nil {
		return err
	}
	got, err := set.Marshal()
	if err != nil {
		return err
	}
	if !bytes.Equal(want, got) {
		return errors.New("models differ from the report's decision")
	}
	for _, m := range append([]string{set.Default}, set.Fallbacks...) {
		if !slices.Contains(free, m) {
			return fmt.Errorf("%s is not a free model on the roster", m)
		}
		if res, ok := find(r.Results, m); !ok || res.Verdict != VerdictPass {
			return fmt.Errorf("%s did not pass this run", m)
		}
	}
	return nil
}

// Summary renders the report as the change PR's body: the decision, then every model's verdict.
func (r Report) Summary() string {
	var b strings.Builder
	if r.Decision != nil {
		fmt.Fprintf(&b, "Default: `%s` (%s).\n\n", r.Decision.Set.Default, r.Decision.Reason)
	}
	b.WriteString("| Model | Verdict | Tool calls | Reason |\n|---|---|---|---|\n")
	for _, res := range r.Results {
		fmt.Fprintf(&b, "| `%s` | %s | %d | %s |\n", res.Model, res.Verdict, res.ToolCalls, res.Reason)
	}
	if len(r.Skipped) > 0 {
		fmt.Fprintf(&b, "\nNot probed this run (model cap): %s.\n", strings.Join(r.Skipped, ", "))
	}
	fmt.Fprintf(&b, "\nProbe spend: %d input, %d output tokens, cost %.4f.\n", r.Usage.Input, r.Usage.Output, r.Usage.Cost)
	return b.String()
}
