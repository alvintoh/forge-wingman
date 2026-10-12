package dispatcher

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

const (
	peakModel = "command-code/deepseek/deepseek-v4.1-flash"
	flatModel = "gemini/gemini-3.8-flash"
	freeModel = "command-code/poolside/laguna-s-2.1-free"
)

// fakePeakModes reports the peak mode configured, or fails to read it.
type fakePeakModes struct {
	mode PeakMode
	err  error
}

func (m fakePeakModes) Mode(context.Context) (PeakMode, error) { return m.mode, m.err }

// peakConfig is a dispatch config whose default model prices peak hours.
func peakConfig() Config {
	c := buildConfig
	c.Model = peakModel
	return c
}

// peakPoll polls one candidate at at, under mode, with an estimate of minutes.
func peakPoll(t *testing.T, cand Candidate, cfg Config, mode PeakMode, at time.Time, minutes int64) (*fakeQueue, *fakeWorkflow, Result) {
	t.Helper()
	q := &fakeQueue{candidates: []Candidate{cand}}
	w := &fakeWorkflow{}
	deps := pollDeps(fakeSource{}, q, w)
	deps.PeakModes = fakePeakModes{mode: mode}
	deps.Estimator = fakeEstimator{bySize: map[string]Estimate{"M": {Minutes: minutes}}}
	deps.Now = func() time.Time { return at }
	res, err := Poll(context.Background(), deps, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return q, w, res
}

func monday(hour, minute int) time.Time { return time.Date(2026, 9, 28, hour, minute, 0, 0, time.UTC) }

func TestPollHoldsARunWhileItsModelsPeakWindowIsOpenOrReached(t *testing.T) {
	for name, tt := range map[string]struct {
		mode    PeakMode
		at      time.Time
		minutes int64
		held    bool
	}{
		"the window is open":                   {PeakOffPeakOnly, monday(2, 0), 30, true},
		"the second window is open":            {PeakOffPeakOnly, monday(9, 59), 30, true},
		"the estimate reaches the next window": {PeakOffPeakOnly, monday(0, 45), 30, true},
		"the estimate ends on the window open": {PeakOffPeakOnly, monday(0, 45), 15, true},
		"the estimate ends before the window":  {PeakOffPeakOnly, monday(0, 45), 14, false},
		"no estimate, window not yet open":     {PeakOffPeakOnly, monday(0, 45), 0, false},
		"no estimate, window open":             {PeakOffPeakOnly, monday(2, 0), 0, true},
		"the first poll after a window closes": {PeakOffPeakOnly, monday(4, 0), 30, false},
		"the first poll after the last closes": {PeakOffPeakOnly, monday(10, 0), 30, false},
		"between the windows, run reaches one": {PeakOffPeakOnly, monday(5, 40), 30, true},
		"a weekend inside the hours":           {PeakOffPeakOnly, time.Date(2026, 9, 26, 2, 0, 0, 0, time.UTC), 30, false},
		"always, inside the window":            {PeakAlways, monday(2, 0), 30, false},
		"always, reaching the window":          {PeakAlways, monday(0, 45), 30, false},
	} {
		t.Run(name, func(t *testing.T) {
			q, w, res := peakPoll(t, Candidate{RunID: "run-a", Repo: "octo/scratch", Size: "M"}, peakConfig(), tt.mode, tt.at, tt.minutes)
			if len(q.tryClaims) != 1 || q.tryClaims[0].facts.PeakHeld != tt.held {
				t.Fatalf("tryClaims = %+v, want PeakHeld %v", q.tryClaims, tt.held)
			}
			if tt.held {
				if len(w.claims) != 0 || len(res.Deferrals) != 1 || res.Deferrals[0].Ceiling != "peak-hours" {
					t.Fatalf("dispatched %+v, deferrals %+v, want the run held on peak-hours", w.claims, res.Deferrals)
				}
				return
			}
			if len(w.claims) != 1 || len(res.Deferrals) != 0 {
				t.Fatalf("dispatched %+v, deferrals %+v, want the run claimed", w.claims, res.Deferrals)
			}
		})
	}
}

func TestPollNeverHoldsARunWhoseModelHasNoPeakWindow(t *testing.T) {
	flat := peakConfig()
	flat.Model = flatModel
	unconfigured := peakConfig()
	unconfigured.Model = "p/paid"
	for name, cfg := range map[string]Config{"a flat-priced paid model": flat, "a model with no rate card": unconfigured} {
		t.Run(name, func(t *testing.T) {
			q, w, _ := peakPoll(t, Candidate{RunID: "run-a", Repo: "octo/scratch", Size: "M"}, cfg, PeakOffPeakOnly, monday(2, 0), 30)
			if q.tryClaims[0].facts.PeakHeld || len(w.claims) != 1 {
				t.Fatalf("tryClaims = %+v, dispatched %+v, want the run claimed", q.tryClaims, w.claims)
			}
		})
	}
}

func TestPollNeverHoldsABuildOnTheFreeTier(t *testing.T) {
	cand := Candidate{RunID: "run-a", Repo: "octo/scratch", Size: "M", Stage: StageBuild,
		LastResort: &runner.ModelLabels{Build: freeModel, Review: "command-code/inclusionai/ling-3.1-flash:free"}}
	cfg := peakConfig()
	cfg.TwoStage = true
	q, w, _ := peakPoll(t, cand, cfg, PeakOffPeakOnly, monday(2, 0), 30)
	if q.tryClaims[0].facts.PeakHeld || len(w.claims) != 1 {
		t.Fatalf("tryClaims = %+v, dispatched %+v, want the free-tier build claimed inside the window", q.tryClaims, w.claims)
	}
}

func TestPollHoldsABuildStageOnTheModelItsPlanStageChose(t *testing.T) {
	cand := Candidate{RunID: "run-a", Repo: "octo/scratch", Size: "M", Stage: StageBuild, Model: peakModel}
	cfg := buildConfig
	cfg.Model = flatModel
	cfg.TwoStage = true
	q, w, res := peakPoll(t, cand, cfg, PeakOffPeakOnly, monday(2, 0), 30)
	if !q.tryClaims[0].facts.PeakHeld || len(w.claims) != 0 || res.Deferrals[0].Ceiling != "peak-hours" {
		t.Fatalf("tryClaims = %+v, dispatched %+v, want the build held on its own model's window", q.tryClaims, w.claims)
	}
}

func TestPollJudgesAPlanStageByItsOwnEstimate(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/scratch", Size: "M"}}}
	w := &fakeWorkflow{}
	deps := pollDeps(fakeSource{}, q, w)
	deps.Estimator = fakeEstimator{
		bySize:     map[string]Estimate{"M": {Minutes: 30}},
		planBySize: map[string]Estimate{"M": {Minutes: 5}},
	}
	deps.Now = func() time.Time { return monday(5, 40) }
	cfg := peakConfig()
	cfg.TwoStage = true
	if _, err := Poll(context.Background(), deps, cfg); err != nil {
		t.Fatal(err)
	}
	if q.tryClaims[0].facts.PeakHeld || len(w.claims) != 1 || w.claims[0].Stage != StagePlan {
		t.Fatalf("tryClaims = %+v, dispatched %+v, want the 5-minute plan stage to finish before the 06:00 window", q.tryClaims, w.claims)
	}
}

func TestPollNeitherMovesNorNoticesAHeldRun(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/scratch", Size: "M"}}}
	w := &fakeWorkflow{}
	deps := usableDeps(q, w)
	deps.Estimator = fakeEstimator{bySize: map[string]Estimate{"M": {Minutes: 30}}}
	deps.Now = func() time.Time { return monday(2, 0) }
	notices := &fakeNotices{}
	deps.Notices = notices
	cfg := walkConfig()
	cfg.Model = peakModel
	res, err := Poll(context.Background(), deps, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.tryClaims) != 1 || len(w.claims) != 0 || len(res.Deferrals) != 1 || len(notices.raised) != 0 {
		t.Fatalf("claims %d, dispatched %+v, deferrals %+v, notices %+v, want one claim, held, no notice",
			len(q.tryClaims), w.claims, res.Deferrals, notices.raised)
	}
}

func TestPollHoldsARunMovedOntoAPlanWhoseModelPricesPeakHours(t *testing.T) {
	q := &fakeQueue{
		candidates:   []Candidate{{RunID: "run-a", Repo: "octo/scratch", Size: "M"}},
		planBindings: map[string]string{"p": "p-5h"},
	}
	deps := usableDeps(q, &fakeWorkflow{})
	deps.Estimator = fakeEstimator{bySize: map[string]Estimate{"M": {Minutes: 30}}}
	deps.Now = func() time.Time { return monday(2, 0) }
	cfg := walkConfig()
	cfg.NextPlans = cfg.NextPlans[:1]
	cfg.NextPlans[0].Models.Build = peakModel
	res, err := Poll(context.Background(), deps, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.tryClaims) != 2 || q.tryClaims[0].facts.PeakHeld || !q.tryClaims[1].facts.PeakHeld {
		t.Fatalf("tryClaims = %+v, want the first plan free to claim and the move held", q.tryClaims)
	}
	if len(res.Dispatched) != 0 || res.Deferrals[0].Ceiling != "peak-hours" {
		t.Fatalf("dispatched %q, deferrals %+v, want the run held on peak-hours, not on the free tier", res.Dispatched, res.Deferrals)
	}
}

func TestPollHoldsWhenThePeakModeCannotBeRead(t *testing.T) {
	for name, modes := range map[string]fakePeakModes{
		"the read fails":  {err: errFirestore},
		"an unknown mode": {mode: "sometimes"},
	} {
		t.Run(name, func(t *testing.T) {
			q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/scratch", Size: "M"}}}
			var logs bytes.Buffer
			deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
			deps.PeakModes = modes
			deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			deps.Now = func() time.Time { return monday(2, 0) }
			if _, err := Poll(context.Background(), deps, peakConfig()); err != nil {
				t.Fatal(err)
			}
			if !q.tryClaims[0].facts.PeakHeld {
				t.Fatalf("facts = %+v, want the run held as off-peak-only holds", q.tryClaims[0].facts)
			}
			if !strings.Contains(logs.String(), "msg=peakModeUnread") {
				t.Fatalf("log = %q, want peakModeUnread", logs.String())
			}
		})
	}
}

func TestPollReadsThePeakModeOncePerPoll(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{
		{RunID: "run-a", Repo: "octo/scratch", Size: "M"}, {RunID: "run-b", Repo: "octo/other", Size: "M"},
	}}
	modes := &countingPeakModes{}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	deps.PeakModes = modes
	if _, err := Poll(context.Background(), deps, peakConfig()); err != nil {
		t.Fatal(err)
	}
	if modes.reads != 1 {
		t.Fatalf("reads = %d, want one per poll", modes.reads)
	}
}

