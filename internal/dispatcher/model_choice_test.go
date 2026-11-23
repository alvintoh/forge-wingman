package dispatcher

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/planrefresh"
	"github.com/alvintoh/forge-wingman/internal/providers"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// The priced models the choice is tested over, each with its real rate card in
// providers.json: Flash carries a weekday peak, Haiku and Muse none, and the
// Gemini model is a free last-resort one.
const (
	flash  = "opencode-go/deepseek-v4.1-flash"
	haiku  = "opencode-go/claude-haiku-5-5"
	muse   = "opencode-go/muse-spark-1.3-contributor"
	gemini = "gemini/gemini-3.8-flash"
)

// weekdayPeak is a Wednesday inside Flash's 06:00-10:00 UTC peak window;
// pollAt is a Sunday, which is off-peak all day.
var weekdayPeak = time.Date(2026, 11, 18, 7, 0, 0, 0, time.UTC)

// cachedRecord is a model's settled record of a size at a 95% cache share.
func cachedRecord(model, size string, settled, passed int) ModelRecord {
	return ModelRecord{Model: model, Size: size, Settled: settled, Passed: passed, FreshInput: 50, CacheRead: 950}
}

func TestRankEligiblePricesTheReferenceMixAtTheHoursRates(t *testing.T) {
	records := []ModelRecord{cachedRecord(flash, "M", 5, 5), cachedRecord(haiku, "M", 5, 5)}
	for name, tt := range map[string]struct {
		at   time.Time
		want []string
	}{
		"off-peak, Flash's $0.02235 under Haiku's $0.02450": {at: pollAt, want: []string{flash, haiku}},
		"weekday peak, Flash doubled to $0.04470":           {at: weekdayPeak, want: []string{haiku, flash}},
	} {
		t.Run(name, func(t *testing.T) {
			got := RankEligible(records, "M", []string{flash, haiku}, providers.RatesFor, tt.at)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("ranked %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRankEligibleSplitsTheInputByTheModelsOwnCacheShare(t *testing.T) {
	// Uncached, Flash's $0.15 input loses to Haiku's $0.10, which a 95% share
	// reverses (the test above).
	records := []ModelRecord{
		{Model: flash, Size: "M", Settled: 5, Passed: 5, FreshInput: 1000},
		cachedRecord(haiku, "M", 5, 5),
	}
	if got := RankEligible(records, "M", []string{flash, haiku}, providers.RatesFor, pollAt); !slices.Equal(got, []string{haiku, flash}) {
		t.Fatalf("ranked %v, want Haiku first once Flash reads nothing from cache", got)
	}
}

func TestRankEligibleWorkedExampleCosts(t *testing.T) {
	for _, tt := range []struct {
		model string
		at    time.Time
		want  float64
	}{
		{flash, pollAt, 0.02235},
		{haiku, pollAt, 0.02450},
		{flash, weekdayPeak, 0.04470},
	} {
		card, _ := providers.RatesFor(tt.model)
		r := card.At(tt.at)
		got := (referenceInputTokens*(0.95*r.CacheRead+0.05*r.Input) + referenceOutputTokens*r.Output) / 1_000_000
		if math.Abs(got-tt.want) > 1e-9 {
			t.Errorf("%s at %s costs %v, want the doc's %v", tt.model, tt.at, got, tt.want)
		}
	}
}

func TestRankEligibleAdmitsOnlyAModelThatClearsTheBarForTheSize(t *testing.T) {
	for name, tt := range map[string]struct {
		records    []ModelRecord
		candidates []string
		want       []string
	}{
		"five of five":                 {records: []ModelRecord{cachedRecord(flash, "M", 5, 5)}, want: []string{flash}},
		"four of five is exactly 80%":  {records: []ModelRecord{cachedRecord(flash, "M", 5, 4)}, want: []string{flash}},
		"three of five":                {records: []ModelRecord{cachedRecord(flash, "M", 5, 3)}},
		"eight of ten passed is 80%":   {records: []ModelRecord{cachedRecord(flash, "M", 10, 8)}, want: []string{flash}},
		"seven of ten":                 {records: []ModelRecord{cachedRecord(flash, "M", 10, 7)}},
		"four runs, every one passing": {records: []ModelRecord{cachedRecord(flash, "M", 4, 4)}},
		"passed only at another size":  {records: []ModelRecord{cachedRecord(flash, "S", 9, 9)}},
		"not a candidate":              {records: []ModelRecord{cachedRecord(flash, "M", 5, 5)}, candidates: []string{haiku}},
		"no rate card":                 {records: []ModelRecord{cachedRecord("opencode-go/unpriced", "M", 5, 5)}, candidates: []string{"opencode-go/unpriced"}},
	} {
		t.Run(name, func(t *testing.T) {
			candidates := tt.candidates
			if candidates == nil {
				candidates = []string{flash}
			}
			if got := RankEligible(tt.records, "M", candidates, providers.RatesFor, pollAt); !slices.Equal(got, tt.want) {
				t.Fatalf("ranked %v, want %v", got, tt.want)
			}
		})
	}
}

// fakeModelRecords hands over a poll's settled records and counts the reads.
type fakeModelRecords struct {
	records []ModelRecord
	err     error
	reads   []time.Time
}

func (f *fakeModelRecords) ModelRecords(_ context.Context, since time.Time) ([]ModelRecord, error) {
	f.reads = append(f.reads, since)
	return f.records, f.err
}

// choiceConfig reaches both plans, defaults to Flash and offers every priced
// model, the free Gemini one among them.
func choiceConfig() Config {
	c := buildConfig
	c.Model = flash
	c.Plans = []string{"gemini", "opencode-go"}
	c.BuildCandidates = []string{flash, haiku, muse, gemini}
	c.LastResortPlans = [][]string{{gemini}}
	return c
}

// choiceDeps is a poll whose plans both admit their models and whose records are records.
func choiceDeps(q *fakeQueue, records *fakeModelRecords) Deps {
	d := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	d.ModelPlans = &fakeModelPlans{plans: map[string]providers.Plan{
		"opencode-go": planBilled(providers.BillingAllowance, false),
		"gemini":      planBilled(providers.BillingFree, false),
	}}
	d.ModelRecords = records
	return d
}

// chosen is the model each claim recorded, by run.
func chosen(q *fakeQueue) map[string]string {
	out := map[string]string{}
	for _, call := range q.tryClaims {
		out[call.runID] = call.res.ChosenModel
	}
	return out
}

func TestPollGivesAnUnlabelledRunTheCheapestModelThatPassedForItsSize(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{
		{RunID: "small", Repo: "octo/scratch", Size: "S", Priority: 1},
		{RunID: "medium", Repo: "octo/scratch", Size: "M", Priority: 2},
		{RunID: "large", Repo: "octo/scratch", Size: "L", Priority: 3},
	}}
	records := &fakeModelRecords{records: []ModelRecord{
		cachedRecord(flash, "S", 5, 5), cachedRecord(haiku, "S", 5, 5),
		cachedRecord(haiku, "M", 5, 4), cachedRecord(flash, "M", 5, 3),
		cachedRecord(flash, "L", 4, 4),
	}}
	if _, err := Poll(context.Background(), choiceDeps(q, records), choiceConfig()); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"small": flash, "medium": haiku, "large": ""}
	if got := chosen(q); !maps.Equal(got, want) {
		t.Fatalf("chosen = %v, want %v", got, want)
	}
	for _, call := range q.tryClaims {
		if wantModel := want[call.runID]; wantModel != "" && call.facts.Model != wantModel {
			t.Errorf("%s is judged on %s, want the chosen %s", call.runID, call.facts.Model, wantModel)
		}
	}
	if len(records.reads) != 1 || !records.reads[0].Equal(pollAt.Add(-30*24*time.Hour)) {
		t.Fatalf("records read at %v, want once over the trailing 30 days", records.reads)
	}
}

func TestPollChoosesAtTheClaimHoursRates(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "medium", Repo: "octo/scratch", Size: "M", Priority: 1}}}
	d := choiceDeps(q, &fakeModelRecords{records: []ModelRecord{cachedRecord(flash, "M", 5, 5), cachedRecord(haiku, "M", 5, 5)}})
	d.Now = func() time.Time { return weekdayPeak }
	if _, err := Poll(context.Background(), d, choiceConfig()); err != nil {
		t.Fatal(err)
	}
	if got := chosen(q)["medium"]; got != haiku {
		t.Fatalf("chosen %q, want Haiku while Flash's peak doubles its card", got)
	}
}

