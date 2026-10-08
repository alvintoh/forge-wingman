package providers

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
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

// monthlyPeriod is the window a plan's per-model cap is stated over, matching
// the plan-wide monthly window's own convention: one month, rolling.
const monthlyPeriod = 720 * time.Hour

// HarnessOrder is the harnesses a plan runs through, in selection order: the
// default first, then the ordered fallbacks the router moves to when the one
// before is not registered or not ready.
type HarnessOrder struct {
	Default   string
	Fallbacks []string
}

// Rates is a plan's price for one model, in US dollars per million tokens, by
// token class: the plan's rate card. The runner meters a run from it, so a
// harness whose own cost figure is absent or wrong is still priced (FR-22).
type Rates struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
	// Peak is the card's time-of-use surcharge; nil for a card priced the same at every hour.
	Peak *Peak
}

// Peak is a time-of-use surcharge: Monday to Friday, inside any of Hours (UTC),
// every rate is multiplied by Multiplier. A vendor's off-peak public holidays are
// not modelled, so they meter at peak, never under.
type Peak struct {
	Multiplier float64
	Hours      []HourRange
}

// HourRange is the hours [From, To) of a UTC day.
type HourRange struct {
	From int
	To   int
}

// At returns the rates in force at t: the card's own, multiplied when t falls
// inside a peak window. A zero t, a step whose time is unknown, prices at peak,
// so a window is over-metered rather than under.
func (r Rates) At(t time.Time) Rates {
	flat := Rates{Input: r.Input, Output: r.Output, CacheRead: r.CacheRead, CacheWrite: r.CacheWrite}
	if r.Peak == nil || (!t.IsZero() && !r.Peak.covers(t)) {
		return flat
	}
	m := r.Peak.Multiplier
	return Rates{Input: flat.Input * m, Output: flat.Output * m, CacheRead: flat.CacheRead * m, CacheWrite: flat.CacheWrite * m}
}

// covers reports whether t falls inside one of the peak windows.
func (p Peak) covers(t time.Time) bool {
	t = t.UTC()
	if wd := t.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return false
	}
	h := t.Hour()
	for _, w := range p.Hours {
		if h >= w.From && h < w.To {
			return true
		}
	}
	return false
}

// CostUSD is the USD cost of a usage of tokens at these rates.
func (r Rates) CostUSD(input, output, cacheRead, cacheWrite int64) float64 {
	return (float64(input)*r.Input + float64(output)*r.Output +
		float64(cacheRead)*r.CacheRead + float64(cacheWrite)*r.CacheWrite) / 1_000_000
}

// Provider is one vendor plan the runner is configured to use, as
// providers.json describes it: where the plan's OpenAI-compatible API is, the
// secret holding its key, its own rolling allowance windows, the monthly cap it
// sets per model, and the harnesses allowed to run on it.
type Provider struct {
	// BaseURL is the plan's OpenAI-compatible API base.
	BaseURL string
	// KeySecret names the secret holding the plan's API key.
	KeySecret string
	// Windows are the plan-wide rolling allowance ceilings.
	Windows []Window
	// ModelCaps are the plan's own monthly ceilings, keyed by the full model id
	// a run names. A model absent here has no cap of its own and is bounded by
	// Windows alone.
	ModelCaps map[string]money.Micros
	// Harnesses is the plan's harness order: the default first, then its
	// ordered fallbacks.
	Harnesses HarnessOrder
	// Rates is the plan's rate card, keyed by the full model id a run names,
	// in USD per million tokens. A model absent here keeps the harness's own
	// cost figure.
	Rates map[string]Rates
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
	BaseURL   string              `json:"base_url"`
	KeySecret string              `json:"key_secret"`
	Windows   []windowDoc         `json:"windows"`
	ModelCaps map[string]float64  `json:"model_caps"`
	Harnesses harnessDoc          `json:"harnesses"`
	Rates     map[string]ratesDoc `json:"rates"`
}

// harnessDoc is a plan's harness order as providers.json writes it: the default
// harness, then the ordered fallbacks.
type harnessDoc struct {
	Default   string   `json:"default"`
	Fallbacks []string `json:"fallbacks"`
}

// ratesDoc is one model's rate card as providers.json writes it: US dollars per
// million tokens, by token class.
type ratesDoc struct {
	Input      float64  `json:"input_usd_per_mtok"`
	Output     float64  `json:"output_usd_per_mtok"`
	CacheRead  float64  `json:"cache_read_usd_per_mtok"`
	CacheWrite float64  `json:"cache_write_usd_per_mtok"`
	Peak       *peakDoc `json:"peak"`
}

// peakDoc is a rate card's surcharge as providers.json writes it: the
// multiplier, and the weekday UTC hour ranges it applies in, each [from, to).
type peakDoc struct {
	Multiplier      float64        `json:"multiplier"`
	WeekdayUTCHours []hourRangeDoc `json:"weekday_utc_hours"`
}

