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
	if r.Peak == nil || (!t.IsZero() && !r.Peak.Covers(t)) {
		return flat
	}
	m := r.Peak.Multiplier
	return Rates{Input: flat.Input * m, Output: flat.Output * m, CacheRead: flat.CacheRead * m, CacheWrite: flat.CacheWrite * m}
}

// Covers reports whether t falls inside one of the peak windows.
func (p Peak) Covers(t time.Time) bool {
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

// Spans reports whether any part of the span of length d from from falls in one
// of the peak windows; a zero from, an unknown time, spans peak. The windows are
// whole hours, so walking the hours the span touches is exact, and a span ending
// on the first instant of a window touches it.
func (p Peak) Spans(from time.Time, d time.Duration) bool {
	if from.IsZero() {
		return true
	}
	end := from.Add(d)
	for at := from.Truncate(time.Hour); !at.After(end); at = at.Add(time.Hour) {
		if p.Covers(at) {
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
	// OutputCaps are the plan's own per-model output-token ceilings, keyed by the
	// full model id a run names. A model absent here is asked for with the
	// harness's own default, which some APIs reject outright.
	OutputCaps map[string]int
	// Harnesses is the plan's harness order: the default first, then its
	// ordered fallbacks.
	Harnesses HarnessOrder
	// Rates is the plan's rate card, keyed by the full model id a run names,
	// in USD per million tokens. A model absent here keeps the harness's own
	// cost figure.
	Rates map[string]Rates
	// LastResort is the plan's own free tier, empty for a plan without one.
	LastResort LastResort
	// Models are the models a run moved onto this plan starts on, empty for a
	// plan no run is moved onto.
	Models Models
}

// Models are the models a run on a plan starts on, every one of the plan's own.
type Models struct {
	Build  string
	Review string
	Plan   []string
}

// LastResort is a plan's free models, in the order a run falls through them
// once every paid ceiling refuses it.
type LastResort struct {
	Models []string
	// MeasuredOn is the yyyy-mm-dd date the order was measured, or unmeasuredOn.
	MeasuredOn string
}

const unmeasuredOn = "unmeasured"

// freeOnly reports whether the plan serves nothing but its free tier: no
// windows or caps, and every model it prices on the last-resort list.
func (p Provider) freeOnly() bool {
	if len(p.LastResort.Models) == 0 || len(p.Windows) > 0 || len(p.ModelCaps) > 0 {
		return false
	}
	for model := range p.Rates {
		if !slices.Contains(p.LastResort.Models, model) {
			return false
		}
	}
	return true
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
	// LastResortPlans are the plans' free tiers, named in the order the
	// dispatcher's free-tier fallback walks them. Empty for a configuration
	// that names none, which has no free tier to fall to.
	LastResortPlans []string
	// PaidPlans are the plans a ticket may start on, in the order the
	// dispatcher walks them as each one's own ceilings bind. Empty for a
	// configuration whose one paid plan is the default model's.
	PaidPlans []string
}

// configDoc is configJSON's own shape; the parsed Config is the converted one.
type configDoc struct {
	CashLimitUSD        float64                `json:"cash_limit_usd"`
	RunnerFreeMinutes   int64                  `json:"runner_free_minutes"`
	LastResortPlans     []string               `json:"last_resort_plans"`
	PaidPlans           []string               `json:"paid_plans"`
	AllowanceMarkers    []string               `json:"allowance_markers"`
	AvailabilityMarkers []string               `json:"availability_markers"`
	Providers           map[string]providerDoc `json:"providers"`
}

type providerDoc struct {
	BaseURL    string              `json:"base_url"`
	KeySecret  string              `json:"key_secret"`
	Windows    []windowDoc         `json:"windows"`
	ModelCaps  map[string]float64  `json:"model_caps"`
	OutputCaps map[string]int      `json:"output_caps"`
	Harnesses  harnessDoc          `json:"harnesses"`
	Rates      map[string]ratesDoc `json:"rates"`
	LastResort *lastResortDoc      `json:"last_resort"`
	Models     *modelsDoc          `json:"models"`
}

type modelsDoc struct {
	Build  string   `json:"build"`
	Review string   `json:"review"`
	Plan   []string `json:"plan"`
}

type lastResortDoc struct {
	Models     []string `json:"models"`
	MeasuredOn string   `json:"measured_on"`
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
		LastResortPlans:     doc.LastResortPlans,
		PaidPlans:           doc.PaidPlans,
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
		if len(p.OutputCaps) > 0 {
			provider.OutputCaps = make(map[string]int, len(p.OutputCaps))
			for model, maxTokens := range p.OutputCaps {
				provider.OutputCaps[model] = maxTokens
			}
		}
		if len(p.Rates) > 0 {
			provider.Rates = make(map[string]Rates, len(p.Rates))
			for model, r := range p.Rates {
				provider.Rates[model] = r.rates()
			}
		}
		if p.LastResort != nil {
			provider.LastResort = LastResort{Models: p.LastResort.Models, MeasuredOn: p.LastResort.MeasuredOn}
			if provider.LastResort.Models == nil {
				provider.LastResort.Models = []string{}
			}
		}
		if p.Models != nil {
			provider.Models = Models{Build: p.Models.Build, Review: p.Models.Review, Plan: p.Models.Plan}
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
	paid := false
	for name, p := range c.Providers {
		paid = paid || !p.freeOnly()
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
		if err := checkLastResort(name, p); err != nil {
			return err
		}
		for model, r := range p.Rates {
			free := slices.Contains(p.LastResort.Models, model)
			switch {
			case !isPlanModel(name, model):
				return fmt.Errorf("provider %s has rates for %q, not a %s model", name, model, name)
			case min(r.Input, r.Output, r.CacheRead, r.CacheWrite) < 0:
				return fmt.Errorf("provider %s model %s has a negative rate", name, model)
			case free && (r.Peak != nil || max(r.Input, r.Output, r.CacheRead, r.CacheWrite) > 0):
				return fmt.Errorf("provider %s last-resort model %s is not priced $0", name, model)
			case !free && max(r.Input, r.Output, r.CacheRead, r.CacheWrite) == 0:
				return fmt.Errorf("provider %s model %s has no rate", name, model)
			}
			if err := checkPeak(r.Peak); err != nil {
				return fmt.Errorf("provider %s model %s peak: %w", name, model, err)
			}
		}
	}
	if !paid {
		return errors.New("every provider is free-only")
	}
	seen := map[string]bool{}
	for _, plan := range c.LastResortPlans {
		switch {
		case plan == "":
			return errors.New("the last-resort order names an empty plan")
		case seen[plan]:
			return fmt.Errorf("the last-resort order names %s twice", plan)
		}
		seen[plan] = true
		p, ok := c.Providers[plan]
		if !ok {
			return fmt.Errorf("the last-resort order names %s, which is not configured", plan)
		}
		if len(p.LastResort.Models) == 0 {
			return fmt.Errorf("the last-resort order names %s, which has no free tier", plan)
		}
	}
	return c.checkPaidPlans()
}

// checkPaidPlans reports whether the paid-plan order is usable: each plan named
// once, configured, paid, and carrying the models a run moved onto it starts on.
func (c Config) checkPaidPlans() error {
	seen := map[string]bool{}
	for _, plan := range c.PaidPlans {
		switch {
		case plan == "":
			return errors.New("the paid-plan order names an empty plan")
		case seen[plan]:
			return fmt.Errorf("the paid-plan order names %s twice", plan)
		}
		seen[plan] = true
		p, ok := c.Providers[plan]
		switch {
		case !ok:
			return fmt.Errorf("the paid-plan order names %s, which is not configured", plan)
		case p.freeOnly():
			return fmt.Errorf("the paid-plan order names %s, which is free-only", plan)
		}
		if err := checkPaidModels(plan, p.Models); err != nil {
			return err
		}
	}
	return nil
}

// checkPaidModels reports whether a paid plan's models are usable: a build model, a
// different review model and one or more distinct plan models, all the plan's own.
func checkPaidModels(provider string, m Models) error {
	if m.Build == "" || m.Review == "" || len(m.Plan) == 0 {
		return fmt.Errorf("paid plan %s needs a build model, a review model and a plan model", provider)
	}
	if m.Build == m.Review {
		return fmt.Errorf("paid plan %s reviews with its build model %s", provider, m.Build)
	}
	seen := map[string]bool{}
	for _, model := range append([]string{m.Build, m.Review}, m.Plan...) {
		if !isPlanModel(provider, model) {
			return fmt.Errorf("paid plan %s has a model %q, not a %s model", provider, model, provider)
		}
	}
	for _, model := range m.Plan {
		if seen[model] {
			return fmt.Errorf("paid plan %s lists plan model %s twice", provider, model)
		}
		seen[model] = true
	}
	return nil
}

// checkLastResort reports whether a plan's free tier is usable: when present,
// one or more distinct models of the plan's own, each priced $0 and none
// carrying a model cap, and a measured date or unmeasuredOn.
func checkLastResort(provider string, p Provider) error {
	lr := p.LastResort
	if lr.Models == nil {
		return nil
	}
	if len(lr.Models) == 0 {
		return fmt.Errorf("provider %s has a last resort with no models", provider)
	}
	if _, err := time.Parse(dateLayout, lr.MeasuredOn); err != nil && lr.MeasuredOn != unmeasuredOn {
		return fmt.Errorf("provider %s last resort measured_on %q is not a yyyy-mm-dd date or %q", provider, lr.MeasuredOn, unmeasuredOn)
	}
	seen := map[string]bool{}
	for _, model := range lr.Models {
		_, capped := p.ModelCaps[model]
		_, priced := p.Rates[model]
		switch {
		case !isPlanModel(provider, model):
			return fmt.Errorf("provider %s has a last-resort model %q, not a %s model", provider, model, provider)
		case seen[model]:
			return fmt.Errorf("provider %s lists last-resort model %s twice", provider, model)
		case capped:
			return fmt.Errorf("provider %s last-resort model %s has a model cap", provider, model)
		case !priced:
			return fmt.Errorf("provider %s last-resort model %s is not priced $0", provider, model)
		}
		seen[model] = true
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

// OutputCaps are a provider's own per-model output-token ceilings, keyed by the
// full model id a run names; empty for a provider configured without any, whose
// harnesses are then asked for their own default.
func OutputCaps(provider string) map[string]int {
	caps := embeddedConfig.Providers[provider].OutputCaps
	if len(caps) == 0 {
		return nil
	}
	out := make(map[string]int, len(caps))
	for model, maxTokens := range caps {
		out[model] = maxTokens
	}
	return out
}

// LastResortModels are a plan's free models in fall-through order, empty for a
// plan with no free tier.
func LastResortModels(plan string) []string {
	return slices.Clone(embeddedConfig.Providers[plan].LastResort.Models)
}

// LastResortPlans are the plans' free tiers named in the order the free-tier
// fallback walks them, empty for a configuration that names none.
func LastResortPlans() []string {
	return slices.Clone(embeddedConfig.LastResortPlans)
}

// PaidPlans are the plans a ticket may start on, in the order the dispatcher
// walks them, empty for a configuration that names none.
func PaidPlans() []string {
	return slices.Clone(embeddedConfig.PaidPlans)
}

// DispatchOrder is every plan a run may walk, in the order the dispatcher
// reaches them: the paid plans a ticket may start on, in configuration order,
// then the free tiers of the last-resort fallback, each plan named once.
func DispatchOrder() []string {
	order := PaidPlans()
	seen := make(map[string]bool, len(order))
	for _, plan := range order {
		seen[plan] = true
	}
	for _, plan := range LastResortPlans() {
		if seen[plan] {
			continue
		}
		seen[plan] = true
		order = append(order, plan)
	}
	return order
}

// PlanModels are the models a run moved onto plan starts on, empty for a plan
// that is not configured.
func PlanModels(plan string) Models {
	m := embeddedConfig.Providers[plan].Models
	m.Plan = slices.Clone(m.Plan)
	return m
}

// RunModels are the models a run may use, each named once: every configured
// plan in name order, and within a plan its build, review and plan models, then
// its free-tier models.
func RunModels() []string {
	var models []string
	seen := make(map[string]bool)
	add := func(model string) {
		if model == "" || seen[model] {
			return
		}
		seen[model] = true
		models = append(models, model)
	}
	for _, plan := range Plans() {
		p := embeddedConfig.Providers[plan]
		add(p.Models.Build)
		add(p.Models.Review)
		for _, model := range p.Models.Plan {
			add(model)
		}
		for _, model := range p.LastResort.Models {
			add(model)
		}
	}
	return models
}

// Plans are every configured plan's name, sorted, so a caller can iterate the
// configuration rather than picking one by name.
func Plans() []string {
	plans := make([]string, 0, len(embeddedConfig.Providers))
	for name := range embeddedConfig.Providers {
		plans = append(plans, name)
	}
	slices.Sort(plans)
	return plans
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