func TestPollRecordsNoModelWhereTheChoiceDoesNotApply(t *testing.T) {
	passing := []ModelRecord{cachedRecord(flash, "M", 5, 5), cachedRecord(muse, "M", 5, 5), cachedRecord(gemini, "M", 5, 5)}
	for name, tt := range map[string]struct {
		cand    Candidate
		records *fakeModelRecords
		unwired bool
	}{
		"the ticket's own label wins": {cand: Candidate{Model: haiku}, records: &fakeModelRecords{records: passing}},
		"a planned run's build":       {cand: Candidate{Stage: StageBuild}, records: &fakeModelRecords{records: passing}},
		"no model passed the bar":     {records: &fakeModelRecords{records: []ModelRecord{cachedRecord(flash, "M", 5, 3)}}},
		"the records cannot be read":  {records: &fakeModelRecords{records: passing, err: errFirestore}},
		"no records wired":            {unwired: true},
	} {
		t.Run(name, func(t *testing.T) {
			cand := tt.cand
			cand.RunID, cand.Repo, cand.Size, cand.Priority = "medium", "octo/scratch", "M", 1
			q := &fakeQueue{candidates: []Candidate{cand}}
			d := choiceDeps(q, tt.records)
			if tt.unwired {
				d.ModelRecords = nil
			}
			cfg := choiceConfig()
			cfg.TwoStage = true
			if _, err := Poll(context.Background(), d, cfg); err != nil {
				t.Fatal(err)
			}
			if len(q.tryClaims) != 1 || q.tryClaims[0].res.ChosenModel != "" {
				t.Fatalf("claims = %+v, want one recording no model", q.tryClaims)
			}
		})
	}
}

