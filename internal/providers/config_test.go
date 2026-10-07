package providers

import (
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/money"
)

// providerFacts is a provider entry's required addressing fields, as a case
// below extends with the field it varies.
const providerFacts = `"base_url":"https://v.example/v1","key_secret":"k","harnesses":["h"]`

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
		"no base URL":            {configWith(`"key_secret":"k","harnesses":["h"]`), "base URL"},
		"a non-https base URL":   {configWith(`"base_url":"http://v.example/v1","key_secret":"k","harnesses":["h"]`), "not an https URL"},
		"no key secret":          {configWith(`"base_url":"https://v.example/v1","harnesses":["h"]`), "no key secret"},
		"no harness":             {configWith(`"base_url":"https://v.example/v1","key_secret":"k"`), "allows no harness"},
		"an empty harness name": {configWith(`"base_url":"https://v.example/v1","key_secret":"k","harnesses":["h"," "]`),
			"harness with no name"},
		"a window with no name": {configWith(providerFacts + `,"windows":[{"hours":5,"limit_usd":1}]`), "no name"},
		"a window with no period": {configWith(providerFacts + `,"windows":[{"name":"p-5h","limit_usd":1}]`),
			"no period"},
		"a window with no limit": {configWith(providerFacts + `,"windows":[{"name":"p-5h","hours":5}]`),
			"no limit"},
		"a model cap for another provider": {configWith(providerFacts + `,"model_caps":{"other/model":5}`),
			"not a p model"},
		"a model cap with no limit":   {configWith(providerFacts + `,"model_caps":{"p/model":0}`), "cap has no limit"},
		"a model cap naming no model": {configWith(providerFacts + `,"model_caps":{"p/":5}`), "not a p model"},
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
	if p.BaseURL != "https://v.example/v1" || p.KeySecret != "k" || len(p.Harnesses) != 1 || p.Harnesses[0] != "h" {
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
		"command-code":{"base_url":"https://api.example/v1","key_secret":"cc-key","harnesses":["command-code"],
			"windows":[{"name":"cc-5h","hours":5,"limit_usd":14}]},
		"another-plan":{"base_url":"https://api.other/v1","key_secret":"other-key","harnesses":["one","two"],
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
	if len(p.Harnesses) != 2 || p.Harnesses[0] != "one" || p.Harnesses[1] != "two" {
		t.Fatalf("harnesses = %v, want the two the plan allows", p.Harnesses)
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
