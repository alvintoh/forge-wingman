package modelprobe

import (
	"strings"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

func decided(set runner.ModelSet, results ...Result) Report {
	return Report{Results: results, Decision: &Decision{Set: set, Changed: true, Reason: DecisionChallengerWins}}
}

func TestVerify(t *testing.T) {
	set := runner.ModelSet{Default: "opencode/a", Fallbacks: []string{"opencode/b"}}
	report := decided(set, pass("opencode/a", 1, 1), pass("opencode/b", 2, 1))
	free := []string{"opencode/a", "opencode/b"}
	if err := Verify(report, set, free); err != nil {
		t.Fatalf("Verify of a matching set = %v", err)
	}
	for name, tc := range map[string]struct {
		set  runner.ModelSet
		rep  Report
		free []string
		want string
	}{
		"default not free":     {set, report, []string{"opencode/b"}, "opencode/a is not a free model"},
		"fallback not free":    {set, report, []string{"opencode/a"}, "opencode/b is not a free model"},
		"models edited":        {runner.ModelSet{Default: "opencode/a", Fallbacks: []string{"opencode/c"}}, report, append(free, "opencode/c"), "differ from the report"},
		"default did not pass": {set, decided(set, Result{Model: "opencode/a", Verdict: VerdictFail}, pass("opencode/b", 2, 1)), free, "opencode/a did not pass"},
		"no decision":          {set, Report{Results: report.Results}, free, "no decision"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := Verify(tc.rep, tc.set, tc.free); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Verify = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestReadReportRejectsWhatTheProbeCouldNotHaveWritten(t *testing.T) {
	good := `{"probed_at":"2026-10-01","config":{},"results":[{"model":"opencode/a","verdict":"pass","tool_calls":1,"transcript_bytes":1,"usage":{}}],"usage":{}}`
	if _, err := ReadReport([]byte(good)); err != nil {
		t.Fatalf("ReadReport(good) = %v", err)
	}
	for name, doc := range map[string]string{
		"body injected in a reason":               strings.Replace(good, `"verdict":"pass"`, `"verdict":"pass","reason":"[x](http://evil.example)"`, 1),
		"foreign model":                           strings.Replace(good, "opencode/a", "opencode-go/a", 1),
		"markup in a model":                       strings.Replace(good, "opencode/a", "opencode/a`|x", 1),
		"unknown verdict":                         strings.Replace(good, `"pass"`, `"great"`, 1),
		"model without the provider separator":    strings.Replace(good, "opencode/a", "opencode-a", 1),
		"model with a prefix before the provider": strings.Replace(good, "opencode/a", "x-opencode/a", 1),
		"skipped model outside the provider":      strings.Replace(good, `],"usage":{}}`, `],"skipped":["evil"],"usage":{}}`, 1),
		"decision reason outside the known set":   strings.Replace(good, `],"usage":{}}`, `],"decision":{"set":{"default":"opencode/a","fallbacks":[],"probed_at":"d","evidence":[]},"changed":true,"reason":"[x](http://evil.example)"},"usage":{}}`, 1),
		"decision set outside the provider":       strings.Replace(good, `],"usage":{}}`, `],"decision":{"set":{"default":"opencode-go/a","fallbacks":[],"probed_at":"d","evidence":[]},"changed":true,"reason":"incumbent-best"},"usage":{}}`, 1),
		"unknown field":                           strings.Replace(good, `"usage":{}}`, `"usage":{},"note":"hi"}`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadReport([]byte(doc)); err == nil {
				t.Fatal("ReadReport accepted it")
			}
		})
	}
}

func TestVerifyRejectsAFallbackThatDidNotPass(t *testing.T) {
	set := runner.ModelSet{Default: "opencode/a", Fallbacks: []string{"opencode/b"}}
	report := decided(set, pass("opencode/a", 1, 1), Result{Model: "opencode/b", Verdict: VerdictVoid})
	if err := Verify(report, set, []string{"opencode/a", "opencode/b"}); err == nil || !strings.Contains(err.Error(), "opencode/b did not pass") {
		t.Fatalf("Verify = %v, want the fallback that did not pass named", err)
	}
	report = decided(set, pass("opencode/a", 1, 1))
	if err := Verify(report, set, []string{"opencode/a", "opencode/b"}); err == nil || !strings.Contains(err.Error(), "opencode/b did not pass") {
		t.Fatalf("Verify = %v, want a fallback with no result to count as not passed", err)
	}
}