func TestPollSkipsAModelAdmissionRefusesOrThatIsFree(t *testing.T) {
	// Gemini is free, and Muse is the default review model, so a build on it is
	// refused; both are cheaper than Flash, which is chosen.
	q := &fakeQueue{candidates: []Candidate{{RunID: "medium", Repo: "octo/scratch", Size: "M", Priority: 1}}}
	records := &fakeModelRecords{records: []ModelRecord{cachedRecord(gemini, "M", 5, 5), cachedRecord(muse, "M", 5, 5), cachedRecord(flash, "M", 5, 5)}}
	if _, err := Poll(context.Background(), choiceDeps(q, records), choiceConfig()); err != nil {
		t.Fatal(err)
	}
	if got := chosen(q)["medium"]; got != flash {
		t.Fatalf("chosen %q, want Flash past the free and the refused models", got)
	}
}

func TestPollChoosesNoModelWhosePlanCannotBeRead(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "medium", Repo: "octo/scratch", Size: "M", Priority: 1}}}
	d := choiceDeps(q, &fakeModelRecords{records: []ModelRecord{cachedRecord(flash, "M", 5, 5)}})
	d.ModelPlans = &fakeModelPlans{err: errFirestore}
	if _, err := Poll(context.Background(), d, choiceConfig()); err != nil {
		t.Fatal(err)
	}
	if got := chosen(q)["medium"]; got != "" {
		t.Fatalf("chosen %q, want none while admission cannot be asked", got)
	}
}

func TestPollChoosesPastTheDefaultFromTheListTheDispatcherBuilds(t *testing.T) {
	// The real candidate list, default model, plans and review default the
	// dispatcher runs with: a list holding only the default would choose none.
	c := buildConfig
	c.Model = runner.DefaultModel()
	c.Plans = providers.Plans()
	c.BuildCandidates = providers.BuildCandidates()
	var records []ModelRecord
	for _, model := range c.BuildCandidates {
		passed := 5
		if model == c.Model {
			passed = 0
		}
		records = append(records, cachedRecord(model, "M", 5, passed))
	}
	plans := map[string]providers.Plan{}
	for _, plan := range c.Plans {
		plans[plan] = planBilled(providers.BillingAllowance, false)
	}
	q := &fakeQueue{candidates: []Candidate{{RunID: "medium", Repo: "octo/scratch", Size: "M", Priority: 1}}}
	d := choiceDeps(q, &fakeModelRecords{records: records})
	d.ModelPlans = &fakeModelPlans{plans: plans}
	if _, err := Poll(context.Background(), d, c); err != nil {
		t.Fatal(err)
	}
	if got := chosen(q)["medium"]; got == "" || got == c.Model {
		t.Fatalf("chosen %q from %v, want a model other than the default %s", got, c.BuildCandidates, c.Model)
	}
}

