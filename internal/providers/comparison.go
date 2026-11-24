package providers

import (
	"encoding/json"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/money"
)

const (
	fiveHourPeriod = 5 * time.Hour
	weeklyPeriod   = 7 * 24 * time.Hour
	// picoPerUSD is the scale a request's cost is compared with a ceiling at: a
	// rate with up to six decimals times a whole token count is a whole number
	// of picodollars, so the division below is exact where a float quotient is
	// not (0.3 / 0.1 is 2.9999999999999996).
	picoPerUSD = 1e12
	// picoPerMicro converts a Micros ceiling to the same scale.
	picoPerMicro = 1_000_000
)

// Field is one comparison value. Its zero value is unconfirmed, so a value
// nothing recorded can never read as a default.
type Field[T any] struct {
	Value T
	Known bool
}

// MarshalJSON writes a confirmed field as its value and an unconfirmed one as
// the word "unconfirmed", never as the zero value it holds.
func (f Field[T]) MarshalJSON() ([]byte, error) {
	if !f.Known {
		return json.Marshal(string(VerdictUnconfirmed))
	}
	return json.Marshal(f.Value)
}

// Known returns a confirmed field holding v.
func Known[T any](v T) Field[T] { return Field[T]{Value: v, Known: true} }

// HarnessCapabilities is what one harness does on one plan, each fact
// unconfirmed until the harness's adapter records it.
type HarnessCapabilities struct {
	Headless         Field[bool]
	StructuredOutput Field[bool]
	// DeniesPlanningEdits is whether a planning run refuses file edits and shell
	// commands.
	DeniesPlanningEdits Field[bool]
}

// Request is the shape of one request a ceiling is converted to: the model and
// the tokens it sends fresh, reads from cache and writes out. The zero Request
// converts nothing.
type Request struct {
	Model  string
	Fresh  int64
	Cached int64
	Output int64
	// Approximate is set by the caller when any token count is a published
	// approximation rather than one measured from recorded runs. Nothing in the
	// configuration marks a rate card as approximate, so a card never does.
	Approximate bool
}

// Mark says how firm a derived figure is.
type Mark string

const (
	// MarkExact is a figure computed from the plan's own rate card and token
	// counts that are not published approximations.
	MarkExact Mark = "exact"
	// MarkEstimated is a figure with at least one published-approximation input.
	MarkEstimated Mark = "estimated"
)

// RequestsPerDollar is how many whole requests of Shape one dollar buys, with
// the shape beside the figure so it is read against the request it was computed
// for.
type RequestsPerDollar struct {
	RequestCounts
	Shape Request
	Mark  Mark
}

// ComparisonInput is what ComparePlans reads beside the configuration.
type ComparisonInput struct {
	// Plans are the owner's plan records by provider; a provider with none has
	// no confirmed price or verdict.
	Plans map[string]Plan
	// Facts are each provider's latest refreshed facts; a provider with none has
	// no strongest model.
	Facts map[string]Facts
	// Harness reports what a harness does on a plan; nil confirms nothing.
	Harness func(harness, plan string) HarnessCapabilities
	// Request is the shape ceilings in dollars are also expressed in.
	Request Request
}

// RequestCounts is how many whole requests of the Request shape a ceiling
// buys, at the model's off-peak rates and, where it prices by the hour, at peak.
type RequestCounts struct {
	OffPeak int64
	HasPeak bool
	Peak    int64
}

// Ceiling is one of a plan's allowance windows as the vendor published it, in
// dollars. Requests is nil when the ceiling cannot be converted: the model is
// not on the plan's rate card, or a request of that shape costs nothing.
type Ceiling struct {
	Name     string
	Limit    money.Micros
	Requests *RequestCounts
}

// StrongestModel is the plan's highest-scored model on the Coding leaderboard
// the refresh read, with the score as the leaderboard stated it.
type StrongestModel struct {
	Model string
	Score Score
}

// HarnessComparison is one plan-and-harness pair.
type HarnessComparison struct {
	Harness             string
	Headless            Field[bool]
	StructuredOutput    Field[bool]
	DeniesPlanningEdits Field[bool]
}

// PeakPrice is a model's price in force inside its peak window: weekdays, UTC,
// in each of Hours.
type PeakPrice struct {
	Multiplier float64
	Hours      []HourRange
	Input      float64
	Output     float64
	CacheRead  float64
}

// ContextPrice is a model's price for a request whose context is over
// ContextTokens.
type ContextPrice struct {
	ContextTokens int64
	Input         float64
	Output        float64
	CacheRead     float64
}

