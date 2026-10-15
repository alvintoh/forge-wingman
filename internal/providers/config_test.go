package providers

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/money"
)

// providerFacts is a provider entry's required addressing fields, as a case
// below extends with the field it varies.
const providerFacts = `"base_url":"https://v.example/v1","key_secret":"k","harnesses":{"default":"h"}`

// configDoc wraps an entry's own JSON body in a minimal valid document.
func configWith(providerBody string) string {
	return `{"cash_limit_usd":30,"runner_free_minutes":2000,"providers":{"p":{` + providerBody + `}}}`
}

func TestParseConfigRejectsAnInvalidConfig(t *testing.T) {
	for name, tc := range map[string]struct{ doc, want string }{
		"no cash limit":          {`{"cash_limit_usd":0,"runner_free_minutes":2000,"providers":{"p":{}}}`, "cash limit"},
		"negative free minutes":  {`{"cash_limit_usd":30,"runner_free_minutes":-1,"providers":{"p":{}}}`, "runner free minutes"},
		"no provider":            {`{"cash_limit_usd":30,"runner_free_minutes":2000,"providers":{}}`, "no provider"},
		"an empty provider name": {`{"cash_limit_usd":30,"runner_free_minutes":2000,"providers":{"":{}}}`, "empty name"},
		"no base URL":            {configWith(`"key_secret":"k","harnesses":{"default":"h"}`), "base URL"},
		"a non-https base URL":   {configWith(`"base_url":"http://v.example/v1","key_secret":"k","harnesses":{"default":"h"}`), "not an https URL"},
		"no key secret":          {configWith(`"base_url":"https://v.example/v1","harnesses":{"default":"h"}`), "no key secret"},
		"no harness":             {configWith(`"base_url":"https://v.example/v1","key_secret":"k"`), "allows no harness"},
		"fallbacks without a default": {configWith(`"base_url":"https://v.example/v1","key_secret":"k","harnesses":{"fallbacks":["h"]}`),
			"no default"},
		"an empty harness name": {configWith(`"base_url":"https://v.example/v1","key_secret":"k","harnesses":{"default":"h","fallbacks":[" "]}`),
			"harness with no name"},
		"a harness listed twice": {configWith(`"base_url":"https://v.example/v1","key_secret":"k","harnesses":{"default":"h","fallbacks":["h"]}`),
			"lists harness h twice"},
		"a window with no name": {configWith(providerFacts + `,"windows":[{"hours":5,"limit_usd":1}]`), "no name"},
		"a window with no period": {configWith(providerFacts + `,"windows":[{"name":"p-5h","limit_usd":1}]`),
			"no period"},
		"a window with no limit": {configWith(providerFacts + `,"windows":[{"name":"p-5h","hours":5}]`),
			"no limit"},
		"a model cap for another provider": {configWith(providerFacts + `,"model_caps":{"other/model":5}`),
			"not a p model"},
		"a model cap with no limit":   {configWith(providerFacts + `,"model_caps":{"p/model":0}`), "cap has no limit"},
		"a model cap naming no model": {configWith(providerFacts + `,"model_caps":{"p/":5}`), "not a p model"},
		"rates for another provider": {configWith(providerFacts + `,"rates":{"other/model":{"input_usd_per_mtok":1}}`),
			"not a p model"},
		"rates naming no model": {configWith(providerFacts + `,"rates":{"p/":{"input_usd_per_mtok":1}}`),
			"not a p model"},
		"a negative rate": {configWith(providerFacts + `,"rates":{"p/model":{"input_usd_per_mtok":-1}}`),
			"negative rate"},
		"a rate with no rate": {configWith(providerFacts + `,"rates":{"p/model":{}}`), "has no rate"},
		"a peak below 1": {configWith(providerFacts + `,"rates":{"p/model":{"input_usd_per_mtok":1,` +
			`"peak":{"multiplier":0.5,"weekday_utc_hours":[{"from":1,"to":4}]}}}`), "below 1"},
		"a peak with no hours": {configWith(providerFacts + `,"rates":{"p/model":{"input_usd_per_mtok":1,` +
			`"peak":{"multiplier":2}}}`), "no hours"},
		"a peak past midnight": {configWith(providerFacts + `,"rates":{"p/model":{"input_usd_per_mtok":1,` +
			`"peak":{"multiplier":2,"weekday_utc_hours":[{"from":22,"to":25}]}}}`), "not a range"},
		"an empty peak range": {configWith(providerFacts + `,"rates":{"p/model":{"input_usd_per_mtok":1,` +
			`"peak":{"multiplier":2,"weekday_utc_hours":[{"from":4,"to":4}]}}}`), "not a range"},
		"a peak before midnight": {configWith(providerFacts + `,"rates":{"p/model":{"input_usd_per_mtok":1,` +
			`"peak":{"multiplier":2,"weekday_utc_hours":[{"from":-1,"to":4}]}}}`), "not a range"},
		"an unknown field": {`{"cash_limit_usd":30,"runner_free_minutes":2000,"providers":{"p":{}},"extra":1}`,
			"unknown field"},
		"a last resort with no models": {configWith(providerFacts + `,"last_resort":{"models":[],"measured_on":"unmeasured"}`),
			"no models"},
		"a last-resort model of another plan": {configWith(providerFacts + `,"last_resort":{"models":["other/free"],"measured_on":"unmeasured"}`),
			"last-resort model \"other/free\", not a p model"},
		"a last-resort model listed twice": {configWith(providerFacts + `,"rates":` + freeRates("p/free") +
			`,"last_resort":{"models":["p/free","p/free"],"measured_on":"unmeasured"}`), "twice"},
		"a capped last-resort model": {configWith(providerFacts + `,"model_caps":{"p/free":5},"rates":` + freeRates("p/free") +
			`,"last_resort":{"models":["p/free"],"measured_on":"unmeasured"}`), "has a model cap"},
		"an unpriced last-resort model": {configWith(providerFacts + `,"last_resort":{"models":["p/free"],"measured_on":"unmeasured"}`),
			"not priced $0"},
		"a priced last-resort model": {configWith(providerFacts + `,"rates":{"p/free":{"input_usd_per_mtok":1}}` +
			`,"last_resort":{"models":["p/free"],"measured_on":"unmeasured"}`), "not priced $0"},
		"a last-resort model with a peak": {configWith(providerFacts + `,"rates":{"p/free":{"peak":{"multiplier":2,"weekday_utc_hours":[{"from":1,"to":4}]}}}` +
			`,"last_resort":{"models":["p/free"],"measured_on":"unmeasured"}`), "not priced $0"},
		"a last resort measured on no date": {configWith(providerFacts + `,"rates":` + freeRates("p/free") +
			`,"last_resort":{"models":["p/free"],"measured_on":"soon"}`), "not a yyyy-mm-dd date"},
		"a last resort naming no models": {configWith(providerFacts + `,"last_resort":{"measured_on":"unmeasured"}`), "no models"},
		"only free-only plans": {configWith(providerFacts + `,"rates":` + freeRates("p/free") +
			`,"last_resort":{"models":["p/free"],"measured_on":"2026-10-08"}`), "every provider is free-only"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseConfig([]byte(tc.doc))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

// freeRates is a rate card pricing each model at $0.
func freeRates(models ...string) string {
	cards := make([]string, len(models))
	for i, m := range models {
		cards[i] = `"` + m + `":{}`
	}
	return "{" + strings.Join(cards, ",") + "}"
}

func TestParseConfigReadsALastResortBesideThePaidModels(t *testing.T) {
	cfg, err := parseConfig([]byte(configWith(providerFacts +
		`,"windows":[{"name":"p-5h","hours":5,"limit_usd":14}],"rates":{"p/paid":{"input_usd_per_mtok":1},"p/a":{},"p/b":{}}` +
		`,"last_resort":{"models":["p/b","p/a"],"measured_on":"unmeasured"}`)))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Providers["p"].LastResort.Models; !slices.Equal(got, []string{"p/b", "p/a"}) {
		t.Fatalf("last resort = %v, want the configured order", got)
	}
}

func TestParseConfigAcceptsAFreeOnlyPlanBesideAPaidOne(t *testing.T) {
	doc := `{"cash_limit_usd":30,"runner_free_minutes":2000,"providers":{
		"paid":{` + providerFacts + `,"windows":[{"name":"paid-5h","hours":5,"limit_usd":14}]},
		"gratis":{` + providerFacts + `,"rates":` + freeRates("gratis/a") + `,"last_resort":{"models":["gratis/a"],"measured_on":"2026-10-08"}}}}`
	if _, err := parseConfig([]byte(doc)); err != nil {
		t.Fatal(err)
	}
}

func TestParseConfigCountsAPlanWithAnythingPaidAsPaid(t *testing.T) {
	lastResort := `,"last_resort":{"models":["p/free"],"measured_on":"unmeasured"}`
	for name, facts := range map[string]string{
		"a window":    `,"windows":[{"name":"p-5h","hours":5,"limit_usd":14}],"rates":` + freeRates("p/free"),
		"a model cap": `,"model_caps":{"p/paid":5},"rates":` + freeRates("p/free"),
		"a paid rate": `,"rates":{"p/paid":{"input_usd_per_mtok":1},"p/free":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseConfig([]byte(configWith(providerFacts + facts + lastResort))); err != nil {
				t.Fatalf("err = %v, want the plan's only provider accepted as paid", err)
			}
		})
	}
}

func TestEmbeddedGoatFallsToItsOwnFreeModels(t *testing.T) {
	want := []string{"command-code/poolside/laguna-s-2.1-free"}
	if got := LastResortModels("command-code"); !slices.Equal(got, want) {
		t.Fatalf("LastResortModels(command-code) = %v, want %v", got, want)
	}
	if got := LastResortModels("nobody"); len(got) != 0 {
		t.Fatalf("LastResortModels(nobody) = %v, want none", got)
	}
}

func TestParseConfigConvertsWindowsAndCeilings(t *testing.T) {
	cfg, err := parseConfig([]byte(configWith(providerFacts +
		`,"windows":[{"name":"p-5h","hours":5,"limit_usd":14}],"model_caps":{"p/model":60}`)))
	if err != nil {
		t.Fatal(err)
	}
	w := cfg.Providers["p"].Windows[0]
	if w.Period != 5*time.Hour || w.Limit != 14*money.Dollar {
		t.Fatalf("window = %+v, want 5h/$14", w)
	}
	if got := cfg.Providers["p"].ModelCaps["p/model"]; got != 60*money.Dollar {
		t.Fatalf("model cap = %v, want $60", got)
	}
	if cfg.CashLimit != 30*money.Dollar || cfg.RunnerFreeMinutes != 2000 {
		t.Fatalf("ceilings = %d/%d, want $30/2000", cfg.CashLimit, cfg.RunnerFreeMinutes)
	}
}

func TestParseConfigReadsEveryPlanFact(t *testing.T) {
	cfg, err := parseConfig([]byte(configWith(providerFacts)))
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Providers["p"]
	if p.BaseURL != "https://v.example/v1" || p.KeySecret != "k" || p.Harnesses.Default != "h" || len(p.Harnesses.Fallbacks) != 0 {
		t.Fatalf("plan = %+v, want the base URL, key secret and harnesses read back", p)
	}
}

func TestWindowsOfAnUnconfiguredProviderAreEmpty(t *testing.T) {
	if got := Windows("nobody"); got != nil {
		t.Fatalf("Windows(nobody) = %v, want none", got)
	}
}

func TestModelCapsOfAnUnconfiguredProviderAreEmpty(t *testing.T) {
	if got := ModelCaps("nobody"); got != nil {
		t.Fatalf("ModelCaps(nobody) = %v, want none", got)
	}
}

// TestASecondPlanIsAddedByConfigurationAlone is the configuration-only
// proof: a whole second plan — its API base, key secret, windows, per-model cap
// and harnesses — is read from a document with no Go change.
func TestASecondPlanIsAddedByConfigurationAlone(t *testing.T) {
	doc := `{"cash_limit_usd":30,"runner_free_minutes":2000,"providers":{
		"command-code":{"base_url":"https://api.example/v1","key_secret":"cc-key","harnesses":{"default":"command-code"},
			"windows":[{"name":"cc-5h","hours":5,"limit_usd":14}]},
		"another-plan":{"base_url":"https://api.other/v1","key_secret":"other-key","harnesses":{"default":"one","fallbacks":["two"]},
			"windows":[{"name":"other-month","hours":720,"limit_usd":40}],
			"model_caps":{"another-plan/vendor/big":25}}}}`
	cfg, err := parseConfig([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	p, ok := cfg.Providers["another-plan"]
	if !ok {
		t.Fatal("the second plan was not read")
	}
	if p.BaseURL != "https://api.other/v1" || p.KeySecret != "other-key" {
		t.Fatalf("plan = %+v, want its own base URL and secret", p)
	}
	if p.Harnesses.Default != "one" || len(p.Harnesses.Fallbacks) != 1 || p.Harnesses.Fallbacks[0] != "two" {
		t.Fatalf("harnesses = %+v, want one defaulting to one and falling back to two", p.Harnesses)
	}
	if len(p.Windows) != 1 || p.Windows[0].Limit != 40*money.Dollar {
		t.Fatalf("windows = %+v, want the plan's own", p.Windows)
	}
	if p.ModelCaps["another-plan/vendor/big"] != 25*money.Dollar {
		t.Fatalf("model caps = %+v, want the plan's own", p.ModelCaps)
	}
}

func TestEmbeddedConfigIsValid(t *testing.T) {
	if _, err := parseConfig(configJSON); err != nil {
		t.Fatal(err)
	}
}

// TestEmbeddedGoatCarriesItsPlanFacts asserts GOAT's entry keeps its plan-wide
// windows and adds its per-model monthly cap, both read through the accessors
// the dispatcher meters with.
func TestEmbeddedGoatCarriesItsPlanFacts(t *testing.T) {
	windows := Windows("command-code")
	if len(windows) != 3 || windows[0].Limit != 14*money.Dollar || windows[1].Limit != 35*money.Dollar || windows[2].Limit != 70*money.Dollar {
		t.Fatalf("windows = %+v, want $14/5h, $35/week and $70/month", windows)
	}
	caps := ModelCaps("command-code")
	flashCap, ok := caps["command-code/deepseek/deepseek-v4.1-flash"]
	if !ok || flashCap.Limit != 60*money.Dollar || flashCap.Period != monthlyPeriod {
		t.Fatalf("flash cap = %+v (ok %v), want $60 over a month", flashCap, ok)
	}
}

// trialModel is the model a build trial runs against the plan's own default.
const trialModel = "command-code/xiaomi/mimo-v2.6-pro"

// TestEmbeddedGoatPricesTheBuildTrialModel asserts the trial's MiMo model is
// priced from the plan's own card, so a run of it is metered against the plan's
// windows like any other paid model of the plan (AC3), and carries no peak hours
// for a run to be held for (AC4).
func TestEmbeddedGoatPricesTheBuildTrialModel(t *testing.T) {
	rates, ok := RatesFor(trialModel)
	if !ok {
		t.Fatalf("RatesFor(%s) = no card, want the plan's own", trialModel)
	}
	if rates.Input != 0.435 || rates.Output != 0.87 || rates.CacheRead != 0.0036 {
		t.Fatalf("rates = %+v, want $0.435 in, $0.87 out and $0.0036 a cache read per 1M", rates)
	}
	if got := rates.CostUSD(1_000_000, 0, 0, 0); got != 0.435 {
		t.Errorf("a million input tokens cost %v, want $0.435", got)
	}
	if got := rates.CostUSD(0, 1_000_000, 0, 0); got != 0.87 {
		t.Errorf("a million output tokens cost %v, want $0.87", got)
	}
	if got := rates.CostUSD(0, 0, 1_000_000, 0); got != 0.0036 {
		t.Errorf("a million cache-read tokens cost %v, want $0.0036", got)
	}
	if rates.Peak != nil {
		t.Fatalf("peak = %+v, want the model priced the same at every hour", rates.Peak)
	}
}

// TestHarnessesReadsThePlansOrder asserts a plan's harness order reads back with
// its default first, and is empty for a plan that is not configured, so the
// router selects from configuration rather than the model prefix.
func TestHarnessesReadsThePlansOrder(t *testing.T) {
	if got := Harnesses("command-code"); !slices.Equal(got, []string{"omp"}) {
		t.Fatalf("Harnesses(command-code) = %v, want omp alone", got)
	}
	if got := Harnesses("nobody"); got != nil {
		t.Fatalf("Harnesses(nobody) = %v, want none", got)
	}
}

// TestRatesReadThePlansCard asserts a plan's rate card reads back by full model
// id, and a model the plan does not price has none, so the runner keeps the
// harness's own figure for it (FR-22).
func TestRatesReadThePlansCard(t *testing.T) {
	cfg, err := parseConfig([]byte(configWith(providerFacts +
		`,"rates":{"p/model":{"input_usd_per_mtok":0.28,"output_usd_per_mtok":0.42,"cache_read_usd_per_mtok":0.028}}`)))
	if err != nil {
		t.Fatal(err)
	}
	rates, ok := ratesFor(cfg, "p/model")
	if !ok || rates.Input != 0.28 || rates.Output != 0.42 || rates.CacheRead != 0.028 {
		t.Fatalf("rates = %+v (ok %v), want the plan's card", rates, ok)
	}
	if _, ok := ratesFor(cfg, "p/other"); ok {
		t.Fatal("a model the plan does not price had rates")
	}
	if _, ok := ratesFor(cfg, "nomodel"); ok {
		t.Fatal("a model with no provider had rates")
	}
}

// TestRatesCostMultipliesTokensByRates asserts the rate card prices a usage from
// its tokens: cost is the sum of each token class times its rate (FR-22).
func TestRatesCostMultipliesTokensByRates(t *testing.T) {
	r := Rates{Input: 1, Output: 2, CacheRead: 0.5, CacheWrite: 3}
	if got := r.CostUSD(1_000_000, 1_000_000, 2_000_000, 1_000_000); got != 7 {
		t.Fatalf("CostUSD = %v, want the tokens times the rates", got)
	}
}

// TestRatesAtPricesByTimeOfUse asserts a card with a peak doubles inside each
// weekday window, keeps its own rates outside them and at weekends, and prices
// an unknown time at peak.
func TestRatesAtPricesByTimeOfUse(t *testing.T) {
	card := Rates{Input: 0.15, Output: 0.6, CacheRead: 0.003,
		Peak: &Peak{Multiplier: 2, Hours: []HourRange{{From: 1, To: 4}, {From: 6, To: 10}}}}
	offPeak := Rates{Input: 0.15, Output: 0.6, CacheRead: 0.003}
	peak := Rates{Input: 0.3, Output: 1.2, CacheRead: 0.006}
	// 2026-10-07 is a Wednesday.
	at := func(day, hour, minute int) time.Time { return time.Date(2026, 10, day, hour, minute, 0, 0, time.UTC) }
	for name, tt := range map[string]struct {
		at   time.Time
		want Rates
	}{
		"off-peak":                 {at(7, 23, 0), offPeak},
		"inside the first window":  {at(7, 2, 30), peak},
		"inside the second window": {at(7, 9, 59), peak},
		"a window's first hour":    {at(7, 6, 0), peak},
		"a window's end hour":      {at(7, 10, 0), offPeak},
		"between the windows":      {at(7, 5, 59), offPeak},
		"saturday in a window":     {at(10, 2, 0), offPeak},
		"sunday in a window":       {at(11, 7, 0), offPeak},
		"another zone, same hour":  {at(7, 7, 0).In(time.FixedZone("AEDT", 11*3600)), peak},
		"an unknown time":          {time.Time{}, peak},
	} {
		t.Run(name, func(t *testing.T) {
			if got := card.At(tt.at); got != tt.want {
				t.Fatalf("At = %+v, want %+v", got, tt.want)
			}
		})
	}
	if got := offPeak.At(time.Time{}); got != offPeak {
		t.Fatalf("a card with no peak at an unknown time = %+v, want its own rates", got)
	}
}

// TestPeakSpans asserts a span touches a window when any hour it covers is in
// one, including a span that ends on a window's first instant, and never on a
// weekend.
func TestPeakSpans(t *testing.T) {
	peak := Peak{Multiplier: 2, Hours: []HourRange{{From: 1, To: 4}, {From: 6, To: 10}}}
	// 2026-10-07 is a Wednesday.
	at := func(day, hour, minute int) time.Time { return time.Date(2026, 10, day, hour, minute, 0, 0, time.UTC) }
	for name, tt := range map[string]struct {
		from time.Time
		d    time.Duration
		want bool
	}{
		"inside a window with no duration":    {at(7, 2, 0), 0, true},
		"off-peak with no duration":           {at(7, 5, 0), 0, false},
		"a span opening inside a window":      {at(7, 0, 45), 30 * time.Minute, true},
		"a span ending before the window":     {at(7, 0, 45), 10 * time.Minute, false},
		"a span ending on a window's start":   {at(7, 0, 45), 15 * time.Minute, true},
		"a span ending a minute before it":    {at(7, 0, 45), 14 * time.Minute, false},
		"a span across the gap into a window": {at(7, 4, 0), 2 * time.Hour, true},
		"a span inside the gap":               {at(7, 4, 0), 90 * time.Minute, false},
		"a span out of a window":              {at(7, 3, 30), time.Hour, true},
		"a weekend span over the hours":       {at(10, 0, 0), 12 * time.Hour, false},
		"a Friday span running into Saturday": {at(9, 22, 0), 6 * time.Hour, false},
		"a Sunday span running into a Monday": {at(11, 23, 30), 90 * time.Minute, true},
		"an unknown start":                    {time.Time{}, 0, true},
		"another zone, the same peak instant": {at(7, 7, 0).In(time.FixedZone("AEDT", 11*3600)), 0, true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := peak.Spans(tt.from, tt.d); got != tt.want {
				t.Fatalf("Spans(%v, %v) = %v, want %v", tt.from, tt.d, got, tt.want)
			}
		})
	}
}

// TestRatesReadThePeak asserts a card's surcharge reads back from its document.
func TestRatesReadThePeak(t *testing.T) {
	cfg, err := parseConfig([]byte(configWith(providerFacts + `,"rates":{"p/model":{"input_usd_per_mtok":1,` +
		`"peak":{"multiplier":2,"weekday_utc_hours":[{"from":1,"to":4},{"from":6,"to":10}]}}}`)))
	if err != nil {
		t.Fatal(err)
	}
	rates, _ := ratesFor(cfg, "p/model")
	if rates.Peak == nil || rates.Peak.Multiplier != 2 ||
		!slices.Equal(rates.Peak.Hours, []HourRange{{From: 1, To: 4}, {From: 6, To: 10}}) {
		t.Fatalf("peak = %+v, want x2 over 01-04 and 06-10", rates.Peak)
	}
}

// TestParseConfigAcceptsARateCardsBoundaries asserts the limits a card may sit
// on are valid: a cache-write rate alone, a multiplier of exactly 1, and a
// window running to midnight.
func TestParseConfigAcceptsARateCardsBoundaries(t *testing.T) {
	for name, rates := range map[string]string{
		"a cache-write rate alone": `{"cache_write_usd_per_mtok":1}`,
		"a multiplier of 1": `{"input_usd_per_mtok":1,` +
			`"peak":{"multiplier":1,"weekday_utc_hours":[{"from":1,"to":4}]}}`,
		"a window to midnight": `{"input_usd_per_mtok":1,` +
			`"peak":{"multiplier":2,"weekday_utc_hours":[{"from":0,"to":24}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseConfig([]byte(configWith(providerFacts + `,"rates":{"p/model":` + rates + `}`))); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// twoPlanDoc wraps a paid plan and a free-only one in one document, with the
// given last_resort_plans body, so the order's validation is exercised against a
// configuration that has both kinds of plan.
func twoPlanDoc(order string) string {
	return `{"cash_limit_usd":30,"runner_free_minutes":2000,` + order + `"providers":{
		"paid":{` + providerFacts + `,"windows":[{"name":"paid-5h","hours":5,"limit_usd":14}]},
		"gratis":{` + providerFacts + `,"rates":` + freeRates("gratis/a") + `,"last_resort":{"models":["gratis/a"],"measured_on":"2026-10-08"}}}}`
}

// TestParseConfigRejectsAnInvalidLastResortOrder asserts the ordered free-tier
// plans must be configured providers that actually hold a free tier, each named
// once.
func TestParseConfigRejectsAnInvalidLastResortOrder(t *testing.T) {
	for name, tc := range map[string]struct{ order, want string }{
		"an unconfigured plan":     {`"last_resort_plans":["nobody"],`, "not configured"},
		"a plan with no free tier": {`"last_resort_plans":["paid"],`, "no free tier"},
		"a plan named twice":       {`"last_resort_plans":["gratis","gratis"],`, "twice"},
		"an empty plan":            {`"last_resort_plans":[""],`, "empty plan"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseConfig([]byte(twoPlanDoc(tc.order)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

// TestParseConfigReadsTheLastResortOrder asserts the ordered free-tier plans are
// read back in the order the document names them, so the dispatcher walks the
// configuration rather than a hardcoded sequence.
func TestParseConfigReadsTheLastResortOrder(t *testing.T) {
	cfg, err := parseConfig([]byte(twoPlanDoc(`"last_resort_plans":["gratis"],`)))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.LastResortPlans; !slices.Equal(got, []string{"gratis"}) {
		t.Fatalf("LastResortPlans = %v, want the configured order", got)
	}
}

// TestEmbeddedConfigNamesTheFreeTierOrder asserts the embedded configuration
// walks the plans' free tiers in the configured order, so a paid plan whose
// allowance is spent falls through to the next plan's free models.
func TestEmbeddedConfigNamesTheFreeTierOrder(t *testing.T) {
	if got := LastResortPlans(); !slices.Equal(got, []string{"command-code", "gemini"}) {
		t.Fatalf("LastResortPlans() = %v, want command-code then gemini", got)
	}
	if got := Plans(); !slices.Equal(got, []string{"command-code", "gemini"}) {
		t.Fatalf("Plans() = %v, want both configured plans, sorted", got)
	}
}

// TestEmbeddedConfigWalksItsPlansPaidFirstThenFree asserts the dispatch order
// names each plan once: the paid plan a ticket starts on first, then the
// free-tier fallback, so a plan listed in both is not walked twice.
func TestEmbeddedConfigWalksItsPlansPaidFirstThenFree(t *testing.T) {
	if got, want := DispatchOrder(), []string{"command-code", "gemini"}; !slices.Equal(got, want) {
		t.Fatalf("DispatchOrder() = %v, want %v", got, want)
	}
}

// TestDispatchOrderIsThePaidPlansThenTheLastResortPlansTheDispatcherWalks asserts
// the providers view's order is the one the dispatcher reads from PaidPlans and
// LastResortPlans, each plan named once even when both list it.
func TestDispatchOrderIsThePaidPlansThenTheLastResortPlansTheDispatcherWalks(t *testing.T) {
	saved := embeddedConfig
	t.Cleanup(func() { embeddedConfig = saved })
	embeddedConfig.PaidPlans = []string{"b", "a"}
	embeddedConfig.LastResortPlans = []string{"c", "a", "d"}

	var walked []string
	for _, plan := range append(PaidPlans(), LastResortPlans()...) {
		if !slices.Contains(walked, plan) {
			walked = append(walked, plan)
		}
	}
	if got := DispatchOrder(); !slices.Equal(got, walked) || !slices.Equal(got, []string{"b", "a", "c", "d"}) {
		t.Errorf("DispatchOrder() = %v, dispatcher walks %v", got, walked)
	}
}

// TestEmbeddedGeminiIsAFreeTierPlan asserts the second plan is configuration
// alone: its OpenAI-compatible base URL, its own key secret, its opencode
// harness and its free models priced $0, all read through the accessors the
// runner and dispatcher use.
func TestEmbeddedGeminiIsAFreeTierPlan(t *testing.T) {
	if got := BaseURL("gemini"); got != "https://generativelanguage.googleapis.com/v1beta/openai/" {
		t.Fatalf("BaseURL(gemini) = %q, want the OpenAI-compatible endpoint", got)
	}
	if got := KeySecret("gemini"); got != "GEMINI_API_KEY" {
		t.Fatalf("KeySecret(gemini) = %q", got)
	}
	if got := Harnesses("gemini"); !slices.Equal(got, []string{"omp"}) {
		t.Fatalf("Harnesses(gemini) = %v, want omp", got)
	}
	models := LastResortModels("gemini")
	if len(models) != 2 || models[0] != "gemini/gemini-3.8-flash" || models[1] != "gemini/gemini-3.5-flash-lite" {
		t.Fatalf("LastResortModels(gemini) = %v, want the two free models", models)
	}
	for _, model := range models {
		rates, ok := RatesFor(model)
		if !ok || rates.Input != 0 || rates.Output != 0 || rates.CacheRead != 0 || rates.CacheWrite != 0 {
			t.Fatalf("RatesFor(%s) = %+v (ok %v), want every rate $0", model, rates, ok)
		}
	}
}

func TestPlansListsEveryConfiguredPlanSorted(t *testing.T) {
	// Map order is random, so one call can come out sorted by chance.
	for range 32 {
		if got, want := Plans(), []string{"command-code", "gemini"}; !slices.Equal(got, want) {
			t.Fatalf("Plans() = %q, want %q", got, want)
		}
	}
}

// paidPlanDoc is a document with two paid plans, each carrying its models, and a
// free-only third, with order as the paid-plan order.
func paidPlanDoc(order, firstModels string) string {
	return `{"cash_limit_usd":30,"runner_free_minutes":2000,` + order + `"providers":{
		"alpha":{` + providerFacts + `,"windows":[{"name":"alpha-5h","hours":5,"limit_usd":14}],` + firstModels + `},
		"beta":{` + providerFacts + `,"windows":[{"name":"beta-5h","hours":5,"limit_usd":9}],` +
		`"models":{"build":"beta/big","review":"beta/small","plan":["beta/big","beta/mid"]}},
		"gratis":{` + providerFacts + `,"rates":` + freeRates("gratis/a") + `,"last_resort":{"models":["gratis/a"],"measured_on":"2026-10-08"}}}}`
}

const alphaModels = `"models":{"build":"alpha/big","review":"alpha/small","plan":["alpha/big"]}`

func TestParseConfigRejectsAnInvalidPaidPlanOrder(t *testing.T) {
	for name, tc := range map[string]struct{ order, models, want string }{
		"an unconfigured plan": {`"paid_plans":["nobody"],`, alphaModels, "not configured"},
		"a plan named twice":   {`"paid_plans":["alpha","alpha"],`, alphaModels, "twice"},
		"an empty plan":        {`"paid_plans":[""],`, alphaModels, "empty plan"},
		"a free-only plan":     {`"paid_plans":["gratis"],`, alphaModels, "free-only"},
		"a plan with no models": {`"paid_plans":["alpha"],`, `"windows":[{"name":"alpha-1h","hours":1,"limit_usd":1}]`,
			"needs a build model"},
		"a plan with no review model": {`"paid_plans":["alpha"],`,
			`"models":{"build":"alpha/big","plan":["alpha/big"]}`, "needs a build model"},
		"a plan with no plan models": {`"paid_plans":["alpha"],`,
			`"models":{"build":"alpha/big","review":"alpha/small"}`, "needs a build model"},
		"a review model that is the build model": {`"paid_plans":["alpha"],`,
			`"models":{"build":"alpha/big","review":"alpha/big","plan":["alpha/big"]}`, "reviews with its build model"},
		"a model of another plan": {`"paid_plans":["alpha"],`,
			`"models":{"build":"alpha/big","review":"beta/small","plan":["alpha/big"]}`, "not a alpha model"},
		"a plan model listed twice": {`"paid_plans":["alpha"],`,
			`"models":{"build":"alpha/big","review":"alpha/small","plan":["alpha/big","alpha/big"]}`, "plan model alpha/big twice"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseConfig([]byte(paidPlanDoc(tc.order, tc.models)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestParseConfigReadsThePaidPlanOrderAndModels(t *testing.T) {
	cfg, err := parseConfig([]byte(paidPlanDoc(`"paid_plans":["alpha","beta"],`, alphaModels)))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.PaidPlans, []string{"alpha", "beta"}) {
		t.Fatalf("PaidPlans = %v, want alpha then beta", cfg.PaidPlans)
	}
	want := Models{Build: "beta/big", Review: "beta/small", Plan: []string{"beta/big", "beta/mid"}}
	if got := cfg.Providers["beta"].Models; got.Build != want.Build || got.Review != want.Review || !slices.Equal(got.Plan, want.Plan) {
		t.Fatalf("beta Models = %+v, want %+v", got, want)
	}
}

// TestRunModelsListsEveryModelARunMayUseOnce asserts the models a run may use
// are each plan's build, review and plan models and its free-tier ones, each
// named once — the review model included, which its own plan's rate card does
// not price.
func TestRunModelsListsEveryModelARunMayUseOnce(t *testing.T) {
	want := []string{
		"command-code/deepseek/deepseek-v4.1-flash",
		"command-code/meta/muse-spark-1.3-contributor",
		"command-code/poolside/laguna-s-2.1-free",
		"gemini/gemini-3.8-flash",
		"gemini/gemini-3.5-flash-lite",
	}
	if got := RunModels(); !slices.Equal(got, want) {
		t.Fatalf("RunModels() = %v, want %v", got, want)
	}
}

// TestRunModelsWalksAPlansPlanModels asserts a plan's own plan models are walked
// beside its build and review models, from a plan the embedded config does not
// hold: there the one plan model is also that plan's build model, so dropping
// the walk over them changes nothing the embedded config can show. The plan
// names its build model too, so the walk names it once (AC1).
func TestRunModelsWalksAPlansPlanModels(t *testing.T) {
	cfg, err := parseConfig([]byte(configWith(providerFacts +
		`,"models":{"build":"p/build","review":"p/review","plan":["p/build","p/plan"]}}`)))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"p/build", "p/review", "p/plan"}
	if got := runModels(cfg.Providers, []string{"p"}); !slices.Equal(got, want) {
		t.Fatalf("runModels = %v, want %v", got, want)
	}
}

func TestEmbeddedGoatIsTheOnlyPaidPlan(t *testing.T) {
	if got := PaidPlans(); !slices.Equal(got, []string{"command-code"}) {
		t.Fatalf("PaidPlans() = %v, want command-code alone", got)
	}
	m := PlanModels("command-code")
	if m.Build != "command-code/deepseek/deepseek-v4.1-flash" || m.Review != "command-code/meta/muse-spark-1.3-contributor" ||
		!slices.Equal(m.Plan, []string{"command-code/deepseek/deepseek-v4.1-flash"}) {
		t.Fatalf("PlanModels(command-code) = %+v", m)
	}
}