func TestPollChoosesNothingWithNoCandidates(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "medium", Repo: "octo/scratch", Size: "M", Priority: 1}}}
	records := &fakeModelRecords{records: []ModelRecord{cachedRecord(flash, "M", 5, 5)}}
	c := choiceConfig()
	c.BuildCandidates = nil
	if _, err := Poll(context.Background(), choiceDeps(q, records), c); err != nil {
		t.Fatal(err)
	}
	if len(records.reads) != 0 || chosen(q)["medium"] != "" {
		t.Fatalf("reads = %v, chosen = %v, want no read and no choice", records.reads, chosen(q))
	}
}

// chosenRetryPoll polls an unlabelled ticket against a store holding FRG-18 as
// an owner's retry whose earlier claim chose model.
func chosenRetryPoll(t *testing.T, model string, planErr error) (*fakeQueue, Result) {
	t.Helper()
	q := &fakeQueue{holds: map[string]bool{"FRG-18": true}, retried: map[string]bool{"FRG-18": true},
		chosenModels: map[string]string{"FRG-18": model},
		candidates:   []Candidate{{RunID: "FRG-18", Repo: "octo/scratch", Size: "S", Priority: 2, Attempt: 1}}}
	deps := pollDeps(fakeSource{issues: []Issue{admitted("size:S", "repo:octo/scratch")}}, q, &fakeWorkflow{})
	deps.ModelPlans = &fakeModelPlans{plans: modelPlans, err: planErr}
	res, err := Poll(context.Background(), deps, planConfig())
	if err != nil {
		t.Fatal(err)
	}
	return q, res
}

func TestPollChecksTheModelAnEarlierClaimChoseForARetry(t *testing.T) {
	q, _ := chosenRetryPoll(t, "allowance/a", nil)
	if len(q.clearedChosen) != 0 || q.retriedModels["FRG-18"].Build != "allowance/a" {
		t.Fatalf("cleared = %v, models = %+v, want the admitted chosen model kept", q.clearedChosen, q.retriedModels)
	}

	q, _ = chosenRetryPoll(t, "tokens/a", nil)
	if !slices.Equal(q.clearedChosen, []string{"FRG-18"}) {
		t.Fatalf("cleared = %v, want the refused chosen model dropped", q.clearedChosen)
	}
	if len(q.stoppedRetried) != 0 || len(q.rejected) != 0 {
		t.Fatalf("stopped = %v, rejected = %v, want the retry left to choose again", q.stoppedRetried, q.rejected)
	}

	q, res := chosenRetryPoll(t, "allowance/a", errFirestore)
	if len(q.clearedChosen) != 0 || len(q.tryClaims) != 0 ||
		len(res.Deferrals) != 1 || res.Deferrals[0].Ceiling != ConditionModelsUnchecked {
		t.Fatalf("cleared = %v, claims = %d, deferrals = %+v, want the retry held unchecked",
			q.clearedChosen, len(q.tryClaims), res.Deferrals)
	}
}

func TestPollReadsARetrysPlansOnceWhenNoChosenModelNeedsChecking(t *testing.T) {
	for name, tt := range map[string]struct {
		labels []string
		chosen string
		reads  []string
	}{
		"the ticket names the build model":          {labels: []string{"model:allowance/a"}, chosen: "free/x", reads: []string{"allowance"}},
		"the ticket names a review model, no build": {labels: []string{"review-model:allowance/r"}, reads: []string{"allowance"}},
	} {
		t.Run(name, func(t *testing.T) {
			q := &fakeQueue{holds: map[string]bool{"FRG-18": true}, retried: map[string]bool{"FRG-18": true},
				chosenModels: map[string]string{"FRG-18": tt.chosen}}
			plans := &fakeModelPlans{plans: modelPlans}
			deps := pollDeps(fakeSource{issues: []Issue{admitted(append([]string{"size:S", "repo:octo/scratch"}, tt.labels...)...)}}, q, &fakeWorkflow{})
			deps.ModelPlans = plans
			if _, err := Poll(context.Background(), deps, planConfig()); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(plans.reads, tt.reads) {
				t.Fatalf("plans read %v, want %v: nothing chosen needs a second check", plans.reads, tt.reads)
			}
		})
	}
}

