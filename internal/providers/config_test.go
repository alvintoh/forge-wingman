package providers

import (
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/money"
)

func TestParseConfigRejectsAnInvalidConfig(t *testing.T) {
	for name, tc := range map[string]struct{ doc, want string }{
		"no cash limit":          {`{"cash_limit_usd":0,"runner_free_minutes":2000,"providers":{"p":{}}}`, "cash limit"},
		"negative free minutes":  {`{"cash_limit_usd":30,"runner_free_minutes":-1,"providers":{"p":{}}}`, "runner free minutes"},
		"no provider":            {`{"cash_limit_usd":30,"runner_free_minutes":2000,"providers":{}}`, "no provider"},
		"an empty provider name": {`{"cash_limit_usd":30,"runner_free_minutes":2000,"providers":{"":{}}}`, "empty name"},
		"a window with no name": {`{"cash_limit_usd":30,"runner_free_minutes":2000,
			"providers":{"p":{"windows":[{"hours":5,"limit_usd":1}]}}}`, "no name"},
		"a window with no period": {`{"cash_limit_usd":30,"runner_free_minutes":2000,
			"providers":{"p":{"windows":[{"name":"p-5h","limit_usd":1}]}}}`, "no period"},
		"a window with no limit": {`{"cash_limit_usd":30,"runner_free_minutes":2000,
			"providers":{"p":{"windows":[{"name":"p-5h","hours":5}]}}}`, "no limit"},
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
	cfg, err := parseConfig([]byte(`{"cash_limit_usd":30,"runner_free_minutes":2000,
		"providers":{"p":{"windows":[{"name":"p-5h","hours":5,"limit_usd":14}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	w := cfg.Providers["p"].Windows[0]
	if w.Period != 5*time.Hour || w.Limit != 14*money.Dollar {
		t.Fatalf("window = %+v, want 5h/$14", w)
	}
	if cfg.CashLimit != 30*money.Dollar || cfg.RunnerFreeMinutes != 2000 {
		t.Fatalf("ceilings = %d/%d, want $30/2000", cfg.CashLimit, cfg.RunnerFreeMinutes)
	}
}

func TestWindowsOfAnUnconfiguredProviderAreEmpty(t *testing.T) {
	if got := Windows("nobody"); got != nil {
		t.Fatalf("Windows(nobody) = %v, want none", got)
	}
}

func TestEmbeddedConfigIsValid(t *testing.T) {
	if _, err := parseConfig(configJSON); err != nil {
		t.Fatal(err)
	}
}
