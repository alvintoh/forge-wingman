package providers

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/alvintoh/forge-wingman/internal/money"
)

//go:embed providers.json
var configJSON []byte

// Window is one of a provider's own rolling allowance ceilings, as its plan
// states it: the settled cost of runs that ended within Period must not exceed
// Limit.
type Window struct {
	Name   string
	Period time.Duration
	Limit  money.Micros
}

// Provider is one vendor the runner is configured to use, as providers.json
// describes it.
type Provider struct {
	Windows []Window
}

// Config is the runner's provider configuration: the vendor facts that are
// configuration rather than code, so a provider is added or replaced by editing
// configuration, never the Go.
type Config struct {
	// CashLimit is NFR-1's ceiling on provider cost, checked instead of a
	// windowed provider's own windows for a per-token one.
	CashLimit money.Micros
	// RunnerFreeMinutes is NFR-1's free GitHub Actions minutes on a private target.
	RunnerFreeMinutes int64
	// AllowanceMarkers and AvailabilityMarkers are the phrases a failed run's
	// stderr may carry when a provider's own allowance is exhausted and when
	// the requested model is unavailable. UNVERIFIED against a live exhaustion
	// or outage: no probe has confirmed a provider's actual wording, so these
	// are a documented assumption pending that verification.
	AllowanceMarkers    []string
	AvailabilityMarkers []string
	// Providers maps each configured provider name to its plan facts.
	Providers map[string]Provider
}

// configDoc is configJSON's own shape; the parsed Config is the converted one.
type configDoc struct {
	CashLimitUSD        float64                `json:"cash_limit_usd"`
	RunnerFreeMinutes   int64                  `json:"runner_free_minutes"`
	AllowanceMarkers    []string               `json:"allowance_markers"`
	AvailabilityMarkers []string               `json:"availability_markers"`
	Providers           map[string]providerDoc `json:"providers"`
}

type providerDoc struct {
	Windows []windowDoc `json:"windows"`
}

type windowDoc struct {
	Name     string  `json:"name"`
	Hours    float64 `json:"hours"`
	LimitUSD float64 `json:"limit_usd"`
}

var embeddedConfig = mustParseConfig(configJSON)

// parseConfig decodes and validates a providers.json document, converting
// every dollar figure to Micros and every window length to a Duration.
//
// Unknown fields are rejected, since the file is this repository's own contract.
func parseConfig(b []byte) (Config, error) {
	var doc configDoc
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return Config{}, fmt.Errorf("decoding providers: %w", err)
	}
	cfg := Config{
		CashLimit:           money.FromUSD(doc.CashLimitUSD),
		RunnerFreeMinutes:   doc.RunnerFreeMinutes,
		AllowanceMarkers:    doc.AllowanceMarkers,
		AvailabilityMarkers: doc.AvailabilityMarkers,
		Providers:           make(map[string]Provider, len(doc.Providers)),
	}
	for name, p := range doc.Providers {
		provider := Provider{}
		for _, w := range p.Windows {
			provider.Windows = append(provider.Windows, Window{
				Name:   w.Name,
				Period: time.Duration(w.Hours * float64(time.Hour)),
				Limit:  money.FromUSD(w.LimitUSD),
			})
		}
		cfg.Providers[name] = provider
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func mustParseConfig(b []byte) Config {
	cfg, err := parseConfig(b)
	if err != nil {
		panic(err)
	}
	return cfg
}

func (c Config) validate() error {
	if c.CashLimit <= 0 {
		return errors.New("cash limit is not a positive amount")
	}
	if c.RunnerFreeMinutes < 0 {
		return errors.New("runner free minutes is negative")
	}
	if len(c.Providers) == 0 {
		return errors.New("no provider is configured")
	}
	for name, p := range c.Providers {
		if name == "" {
			return errors.New("a provider has an empty name")
		}
		for _, w := range p.Windows {
			switch {
			case w.Name == "":
				return fmt.Errorf("provider %s has a window with no name", name)
			case w.Period <= 0:
				return fmt.Errorf("provider %s window %s has no period", name, w.Name)
			case w.Limit <= 0:
				return fmt.Errorf("provider %s window %s has no limit", name, w.Name)
			}
		}
	}
	return nil
}

// Windows are a provider's own rolling allowance ceilings, in the order the plan
// states them; empty for a provider configured without any, whose cost is then
// checked against CashLimit.
func Windows(provider string) []Window {
	return slices.Clone(embeddedConfig.Providers[provider].Windows)
}

// CashLimit is NFR-1's cash ceiling on provider cost.
func CashLimit() money.Micros { return embeddedConfig.CashLimit }

// RunnerFreeMinutes is NFR-1's free GitHub Actions minutes on a private target.
func RunnerFreeMinutes() int64 { return embeddedConfig.RunnerFreeMinutes }

// AllowanceMarkers are the phrases a failed run's stderr may carry when a
// provider's own allowance is exhausted.
func AllowanceMarkers() []string { return embeddedConfig.AllowanceMarkers }

// AvailabilityMarkers are the phrases a failed run's stderr may carry when the
// requested model is unavailable.
func AvailabilityMarkers() []string { return embeddedConfig.AvailabilityMarkers }