func TestPollRecordsWhyEachRunBuildsOnItsModel(t *testing.T) {
	passing := []ModelRecord{cachedRecord(flash, "M", 5, 5), cachedRecord(haiku, "M", 5, 5)}
	for name, tt := range map[string]struct {
		cand        Candidate
		records     *fakeModelRecords
		unwired     bool
		noCands     bool
		wantReason  string
		wantVersion string
	}{
		"chosen, with the size, the hour and the rates": {records: &fakeModelRecords{records: passing},
			wantReason: "cheapest eligible for size M at 07:00 UTC", wantVersion: providers.RatesVersion()},
		"no model passed for an M":     {records: &fakeModelRecords{records: []ModelRecord{cachedRecord(flash, "M", 5, 3)}}, wantReason: "no eligible model for size M"},
		"no model passed for an L":     {cand: Candidate{Size: "L"}, records: &fakeModelRecords{records: passing}, wantReason: "no eligible model for size L"},
		"the records cannot be read":   {records: &fakeModelRecords{records: passing, err: errFirestore}, wantReason: "model records unreadable for size M"},
		"the ticket's own label":       {cand: Candidate{Model: haiku}, records: &fakeModelRecords{records: passing}},
		"a planned run's build":        {cand: Candidate{Stage: StageBuild}, records: &fakeModelRecords{records: passing}},
		"no records wired":             {unwired: true, wantReason: "no eligible model for size M"},
		"no candidates to choose from": {noCands: true, records: &fakeModelRecords{records: passing}, wantReason: "no eligible model for size M"},
	} {
		t.Run(name, func(t *testing.T) {
			cand := tt.cand
			cand.RunID, cand.Repo, cand.Priority = "run", "octo/scratch", 1
			if cand.Size == "" {
				cand.Size = "M"
			}
			q := &fakeQueue{candidates: []Candidate{cand}}
			d := choiceDeps(q, tt.records)
			d.Now = func() time.Time { return weekdayPeak.Add(42 * time.Minute) }
			if tt.unwired {
				d.ModelRecords = nil
			}
			cfg := choiceConfig()
			cfg.TwoStage = true
			if tt.noCands {
				cfg.BuildCandidates = nil
			}
			if _, err := Poll(context.Background(), d, cfg); err != nil {
				t.Fatal(err)
			}
			if len(q.tryClaims) != 1 {
				t.Fatalf("claims = %+v, want one", q.tryClaims)
			}
			if res := q.tryClaims[0].res; res.ModelReason != tt.wantReason || res.RatesVersion != tt.wantVersion {
				t.Fatalf("reason = %q, rates = %q, want %q, %q", res.ModelReason, res.RatesVersion, tt.wantReason, tt.wantVersion)
			}
		})
	}
}

// TestPollRecordsAnUnreadablePlanWhereAModelClearedTheBar asserts a claim whose
// default model passed for the size but whose plan read fails records that the
// plan could not be read, not that no model was eligible: a failed read is
// neither a refusal nor an admission.
func TestPollRecordsAnUnreadablePlanWhereAModelClearedTheBar(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "medium", Repo: "octo/scratch", Size: "M", Priority: 1}}}
	d := choiceDeps(q, &fakeModelRecords{records: []ModelRecord{cachedRecord(flash, "M", 5, 5)}})
	d.ModelPlans = &fakeModelPlans{err: errFirestore}
	if _, err := Poll(context.Background(), d, choiceConfig()); err != nil {
		t.Fatal(err)
	}
	if len(q.tryClaims) != 1 || q.tryClaims[0].res.ModelReason != "model plans unreadable for size M" {
		t.Fatalf("claims = %+v, want one recording the unreadable plan", q.tryClaims)
	}
}

