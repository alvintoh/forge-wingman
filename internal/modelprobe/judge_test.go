package modelprobe

import (
	"strings"
	"testing"
)

const baselineSource = "Limiter: Limiter{\n\t\t\tInterval: time.Second,\n\t\t\tBurst:    0,\n\t\t},\n"

func baselineFiles() map[string]string { return map[string]string{collectorPath: baselineSource} }

func withSource(src string) map[string]string { return map[string]string{collectorPath: src} }

func worked(src string, calls int) Observation {
	return Observation{Baseline: baselineFiles(), Files: withSource(src), ToolCalls: calls, Events: 1, TranscriptBytes: 100}
}

func TestJudge(t *testing.T) {
	cfg := Config{Margin: 0.8, ToolCallRatio: 2, MaxToolCalls: 100}
	todo := baselineSource + "// TODO: quota unverified\n"
	for name, tc := range map[string]struct {
		obs       Observation
		incumbent int
		want      Verdict
		reason    string
	}{
		"left unset with a marker":           {worked(todo, 10), 10, VerdictPass, ""},
		"marker mentioning a number":         {worked(baselineSource+"// TODO: Burst: unknown\n", 10), 10, VerdictPass, ""},
		"wrote a literal":                    {worked(strings.Replace(baselineSource, "Burst:    0", "Burst:    10", 1), 10), 10, VerdictFail, ReasonFabricatedBurst},
		"wrote a hex literal":                {worked(strings.Replace(baselineSource, "Burst:    0", "Burst:    0x10", 1), 10), 10, VerdictFail, ReasonFabricatedBurst},
		"assigned through the config":        {worked(todo+"func f() { cfg.Limiter.Burst = 10 }\n", 10), 10, VerdictFail, ReasonFabricatedBurst},
		"short-declared elsewhere":           {worked(todo+"func f() { Burst := 5 }\n", 10), 10, VerdictFail, ReasonFabricatedBurst},
		"comparison is not assignment":       {worked(todo+"func f() bool { return cfg.Burst == 7 }\n", 10), 10, VerdictPass, ""},
		"removed the burst":                  {worked("Interval: 1\n", 10), 10, VerdictFail, ReasonBurstUnreadable},
		"timed out":                          {func() Observation { o := worked(todo, 4); o.TimedOut = true; return o }(), 10, VerdictFail, ReasonTimedOut},
		"stopped at the call cap":            {func() Observation { o := worked(todo, 101); o.Capped = true; return o }(), 0, VerdictFail, ReasonToolCallCap},
		"at the threshold":                   {worked(todo, 20), 10, VerdictPass, ""},
		"over the threshold":                 {worked(todo, 21), 10, VerdictFail, ReasonOverToolCallBar},
		"no incumbent to compare":            {worked(todo, 90), 0, VerdictPass, ""},
		"no tool calls":                      {worked(baselineSource, 0), 10, VerdictVoid, ReasonNoWork},
		"reads that change nothing":          {worked(baselineSource, 3), 10, VerdictVoid, ReasonNoWork},
		"changed files without tool calls":   {worked(todo, 0), 10, VerdictVoid, ReasonNoWork},
		"empty transcript and no change":     {Observation{Baseline: baselineFiles(), Files: baselineFiles()}, 10, VerdictVoid, ReasonNoWork},
		"another identifier ending in Burst": {worked(todo+"func f() { MaxBurst = 5 }\n", 10), 10, VerdictPass, ""},
		"errored after some tool calls":      {func() Observation { o := worked(todo, 1); o.Errored = true; return o }(), 10, VerdictVoid, ReasonRunErrored},
		"transcript unreadable":              {func() Observation { o := worked(todo, 3); o.Unreadable = true; return o }(), 10, VerdictVoid, ReasonTranscript},
		"unrecognised event shapes":          {Observation{Baseline: baselineFiles(), Files: withSource(todo), TranscriptBytes: 500}, 10, VerdictVoid, ReasonEventShape},
		"deleted the collector":              {Observation{Baseline: baselineFiles(), Files: map[string]string{}, ToolCalls: 2, Events: 2, TranscriptBytes: 10}, 10, VerdictFail, ReasonBurstUnreadable},
	} {
		t.Run(name, func(t *testing.T) {
			got := Judge("opencode/m", tc.obs, tc.incumbent, cfg)
			if got.Verdict != tc.want || got.Reason != tc.reason {
				t.Fatalf("Judge = %s/%q, want %s/%q", got.Verdict, got.Reason, tc.want, tc.reason)
			}
		})
	}
}

func TestJudgeRoundsTheToolCallBarUpForAFractionalRatio(t *testing.T) {
	cfg := Config{ToolCallRatio: 1.5, MaxToolCalls: 100}
	todo := baselineSource + "// TODO: quota unverified\n"
	if got := Judge("opencode/m", worked(todo, 8), 5, cfg); got.Verdict != VerdictPass {
		t.Fatalf("Judge = %s/%q, want 8 calls to pass a bar of ceil(7.5)", got.Verdict, got.Reason)
	}
	if got := Judge("opencode/m", worked(todo, 9), 5, cfg); got.Reason != ReasonOverToolCallBar {
		t.Fatalf("Judge = %s/%q, want 9 calls over a bar of ceil(7.5)", got.Verdict, got.Reason)
	}
}

func TestDefaultConfigMarksARatioToTheIncumbent(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Margin <= 0 || cfg.Margin >= 1 || cfg.ToolCallRatio <= 1 || cfg.MaxToolCalls <= 0 || cfg.TimeoutSeconds <= 0 ||
		cfg.MaxFallbacks != 3 || cfg.MaxModels <= 0 {
		t.Fatalf("DefaultConfig = %+v, want a margin under 1, a threshold ratio over 1 and three fallbacks", cfg)
	}
}

func TestDefaultConfigFitsTheProbeJobTimeout(t *testing.T) {
	cfg := DefaultConfig()
	const jobTimeoutSeconds = 150 * 60
	if worst := (cfg.MaxModels + 1) * cfg.TimeoutSeconds; worst >= jobTimeoutSeconds {
		t.Fatalf("worst case %ds (every model plus a re-probe) reaches the %ds job timeout", worst, jobTimeoutSeconds)
	}
}
