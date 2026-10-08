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
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseConfig([]byte(tc.doc))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
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

// TestASecondPlanIsAddedByConfigurationAlone is FRG-62's configuration-only
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

// TestHarnessesReadsThePlansOrder asserts a plan's harness order reads back with
// its default first, and is empty for a plan that is not configured, so the
// router selects from configuration rather than the model prefix.
func TestHarnessesReadsThePlansOrder(t *testing.T) {
	if got := Harnesses("command-code"); !slices.Equal(got, []string{"omp", "command-code"}) {
		t.Fatalf("Harnesses(command-code) = %v, want omp then the command-code fallback", got)
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