// TestPollRecordsNoEligibleModelWhenEveryPlanReadSucceeds asserts a claim whose
// every passed candidate was refused records no model was eligible: the review
// model and a free one are never chosen, and a plan read that succeeds and finds
// the provider unconfigured is a refusal, not an unreadable plan.
func TestPollRecordsNoEligibleModelWhenEveryPlanReadSucceeds(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "medium", Repo: "octo/scratch", Size: "M", Priority: 1}}}
	records := []ModelRecord{cachedRecord(haiku, "M", 5, 5), cachedRecord(muse, "M", 5, 5), cachedRecord(gemini, "M", 5, 5)}
	plans := &fakeModelPlans{plans: map[string]providers.Plan{"gemini": planBilled(providers.BillingFree, false)}}
	d := choiceDeps(q, &fakeModelRecords{records: records})
	d.ModelPlans = plans
	if _, err := Poll(context.Background(), d, choiceConfig()); err != nil {
		t.Fatal(err)
	}
	if len(q.tryClaims) != 1 || q.tryClaims[0].res.ModelReason != "no eligible model for size M" {
		t.Fatalf("claims = %+v, want one recording no eligible model", q.tryClaims)
	}
	if !slices.Contains(plans.reads, "opencode-go") {
		t.Fatalf("plans read %v, want the refusal drawn from a plan it could read", plans.reads)
	}
}

func TestPollGivesEveryRunAfterAnUnreadRecordTheUnreadReason(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{
		{RunID: "first", Repo: "octo/scratch", Size: "M", Priority: 1},
		{RunID: "second", Repo: "octo/scratch", Size: "S", Priority: 2},
	}}
	records := &fakeModelRecords{records: []ModelRecord{cachedRecord(flash, "S", 5, 5)}, err: errFirestore}
	if _, err := Poll(context.Background(), choiceDeps(q, records), choiceConfig()); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, call := range q.tryClaims {
		got[call.runID] = call.res.ModelReason
	}
	want := map[string]string{"first": "model records unreadable for size M", "second": "model records unreadable for size S"}
	if !maps.Equal(got, want) || len(records.reads) != 1 {
		t.Fatalf("reasons = %v after %d reads, want %v from one read", got, len(records.reads), want)
	}
}

// TestPollLogsAClaimsFailedPlanReadAtWarn asserts the claim's failed plan read
// logs modelConfigLookupFailed at warn and not also at info: admission's own
// read of a ticket's named models is a separate line.
func TestPollLogsAClaimsFailedPlanReadAtWarn(t *testing.T) {
	var logs bytes.Buffer
	q := &fakeQueue{candidates: []Candidate{{RunID: "medium", Repo: "octo/scratch", Size: "M", Priority: 1}}}
	d := choiceDeps(q, &fakeModelRecords{records: []ModelRecord{cachedRecord(flash, "M", 5, 5)}})
	d.ModelPlans = &fakeModelPlans{err: errFirestore}
	d.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	if _, err := Poll(context.Background(), d, choiceConfig()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "level=WARN msg=modelConfigLookupFailed") {
		t.Fatalf("logs = %s, want the failed plan read at WARN", logs.String())
	}
	if strings.Contains(logs.String(), "level=INFO msg=modelConfigLookupFailed") {
		t.Fatalf("logs = %s, want no INFO line for the same failure", logs.String())
	}
}

func TestEveryModelReasonFitsTheBoundTheStoreReadsBackWithin(t *testing.T) {
	reasons := []string{ReasonNamedByTicket}
	for _, size := range []string{"S", "M", "L"} {
		reasons = append(reasons, noEligibleReason(size), recordsUnreadableReason(size), plansUnreadableReason(size))
		for h := range 24 {
			reasons = append(reasons, ChosenReason(size, time.Date(2026, 11, 18, h, 59, 0, 0, time.UTC)))
		}
	}
	for _, r := range reasons {
		if len(r) > MaxModelReasonBytes {
			t.Errorf("%q is %d bytes, over %d", r, len(r), MaxModelReasonBytes)
		}
	}
	if got := ChosenReason("M", time.Date(2026, 11, 18, 18, 5, 0, 0, time.FixedZone("AEDT", 11*3600))); got != "cheapest eligible for size M at 07:00 UTC" {
		t.Errorf("a claim at 18:05 AEDT reads %q, want its UTC hour", got)
	}
}