// ModelPrice is a model's rates in US dollars per million tokens: uncached
// input, output and cache read, with the peak and long-context prices where it
// has them.
type ModelPrice struct {
	Model     string
	Input     float64
	Output    float64
	CacheRead float64
	Peak      *PeakPrice
	Above     *ContextPrice
}

// PlanComparison is one plan's comparison fields, every value either confirmed
// or unconfirmed.
type PlanComparison struct {
	Plan         string
	MonthlyPrice Field[money.Micros]
	// RequestsPerDollar is unconfirmed when the plan has no rate card for the
	// stated model or a request of the stated shape costs nothing.
	RequestsPerDollar Field[RequestsPerDollar]
	FiveHour          Field[Ceiling]
	Weekly            Field[Ceiling]
	Monthly           Field[Ceiling]
	Strongest         Field[StrongestModel]
	// StrongestCap is the monthly cap the configuration sets on the strongest
	// model; unconfirmed when there is no strongest model or no cap for it.
	StrongestCap Field[money.Micros]
	// Features are what the configuration shows the plan has.
	Features []string
	// FeedPerks are what the free feed lists the plan as including, as it states
	// them: confirmed only when the feed's last read succeeded.
	FeedPerks Field[[]string]
	// UnshownWindows are the names of configured windows no ceiling field shows:
	// one of a period other than 5 hours, a week or a month, or a repeat of a
	// period another window already fills.
	UnshownWindows []string
	Harnesses      []HarnessComparison
	Verdict        Field[Verdict]
	Models         []ModelPrice
}

// ComparePlans builds every configured plan's comparison fields: paid plans in
// the owner's order, then the free tiers', then any other plan by name.
func ComparePlans(in ComparisonInput) []PlanComparison { return comparePlans(embeddedConfig, in) }

func comparePlans(cfg Config, in ComparisonInput) []PlanComparison {
	var order []string
	for _, plan := range slices.Concat(cfg.PaidPlans, cfg.LastResortPlans, slices.Sorted(maps.Keys(cfg.Providers))) {
		if !slices.Contains(order, plan) {
			order = append(order, plan)
		}
	}
	out := make([]PlanComparison, 0, len(order))
	for _, plan := range order {
		out = append(out, comparePlan(plan, cfg.Providers[plan], in))
	}
	return out
}

func comparePlan(plan string, p Provider, in ComparisonInput) PlanComparison {
	facts := in.Facts[plan]
	c := PlanComparison{Plan: plan, Features: features(p)}
	if facts.Feed.Source.ReadOK() {
		c.FeedPerks = Known(append([]string{}, facts.Feed.Perks...))
	}
	if rec, ok := in.Plans[plan]; ok {
		c.MonthlyPrice = Known(rec.Definition.MonthlyPrice)
		if v := rec.RecordedVerdict(); v == VerdictAllowed || v == VerdictRestricted {
			c.Verdict = Known(v)
		}
	}
	card := p.Rates[in.Request.Model]
	c.RequestsPerDollar = requestsPerDollar(card, in.Request)
	for _, w := range p.Windows {
		field := c.ceilingFor(w.Period)
		if field == nil || field.Known {
			c.UnshownWindows = append(c.UnshownWindows, w.Name)
			continue
		}
		*field = Known(Ceiling{Name: w.Name, Limit: w.Limit, Requests: requestCounts(w.Limit, card, in.Request)})
	}
	if facts.Leaderboard.ReadOK() {
		c.Strongest = strongest(facts.Models.Models)
	}
	if c.Strongest.Known {
		if limit, ok := p.ModelCaps[plan+"/"+c.Strongest.Value.Model]; ok {
			c.StrongestCap = Known(limit)
		}
	}
	for _, h := range harnessOrder(p.Harnesses) {
		pair := HarnessComparison{Harness: h}
		if in.Harness != nil {
			caps := in.Harness(h, plan)
			pair.Headless, pair.StructuredOutput, pair.DeniesPlanningEdits = caps.Headless, caps.StructuredOutput, caps.DeniesPlanningEdits
		}
		c.Harnesses = append(c.Harnesses, pair)
	}
	for _, model := range slices.Sorted(maps.Keys(p.Rates)) {
		c.Models = append(c.Models, modelPrice(model, p.Rates[model]))
	}
	return c
}

// ceilingFor is the field a window of period fills, nil for a period the
// comparison does not show.
func (c *PlanComparison) ceilingFor(period time.Duration) *Field[Ceiling] {
	if period == fiveHourPeriod {
		return &c.FiveHour
	}
	if period == weeklyPeriod {
		return &c.Weekly
	}
	if period == monthlyPeriod {
		return &c.Monthly
	}
	return nil
}