// countingPeakModes counts the reads made of the mode.
type countingPeakModes struct{ reads int }

func (m *countingPeakModes) Mode(context.Context) (PeakMode, error) {
	m.reads++
	return PeakOffPeakOnly, nil
}

func TestPeakModeValid(t *testing.T) {
	for mode, want := range map[PeakMode]bool{PeakOffPeakOnly: true, PeakAlways: true, "": false, "Always": false} {
		if got := mode.Valid(); got != want {
			t.Errorf("PeakMode(%q).Valid() = %v, want %v", mode, got, want)
		}
	}
}

func TestAdmitHoldsAPeakHeldCandidateAheadOfEveryCapButTheHalts(t *testing.T) {
	in := AdmitInput{N: 1, Candidate: Subject{Repo: "octo/scratch"}, Facts: Facts{Limits: DefaultLimits, OpenPRsKnown: true}}
	for name, tt := range map[string]struct {
		mutate func(*AdmitInput)
		ok     bool
		want   string
	}{
		"peak held":               {func(in *AdmitInput) { in.Facts.PeakHeld = true }, false, ConditionPeakHours},
		"not peak held":           {func(*AdmitInput) {}, true, ""},
		"held ahead of the N cap": {func(in *AdmitInput) { in.Facts.PeakHeld = true; in.InFlight = []InFlight{{Repo: "x"}} }, false, ConditionPeakHours},
		"the breaker comes first": {func(in *AdmitInput) { in.Facts.PeakHeld, in.Facts.BreakerTripped = true, true }, false, ConditionCircuitBreaker},
		"the provider halt first": {func(in *AdmitInput) { in.Facts.PeakHeld, in.Facts.ProviderHalted = true, true }, false, CeilingProviderHalted},
	} {
		t.Run(name, func(t *testing.T) {
			c := in
			tt.mutate(&c)
			if ok, binding := Admit(c); ok != tt.ok || binding != tt.want {
				t.Fatalf("Admit = %v, %q, want %v, %q", ok, binding, tt.ok, tt.want)
			}
		})
	}
}