func TestRankSizesListsEachSizesClearingModelsInOrderAndTheRestBelowTheBar(t *testing.T) {
	records := []ModelRecord{
		cachedRecord(flash, "S", 5, 5), cachedRecord(haiku, "S", 6, 5),
		cachedRecord(flash, "M", 5, 3), cachedRecord(haiku, "M", 5, 4),
		cachedRecord(muse, "L", 4, 4),
	}
	got := RankSizes(records, []string{flash, haiku, muse}, providers.RatesFor)
	want := []providers.SizeRanking{
		{Size: "S",
			Ranked:   []providers.RankedModel{{Model: flash, Settled: 5, Passed: 5}, {Model: haiku, Settled: 6, Passed: 5}},
			BelowBar: []providers.RankedModel{{Model: muse}}},
		{Size: "M",
			Ranked:   []providers.RankedModel{{Model: haiku, Settled: 5, Passed: 4}},
			BelowBar: []providers.RankedModel{{Model: flash, Settled: 5, Passed: 3}, {Model: muse}}},
		{Size: "L",
			BelowBar: []providers.RankedModel{{Model: flash}, {Model: haiku}, {Model: muse, Settled: 4, Passed: 4}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranking = %+v, want %+v", got, want)
	}
}

func TestRankSizesPricesAtTheOffPeakReferenceWhateverTheHour(t *testing.T) {
	records := []ModelRecord{cachedRecord(flash, "M", 5, 5), cachedRecord(haiku, "M", 5, 5)}
	if at := RankEligible(records, "M", []string{flash, haiku}, providers.RatesFor, weekdayPeak); at[0] != haiku {
		t.Fatalf("a weekday peak hour ranks %v, want Haiku first so the reference hour is what is under test", at)
	}
	got := RankSizes(records, []string{flash, haiku}, providers.RatesFor)
	if first := got[1].Ranked[0].Model; first != flash {
		t.Fatalf("ranked %s first for M, want Flash at its off-peak card", first)
	}
}

func TestRankSizesLeavesAnUnpricedModelOutOfBothLists(t *testing.T) {
	const unpriced = "opencode-go/unpriced"
	got := RankSizes([]ModelRecord{cachedRecord(unpriced, "M", 5, 5)}, []string{unpriced}, providers.RatesFor)
	if len(got[1].Ranked) != 0 || len(got[1].BelowBar) != 0 {
		t.Fatalf("M = %+v, want a model that clears the bar with no rate card in neither list", got[1])
	}
}

// TestRefreshChangesNoModelAClaimChooses asserts a weekly refresh that records a
// ranking leaves the rates, the build candidates and the model a claim chooses
// as they were: the claim after the refresh chooses what the claim before it did.
func TestRefreshChangesNoModelAClaimChooses(t *testing.T) {
	records := []ModelRecord{
		cachedRecord(flash, "S", 5, 5), cachedRecord(haiku, "S", 5, 5),
		cachedRecord(haiku, "M", 5, 4), cachedRecord(flash, "M", 5, 3),
	}
	claim := func() map[string]string {
		q := &fakeQueue{candidates: []Candidate{
			{RunID: "small", Repo: "octo/scratch", Size: "S", Priority: 1},
			{RunID: "medium", Repo: "octo/scratch", Size: "M", Priority: 2},
		}}
		if _, err := Poll(context.Background(), choiceDeps(q, &fakeModelRecords{records: records}), choiceConfig()); err != nil {
			t.Fatal(err)
		}
		return chosen(q)
	}
	before, candidates, version := claim(), providers.BuildCandidates(), providers.RatesVersion()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"deepseek-v4.1-flash"},{"id":"muse-spark-1.3-contributor"},{"id":"claude-haiku-5-5"}]}`)
	}))
	t.Cleanup(srv.Close)
	refresher := planrefresh.Refresher{
		Vendor: planrefresh.Vendor{HTTP: srv.Client(), Base: func(string) string { return srv.URL }},
		Now:    func() time.Time { return pollAt },
		Rank: func(context.Context, string, time.Time) ([]providers.SizeRanking, error) {
			return RankSizes(records, []string{flash, haiku, muse}, providers.RatesFor), nil
		},
	}
	facts, err := refresher.Refresh(context.Background(), "opencode-go", providers.Definition{}, providers.Facts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.Ranking.Sizes) != 3 || facts.Ranking.Source.State != providers.ReadChanged {
		t.Fatalf("ranking = %+v, want the refresh to have recorded one", facts.Ranking)
	}

	if after := claim(); !maps.Equal(before, after) || before["small"] != flash || before["medium"] != haiku {
		t.Fatalf("chosen %v before the refresh and %v after, want the same, Flash for S and Haiku for M", before, after)
	}
	if !slices.Equal(providers.BuildCandidates(), candidates) || providers.RatesVersion() != version {
		t.Fatal("the refresh changed the embedded build candidates or rates")
	}
}