func harnessOrder(o HarnessOrder) []string { return append([]string{o.Default}, o.Fallbacks...) }

// strongest is the listed model with the highest leaderboard score, the
// lexicographically first on a tie so the answer does not depend on list order.
func strongest(models []ModelFacts) Field[StrongestModel] {
	var best Field[StrongestModel]
	for _, m := range models {
		if !m.Leaderboard.Listed {
			continue
		}
		s := m.Leaderboard.Value
		if !best.Known || s > best.Value.Score.Value || (s == best.Value.Score.Value && m.Model < best.Value.Model) {
			best = Known(StrongestModel{Model: m.Model, Score: m.Leaderboard})
		}
	}
	return best
}

// requestCounts is how many requests of req's shape limit buys on card, nil
// when a request costs nothing (a model the plan does not price has a zero
// card) and the ceiling cannot be converted. Both counts are floored: a
// fraction of a request is not one.
func requestCounts(limit money.Micros, card Rates, req Request) *RequestCounts {
	ctxTokens := req.Fresh + req.Cached
	offPeak := requestsWithin(limit, card.ForContext(ctxTokens), req)
	if offPeak < 0 {
		return nil
	}
	counts := &RequestCounts{OffPeak: offPeak}
	if card.Peak != nil {
		counts.HasPeak = true
		counts.Peak = requestsWithin(limit, peakRates(card.ForContext(ctxTokens)), req)
	}
	return counts
}

// requestsPerDollar is the whole requests of req's shape one dollar buys on
// card, unconfirmed when a request costs nothing: no figure is better than a
// default, and a free card has no finite one.
func requestsPerDollar(card Rates, req Request) Field[RequestsPerDollar] {
	counts := requestCounts(money.Dollar, card, req)
	if counts == nil {
		return Field[RequestsPerDollar]{}
	}
	mark := MarkExact
	if req.Approximate {
		mark = MarkEstimated
	}
	return Known(RequestsPerDollar{RequestCounts: *counts, Shape: req, Mark: mark})
}

// peakRates is r with its peak surcharge applied: Rates.At prices a zero time at
// peak, so the comparison states the peak figure without picking an hour.
func peakRates(r Rates) Rates { return r.At(time.Time{}) }

// requestsWithin floors limit over the cost of one request at r, or -1 when
// that cost is not positive.
func requestsWithin(limit money.Micros, r Rates, req Request) int64 {
	cost := int64(math.Round(r.CostUSD(req.Fresh, req.Output, req.Cached, 0) * picoPerUSD))
	if cost <= 0 {
		return -1
	}
	return int64(limit) * picoPerMicro / cost
}

func modelPrice(model string, r Rates) ModelPrice {
	price := ModelPrice{Model: model, Input: r.Input, Output: r.Output, CacheRead: r.CacheRead}
	if r.Peak != nil {
		at := peakRates(r)
		price.Peak = &PeakPrice{Multiplier: r.Peak.Multiplier, Hours: slices.Clone(r.Peak.Hours), Input: at.Input, Output: at.Output, CacheRead: at.CacheRead}
	}
	if a := r.Above; a != nil {
		price.Above = &ContextPrice{ContextTokens: a.ContextTokens, Input: a.Input, Output: a.Output, CacheRead: a.CacheRead}
	}
	return price
}

// features lists what the configuration shows p has. Free-tier models count
// only when every one is priced at $0 on the rate card.
func features(p Provider) []string {
	var out []string
	if len(p.ModelCaps) > 0 {
		out = append(out, "Per-model dollar limits")
	}
	if freeRung(p) {
		out = append(out, "Free tier at $0")
	}
	for _, model := range slices.Sorted(maps.Keys(p.Rates)) {
		_, id, _ := strings.Cut(model, "/")
		r := p.Rates[model]
		if r.Peak != nil {
			out = append(out, "Peak-hour pricing on "+id)
		}
		if a := r.Above; a != nil {
			out = append(out, "Long-context pricing over "+strconv.FormatInt(a.ContextTokens, 10)+" tokens on "+id)
		}
	}
	return out
}

// freeRung reports whether p has a last-resort rung whose every model is
// priced at $0.
func freeRung(p Provider) bool {
	if len(p.LastResort.Models) == 0 {
		return false
	}
	for _, model := range p.LastResort.Models {
		r, ok := p.Rates[model]
		if !ok || max(r.Input, r.Output, r.CacheRead, r.CacheWrite) != 0 {
			return false
		}
	}
	return true
}