type hourRangeDoc struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// rates converts a rate card from its document shape.
func (d ratesDoc) rates() Rates {
	r := Rates{Input: d.Input, Output: d.Output, CacheRead: d.CacheRead, CacheWrite: d.CacheWrite}
	if d.Peak != nil {
		r.Peak = &Peak{Multiplier: d.Peak.Multiplier}
		for _, h := range d.Peak.WeekdayUTCHours {
			r.Peak.Hours = append(r.Peak.Hours, HourRange(h))
		}
	}
	return r
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
		provider := Provider{
			BaseURL:   p.BaseURL,
			KeySecret: p.KeySecret,
			Harnesses: HarnessOrder{Default: p.Harnesses.Default, Fallbacks: p.Harnesses.Fallbacks},
		}
		for _, w := range p.Windows {
			provider.Windows = append(provider.Windows, Window{
				Name:   w.Name,
				Period: time.Duration(w.Hours * float64(time.Hour)),
				Limit:  money.FromUSD(w.LimitUSD),
			})
		}
		if len(p.ModelCaps) > 0 {
			provider.ModelCaps = make(map[string]money.Micros, len(p.ModelCaps))
			for model, limitUSD := range p.ModelCaps {
				provider.ModelCaps[model] = money.FromUSD(limitUSD)
			}
		}
		if len(p.Rates) > 0 {
			provider.Rates = make(map[string]Rates, len(p.Rates))
			for model, r := range p.Rates {
				provider.Rates[model] = r.rates()
			}
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
		if err := checkURL(p.BaseURL); err != nil {
			return fmt.Errorf("provider %s base URL: %w", name, err)
		}
		if p.KeySecret == "" {
			return fmt.Errorf("provider %s has no key secret", name)
		}
		if err := checkHarnessOrder(name, p.Harnesses); err != nil {
			return err
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
		for model, limit := range p.ModelCaps {
			switch {
			case !isPlanModel(name, model):
				return fmt.Errorf("provider %s has a model cap for %q, not a %s model", name, model, name)
			case limit <= 0:
				return fmt.Errorf("provider %s model %s cap has no limit", name, model)
			}
		}
		for model, r := range p.Rates {
			switch {
			case !isPlanModel(name, model):
				return fmt.Errorf("provider %s has rates for %q, not a %s model", name, model, name)
			case min(r.Input, r.Output, r.CacheRead, r.CacheWrite) < 0:
				return fmt.Errorf("provider %s model %s has a negative rate", name, model)
			case max(r.Input, r.Output, r.CacheRead, r.CacheWrite) == 0:
				return fmt.Errorf("provider %s model %s has no rate", name, model)
			}
			if err := checkPeak(r.Peak); err != nil {
				return fmt.Errorf("provider %s model %s peak: %w", name, model, err)
			}
		}
	}
	return nil
}

// checkPeak reports whether a surcharge is usable: a multiplier of at least 1
// and one or more non-empty hour ranges within a day.
func checkPeak(p *Peak) error {
	if p == nil {
		return nil
	}
	if p.Multiplier < 1 {
		return fmt.Errorf("multiplier %v is below 1", p.Multiplier)
	}
	if len(p.Hours) == 0 {
		return errors.New("no hours")
	}
	for _, h := range p.Hours {
		if h.From < 0 || h.To > 24 || h.From >= h.To {
			return fmt.Errorf("hours %d-%d are not a range within a day", h.From, h.To)
		}
	}
	return nil
}

// isPlanModel reports whether model is a full model id under plan: the plan's
// prefix followed by a non-empty model name.
func isPlanModel(plan, model string) bool {
	rest, ok := strings.CutPrefix(model, plan+"/")
	return ok && rest != ""
}

// checkHarnessOrder reports whether a plan's harness order is usable: a default
// plus fallbacks, each named, and no harness listed twice.
func checkHarnessOrder(provider string, order HarnessOrder) error {
	switch {
	case order.Default == "" && len(order.Fallbacks) == 0:
		return fmt.Errorf("provider %s allows no harness", provider)
	case strings.TrimSpace(order.Default) == "":
		return fmt.Errorf("provider %s has fallback harnesses but no default", provider)
	}
	seen := map[string]bool{order.Default: true}
	for _, h := range order.Fallbacks {
		switch {
		case strings.TrimSpace(h) == "":
			return fmt.Errorf("provider %s has a harness with no name", provider)
		case seen[h]:
			return fmt.Errorf("provider %s lists harness %s twice", provider, h)
		}
		seen[h] = true
	}
	return nil
}

// Harnesses is a plan's harness order: the default harness first, then its
// ordered fallbacks. Empty for a plan that is not configured.
func Harnesses(plan string) []string {
	order := embeddedConfig.Providers[plan].Harnesses
	if order.Default == "" {
		return nil
	}
	return append([]string{order.Default}, order.Fallbacks...)
}

// KeySecret names the secret holding the plan's API key, empty for a plan that is not configured.
func KeySecret(plan string) string { return embeddedConfig.Providers[plan].KeySecret }

// BaseURL is the plan's OpenAI-compatible API base, empty for a plan that is not configured.
func BaseURL(plan string) string { return embeddedConfig.Providers[plan].BaseURL }

// RatesFor returns the plan's rate card for model, and whether the plan prices
// it. A model the plan does not price keeps the harness's own cost figure.
func RatesFor(model string) (Rates, bool) { return ratesFor(embeddedConfig, model) }

// ratesFor returns cfg's rate card for model: the plan the model's provider
// prefix names holds it, keyed by the full model id.
func ratesFor(cfg Config, model string) (Rates, bool) {
	plan, _, _ := strings.Cut(model, "/")
	rates, ok := cfg.Providers[plan].Rates[model]
	return rates, ok
}

// Windows are a provider's own rolling allowance ceilings, in the order the plan
// states them; empty for a provider configured without any, whose cost is then
// checked against CashLimit.
func Windows(provider string) []Window {
	return slices.Clone(embeddedConfig.Providers[provider].Windows)
}

// ModelCaps are a provider's own monthly ceilings per model, keyed by the full
// model id a run names, each over a rolling month; empty for a provider
// configured without any, whose runs are then bounded by its Windows alone.
func ModelCaps(provider string) map[string]Window {
	caps := embeddedConfig.Providers[provider].ModelCaps
	if len(caps) == 0 {
		return nil
	}
	out := make(map[string]Window, len(caps))
	for model, limit := range caps {
		out[model] = Window{Name: model, Period: monthlyPeriod, Limit: limit}
	}
	return out
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
