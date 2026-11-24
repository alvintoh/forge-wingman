package providers

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/money"
)

// planShape is the plan-stage request the Plans screen's first preset names.
var planShape = Request{Model: "opencode-go/deepseek-v4.1-flash", Fresh: 52000, Cached: 2200, Output: 1500}

func comparisonByPlan(t *testing.T, got []PlanComparison, plan string) PlanComparison {
	t.Helper()
	for _, c := range got {
		if c.Plan == plan {
			return c
		}
	}
	t.Fatalf("no comparison for plan %q in %d plans", plan, len(got))
	return PlanComparison{}
}

// comparisonConfig parses a one-provider document: p's own windows and rates, and any extra provider body.
func comparisonConfig(t *testing.T, body string) Config {
	t.Helper()
	cfg, err := parseConfig([]byte(configWith(providerFacts + "," + body)))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

const (
	perMtok10   = `{"input_usd_per_mtok":0,"output_usd_per_mtok":10,"cache_read_usd_per_mtok":0,"cache_write_usd_per_mtok":0}`
	windows20h5 = `"windows":[{"name":"p-5h","hours":5,"limit_usd":20}]`
)

func TestComparePlansReadsTheEmbeddedConfiguration(t *testing.T) {
	got := ComparePlans(ComparisonInput{
		Plans: map[string]Plan{"opencode-go": {
			Definition: Definition{MonthlyPrice: 10 * money.Dollar},
			Verdict:    VerdictRestricted,
		}},
		Request: planShape,
	})
	if len(got) != 2 || got[0].Plan != "opencode-go" || got[1].Plan != "gemini" {
		t.Fatalf("plans = %v, want the paid plan then the free tier, each once", got)
	}
	g := got[0]
	if g.MonthlyPrice != Known(10*money.Dollar) {
		t.Errorf("price = %+v, want $10", g.MonthlyPrice)
	}
	if g.Verdict != Known(VerdictRestricted) {
		t.Errorf("verdict = %+v, want restricted", g.Verdict)
	}
	for name, tt := range map[string]struct {
		got   Field[Ceiling]
		limit money.Micros
		want  RequestCounts
	}{
		"5-hour": {g.FiveHour, 20 * money.Dollar, RequestCounts{OffPeak: 2297, HasPeak: true, Peak: 1148}},
		"weekly": {g.Weekly, 50 * money.Dollar, RequestCounts{OffPeak: 5742, HasPeak: true, Peak: 2871}},
		"month":  {g.Monthly, 60 * money.Dollar, RequestCounts{OffPeak: 6891, HasPeak: true, Peak: 3445}},
	} {
		if !tt.got.Known || tt.got.Value.Limit != tt.limit || tt.got.Value.Requests == nil || *tt.got.Value.Requests != tt.want {
			t.Errorf("%s ceiling = %+v, want limit %d and requests %+v", name, tt.got, tt.limit, tt.want)
		}
	}
	if g.Strongest.Known || g.StrongestCap.Known {
		t.Errorf("strongest = %+v, cap = %+v, want both unconfirmed with no facts", g.Strongest, g.StrongestCap)
	}
	wantFeatures := []string{"Long-context pricing over 100000 tokens on claude-haiku-5-5", "Peak-hour pricing on deepseek-v4.1-flash"}
	if !slices.Equal(g.Features, wantFeatures) {
		t.Errorf("features = %v, want %v", g.Features, wantFeatures)
	}
	if g.FeedPerks.Known || g.UnshownWindows != nil {
		t.Errorf("perks = %+v, unshown windows = %v, want unconfirmed perks and no unshown window", g.FeedPerks, g.UnshownWindows)
	}
	if len(g.Harnesses) != 1 || g.Harnesses[0] != (HarnessComparison{Harness: "omp"}) {
		t.Errorf("harnesses = %+v, want omp with every fact unconfirmed", g.Harnesses)
	}
	if len(g.Models) != 3 {
		t.Fatalf("models = %d, want the three rate cards", len(g.Models))
	}
}

func TestComparePlansLeavesWhatNothingRecordedUnconfirmed(t *testing.T) {
	got := comparisonByPlan(t, ComparePlans(ComparisonInput{Request: planShape}), "gemini")
	if got.MonthlyPrice.Known || got.Verdict.Known {
		t.Errorf("price = %+v, verdict = %+v, want both unconfirmed with no plan record", got.MonthlyPrice, got.Verdict)
	}
	for name, f := range map[string]Field[Ceiling]{"5-hour": got.FiveHour, "weekly": got.Weekly, "monthly": got.Monthly} {
		if f.Known {
			t.Errorf("%s ceiling = %+v, want unconfirmed: providers.json holds no window for gemini", name, f)
		}
	}
	want := []string{"Free tier at $0"}
	if !slices.Equal(got.Features, want) {
		t.Errorf("features = %v, want %v", got.Features, want)
	}
}

func TestComparePlansReadsAVerdictOnlyOnceTheOwnerConfirmedIt(t *testing.T) {
	for name, tt := range map[string]struct {
		verdict Verdict
		want    Field[Verdict]
	}{
		"none recorded": {"", Field[Verdict]{}},
		"unconfirmed":   {VerdictUnconfirmed, Field[Verdict]{}},
		"allowed":       {VerdictAllowed, Known(VerdictAllowed)},
	} {
		got := comparisonByPlan(t, ComparePlans(ComparisonInput{
			Plans: map[string]Plan{"gemini": {Verdict: tt.verdict}},
		}), "gemini")
		if got.Verdict != tt.want {
			t.Errorf("%s: verdict = %+v, want %+v", name, got.Verdict, tt.want)
		}
		if !got.MonthlyPrice.Known || got.MonthlyPrice.Value != 0 {
			t.Errorf("%s: price = %+v, want a recorded $0", name, got.MonthlyPrice)
		}
	}
}

func TestComparePlansPricesAModelByTheHour(t *testing.T) {
	got := comparisonByPlan(t, ComparePlans(ComparisonInput{}), "opencode-go")
	var flash ModelPrice
	for _, m := range got.Models {
		if m.Model == "opencode-go/deepseek-v4.1-flash" {
			flash = m
		}
	}
	want := ModelPrice{
		Model: "opencode-go/deepseek-v4.1-flash", Input: 0.15, Output: 0.6, CacheRead: 0.003,
		Peak: &PeakPrice{Multiplier: 2, Hours: []HourRange{{From: 1, To: 4}, {From: 6, To: 10}}, Input: 0.3, Output: 1.2, CacheRead: 0.006},
	}
	if !reflect.DeepEqual(flash, want) {
		t.Errorf("deepseek price = %+v (peak %+v), want %+v (peak %+v)", flash, flash.Peak, want, want.Peak)
	}
}

func TestComparePlansPricesAModelOverALongContext(t *testing.T) {
	got := comparisonByPlan(t, ComparePlans(ComparisonInput{}), "opencode-go")
	var haiku ModelPrice
	for _, m := range got.Models {
		if m.Model == "opencode-go/claude-haiku-5-5" {
			haiku = m
		}
	}
	want := ModelPrice{
		Model: "opencode-go/claude-haiku-5-5", Input: 0.1, Output: 0.5, CacheRead: 0.01,
		Above: &ContextPrice{ContextTokens: 100000, Input: 0.5, Output: 2.5, CacheRead: 0.05},
	}
	if !reflect.DeepEqual(haiku, want) {
		t.Errorf("haiku price = %+v (above %+v), want %+v (above %+v)", haiku, haiku.Above, want, want.Above)
	}
}

func TestComparePlansConvertsACeilingAtTheRateOfTheRequestsContext(t *testing.T) {
	haiku := func(fresh, cached int64) *RequestCounts {
		got := comparisonByPlan(t, ComparePlans(ComparisonInput{
			Request: Request{Model: "opencode-go/claude-haiku-5-5", Fresh: fresh, Cached: cached, Output: 1000},
		}), "opencode-go")
		return got.FiveHour.Value.Requests
	}
	// At exactly the threshold the base rates apply: 100000 x 0.1 + 1000 x 0.5 = $0.0105 a request.
	if got := haiku(100000, 0); got == nil || *got != (RequestCounts{OffPeak: 1904}) {
		t.Errorf("at the threshold = %+v, want 1904 requests and no peak figure", got)
	}
	// One cached token past it, the long-context rates apply: 100000 x 0.5 + 1 x 0.05 + 1000 x 2.5 = $0.05250005.
	if got := haiku(100000, 1); got == nil || *got != (RequestCounts{OffPeak: 380}) {
		t.Errorf("past the threshold = %+v, want 380 requests", got)
	}
}

func TestComparePlansFloorsADollarCeilingToWholeRequests(t *testing.T) {
	// 1000 output tokens at $10 a million tokens cost exactly $0.01 a request.
	cfg := comparisonConfig(t, windows20h5+`,"rates":{"p/m":`+perMtok10+`}`)
	for name, tt := range map[string]struct {
		limit money.Micros
		want  int64
	}{
		"a limit that is an exact multiple":     {20 * money.Dollar, 2000},
		"a micro-dollar short of the multiple":  {20*money.Dollar - 1, 1999},
		"a micro-dollar past the multiple":      {20*money.Dollar + 1, 2000},
		"a limit below the cost of one request": {9_999, 0},
	} {
		cfg.Providers["p"] = withFiveHourLimit(cfg.Providers["p"], tt.limit)
		got := comparisonByPlan(t, comparePlans(cfg, ComparisonInput{Request: Request{Model: "p/m", Output: 1000}}), "p")
		if r := got.FiveHour.Value.Requests; r == nil || r.OffPeak != tt.want || r.HasPeak {
			t.Errorf("%s: requests = %+v, want %d off-peak and no peak figure", name, r, tt.want)
		}
	}
}

func TestComparePlansFloorsThePeakFigureAtThePeakRate(t *testing.T) {
	// $0.01 a request off-peak, $0.03 at peak: $20 buys 2000 and 666 (666.67 floored, never rounded to 667).
	cfg := comparisonConfig(t, windows20h5+`,"rates":{"p/m":{"output_usd_per_mtok":10,"peak":{"multiplier":3,"weekday_utc_hours":[{"from":1,"to":4}]}}}`)
	got := comparisonByPlan(t, comparePlans(cfg, ComparisonInput{Request: Request{Model: "p/m", Output: 1000}}), "p")
	if r := got.FiveHour.Value.Requests; r == nil || *r != (RequestCounts{OffPeak: 2000, HasPeak: true, Peak: 666}) {
		t.Errorf("requests = %+v, want 2000 off-peak and 666 at peak", r)
	}
}

func TestComparePlansAppliesThePeakMultiplierToTheLongContextRates(t *testing.T) {
	// Over 1000 tokens of context the card costs $20 a million output tokens, $0.02 a request, and $0.04 at peak.
	cfg := comparisonConfig(t, windows20h5+`,"rates":{"p/m":{"output_usd_per_mtok":10,`+
		`"peak":{"multiplier":2,"weekday_utc_hours":[{"from":1,"to":4}]},`+
		`"above":{"context_tokens":1000,"input_usd_per_mtok":0,"output_usd_per_mtok":20,"cache_read_usd_per_mtok":0,"cache_write_usd_per_mtok":0}}}`)
	got := comparisonByPlan(t, comparePlans(cfg, ComparisonInput{Request: Request{Model: "p/m", Fresh: 2000, Output: 1000}}), "p")
	if r := got.FiveHour.Value.Requests; r == nil || *r != (RequestCounts{OffPeak: 1000, HasPeak: true, Peak: 500}) {
		t.Errorf("requests = %+v, want 1000 off-peak and 500 at peak", r)
	}
}

func TestComparePlansKeepsACeilingItCannotConvert(t *testing.T) {
	cfg := comparisonConfig(t, windows20h5+`,"rates":{"p/m":`+perMtok10+`,"p/free":{}}`+
		`,"last_resort":{"models":["p/free"],"measured_on":"2026-10-08","smoke_runs":{"p/free":7}}`)
	for name, req := range map[string]Request{
		"no request given":              {},
		"a model the plan does not set": {Model: "p/other", Output: 1000},
		"a zero-priced model":           {Model: "p/free", Fresh: 1000, Output: 1000},
		"a request of no tokens":        {Model: "p/m"},
	} {
		got := comparisonByPlan(t, comparePlans(cfg, ComparisonInput{Request: req}), "p")
		if c := got.FiveHour; !c.Known || c.Value.Limit != 20*money.Dollar || c.Value.Name != "p-5h" || c.Value.Requests != nil {
			t.Errorf("%s: ceiling = %+v, want the published $20 with no request figure", name, c)
		}
	}
	free := comparisonByPlan(t, comparePlans(cfg, ComparisonInput{}), "p")
	if i := slices.IndexFunc(free.Models, func(m ModelPrice) bool { return m.Model == "p/free" }); i < 0 || free.Models[i].Input != 0 || free.Models[i].Output != 0 || free.Models[i].CacheRead != 0 {
		t.Errorf("models = %+v, want p/free listed at $0", free.Models)
	}
}

func TestComparePlansShowsOnlyTheThreeNamedPeriodsAndTheFirstOfEach(t *testing.T) {
	cfg := comparisonConfig(t, `"windows":[{"name":"first","hours":5,"limit_usd":3},{"name":"day","hours":24,"limit_usd":4},`+
		`{"name":"second","hours":5,"limit_usd":9},{"name":"week","hours":168,"limit_usd":5},{"name":"month","hours":720,"limit_usd":6}]`+
		`,"rates":{"p/m":`+perMtok10+`}`)
	got := comparisonByPlan(t, comparePlans(cfg, ComparisonInput{}), "p")
	names := []string{got.FiveHour.Value.Name, got.Weekly.Value.Name, got.Monthly.Value.Name}
	if !slices.Equal(names, []string{"first", "week", "month"}) {
		t.Errorf("ceilings = %v, want first, week and month: a 24-hour window is not shown", names)
	}
	if !slices.Equal(got.UnshownWindows, []string{"day", "second"}) {
		t.Errorf("unshown windows = %v, want the 24-hour window and the repeated 5-hour one named", got.UnshownWindows)
	}
}

func TestComparePlansNamesTheStrongestModelAndItsCap(t *testing.T) {
	cfg := comparisonConfig(t, windows20h5+`,"model_caps":{"p/big":90},"rates":{"p/m":`+perMtok10+`}`)
	listed := func(model string, value float64) ModelFacts {
		return ModelFacts{Model: model, Leaderboard: Score{Listed: true, Value: value, Benchmark: "coding"}}
	}
	facts := func(models ...ModelFacts) map[string]Facts {
		return map[string]Facts{"p": {Leaderboard: Source{State: ReadUnchanged}, Models: ModelsFacts{Models: models}}}
	}
	for name, tt := range map[string]struct {
		models      []ModelFacts
		wantModel   string
		wantCap     Field[money.Micros]
		wantKnownTo bool
	}{
		"the highest listed score wins, an unlisted one is ignored": {
			[]ModelFacts{listed("small", 50), {Model: "ghost", Leaderboard: Score{Value: 99}}, listed("big", 70)}, "big", Known(90 * money.Dollar), true},
		"a tie goes to the lexicographically first id": {
			[]ModelFacts{listed("zeta", 70), listed("big", 70)}, "big", Known(90 * money.Dollar), true},
		"a strongest model with no cap of its own is unconfirmed": {
			[]ModelFacts{listed("big", 70), listed("other", 80)}, "other", Field[money.Micros]{}, true},
		"no model listed on the leaderboard": {
			[]ModelFacts{{Model: "big"}}, "", Field[money.Micros]{}, false},
	} {
		got := comparisonByPlan(t, comparePlans(cfg, ComparisonInput{Facts: facts(tt.models...)}), "p")
		if got.Strongest.Known != tt.wantKnownTo || got.Strongest.Value.Model != tt.wantModel {
			t.Errorf("%s: strongest = %+v, want %q (known %t)", name, got.Strongest, tt.wantModel, tt.wantKnownTo)
		}
		if got.StrongestCap != tt.wantCap {
			t.Errorf("%s: cap = %+v, want %+v", name, got.StrongestCap, tt.wantCap)
		}
	}
	if got := comparisonByPlan(t, comparePlans(cfg, ComparisonInput{}), "p"); got.Strongest.Known || got.StrongestCap.Known {
		t.Errorf("with no facts: strongest = %+v, cap = %+v, want both unconfirmed", got.Strongest, got.StrongestCap)
	}
}

func TestComparePlansTrustsTheLeaderboardOnlyWhenItsLastReadSucceeded(t *testing.T) {
	cfg := comparisonConfig(t, windows20h5+`,"model_caps":{"p/big":90},"rates":{"p/m":`+perMtok10+`}`)
	scored := []ModelFacts{{Model: "big", Leaderboard: Score{Listed: true, Value: 70}}}
	for name, tt := range map[string]struct {
		state ReadState
		want  bool
	}{
		"read and changed":                         {ReadChanged, true},
		"read and unchanged":                       {ReadUnchanged, true},
		"could not read, scores carried forward":   {ReadCouldNotRead, false},
		"no read recorded, scores somehow present": {"", false},
	} {
		got := comparisonByPlan(t, comparePlans(cfg, ComparisonInput{Facts: map[string]Facts{
			"p": {Leaderboard: Source{State: tt.state}, Models: ModelsFacts{Models: scored}},
		}}), "p")
		if got.Strongest.Known != tt.want || got.StrongestCap.Known != tt.want {
			t.Errorf("%s: strongest known = %t, cap known = %t, want both %t", name, got.Strongest.Known, got.StrongestCap.Known, tt.want)
		}
	}
}

func TestComparePlansTrustsTheFeedsPerksOnlyWhenItsLastReadSucceeded(t *testing.T) {
	cfg := comparisonConfig(t, windows20h5+`,"rates":{"p/m":`+perMtok10+`}`)
	perks := []string{"Prompt caching"}
	for name, tt := range map[string]struct {
		state ReadState
		want  Field[[]string]
	}{
		"read":                      {ReadChanged, Known(perks)},
		"could not read, carried":   {ReadCouldNotRead, Field[[]string]{}},
		"no read recorded":          {"", Field[[]string]{}},
		"read and listing no perks": {ReadUnchanged, Known([]string{})},
	} {
		in := perks
		if name == "read and listing no perks" {
			in = nil
		}
		got := comparisonByPlan(t, comparePlans(cfg, ComparisonInput{Facts: map[string]Facts{
			"p": {Feed: FeedFacts{Source: Source{State: tt.state}, Perks: in}},
		}}), "p")
		if !reflect.DeepEqual(got.FeedPerks, tt.want) {
			t.Errorf("%s: perks = %+v, want %+v", name, got.FeedPerks, tt.want)
		}
	}
}

func TestComparePlansListsOnlyFeaturesTheConfigurationShows(t *testing.T) {
	cfg := comparisonConfig(t, windows20h5+`,"model_caps":{"p/m":90},"rates":{"p/m":`+perMtok10+`,"p/free":{}}`+
		`,"last_resort":{"models":["p/free"],"measured_on":"2026-10-08","smoke_runs":{"p/free":7}}`)
	got := comparisonByPlan(t, comparePlans(cfg, ComparisonInput{}), "p")
	if want := []string{"Per-model dollar limits", "Free tier at $0"}; !slices.Equal(got.Features, want) {
		t.Errorf("features = %v, want %v", got.Features, want)
	}
	// A rung whose model carries any price is not a free tier, whatever the rung says.
	for name, priced := range map[string]Rates{"input": {Input: 1}, "output": {Output: 1}, "cache read": {CacheRead: 1}, "cache write": {CacheWrite: 1}} {
		p := cfg.Providers["p"]
		p.Rates = maps.Clone(p.Rates)
		p.Rates["p/free"] = priced
		p.ModelCaps = nil
		withPriced := cfg
		withPriced.Providers = map[string]Provider{"p": p}
		if got := comparisonByPlan(t, comparePlans(withPriced, ComparisonInput{}), "p"); len(got.Features) != 0 {
			t.Errorf("%s priced: features = %v, want none: the rung is priced and no cap is set", name, got.Features)
		}
	}
}

func TestComparePlansWritesAnUnconfirmedFieldAsTheWordNeverAsAZeroValue(t *testing.T) {
	cfg := comparisonConfig(t, windows20h5+`,"rates":{"p/m":`+perMtok10+`}`)
	for name, tt := range map[string]struct {
		in   ComparisonInput
		path []string
		want any
	}{
		"an unknown number":    {ComparisonInput{}, []string{"MonthlyPrice"}, "unconfirmed"},
		"an unknown struct":    {ComparisonInput{}, []string{"Strongest"}, "unconfirmed"},
		"an unknown bool":      {ComparisonInput{}, []string{"Harnesses", "0", "DeniesPlanningEdits"}, "unconfirmed"},
		"an unknown ceiling":   {ComparisonInput{}, []string{"Monthly"}, "unconfirmed"},
		"unknown perks":        {ComparisonInput{}, []string{"FeedPerks"}, "unconfirmed"},
		"a recorded $0 price":  {ComparisonInput{Plans: map[string]Plan{"p": {}}}, []string{"MonthlyPrice"}, float64(0)},
		"a known bool":         {ComparisonInput{Harness: func(string, string) HarnessCapabilities { return HarnessCapabilities{Headless: Known(false)} }}, []string{"Harnesses", "0", "Headless"}, false},
		"a known ceiling name": {ComparisonInput{}, []string{"FiveHour", "Name"}, "p-5h"},
	} {
		b, err := json.Marshal(comparisonByPlan(t, comparePlans(cfg, tt.in), "p"))
		if err != nil {
			t.Fatal(err)
		}
		var doc any
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		got := doc
		for _, key := range tt.path {
			switch node := got.(type) {
			case map[string]any:
				got = node[key]
			case []any:
				i := 0
				if key != "0" {
					t.Fatalf("%s: unexpected index %s", name, key)
				}
				got = node[i]
			default:
				got = nil
			}
		}
		if got != tt.want {
			t.Errorf("%s: %v = %#v, want %#v in %s", name, tt.path, got, tt.want, b)
		}
	}
}

func TestComparePlansFillsEveryFieldOfAFullyRecordedPlan(t *testing.T) {
	cfg := comparisonConfig(t, `"windows":[{"name":"a","hours":5,"limit_usd":20},{"name":"b","hours":168,"limit_usd":50},{"name":"c","hours":720,"limit_usd":60}]`+
		`,"model_caps":{"p/big":90},"rates":{"p/big":`+perMtok10+`}`)
	got := comparisonByPlan(t, comparePlans(cfg, ComparisonInput{
		Plans: map[string]Plan{"p": {Definition: Definition{MonthlyPrice: 12 * money.Dollar}, Verdict: VerdictAllowed}},
		Facts: map[string]Facts{"p": {
			Leaderboard: Source{State: ReadChanged},
			Models:      ModelsFacts{Models: []ModelFacts{{Model: "big", Leaderboard: Score{Listed: true, Value: 70}}}},
			Feed:        FeedFacts{Source: Source{State: ReadUnchanged}, Perks: []string{"Prompt caching"}},
		}},
		Harness: func(harness, plan string) HarnessCapabilities {
			if harness != "h" || plan != "p" {
				return HarnessCapabilities{}
			}
			return HarnessCapabilities{Headless: Known(true), StructuredOutput: Known(false), DeniesPlanningEdits: Known(true)}
		},
		Request: Request{Model: "p/big", Output: 1000},
	}), "p")
	for name, known := range map[string]bool{
		"price": got.MonthlyPrice.Known, "verdict": got.Verdict.Known, "5-hour": got.FiveHour.Known, "weekly": got.Weekly.Known,
		"monthly": got.Monthly.Known, "strongest": got.Strongest.Known, "cap": got.StrongestCap.Known, "perks": got.FeedPerks.Known,
	} {
		if !known {
			t.Errorf("%s is unconfirmed on a fully recorded plan", name)
		}
	}
	wantPair := HarnessComparison{Harness: "h", Headless: Known(true), StructuredOutput: Known(false), DeniesPlanningEdits: Known(true)}
	if len(got.Harnesses) != 1 || got.Harnesses[0] != wantPair {
		t.Errorf("harnesses = %+v, want %+v", got.Harnesses, wantPair)
	}
	if !slices.Equal(got.Features, []string{"Per-model dollar limits"}) || !slices.Equal(got.FeedPerks.Value, []string{"Prompt caching"}) {
		t.Errorf("features = %v, perks = %v, want the configuration's and the feed's kept apart", got.Features, got.FeedPerks.Value)
	}
}

func TestComparePlansListsAPlansHarnessesInSelectionOrder(t *testing.T) {
	cfg, err := parseConfig([]byte(`{"cash_limit_usd":30,"runner_free_minutes":2000,"providers":{"p":{"base_url":"https://v.example/v1","key_secret":"k",` +
		`"harnesses":{"default":"first","fallbacks":["second","third"]},"rates":{"p/m":` + perMtok10 + `}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	got := comparisonByPlan(t, comparePlans(cfg, ComparisonInput{}), "p")
	var names []string
	for _, h := range got.Harnesses {
		names = append(names, h.Harness)
	}
	if !slices.Equal(names, []string{"first", "second", "third"}) {
		t.Errorf("harnesses = %v, want the default then its fallbacks", names)
	}
}

func withFiveHourLimit(p Provider, limit money.Micros) Provider {
	p.Windows = slices.Clone(p.Windows)
	p.Windows[0].Limit = limit
	return p
}
