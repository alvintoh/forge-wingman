// Package providers holds the owner's record of each provider's plan: what it
// costs, how it behaves at its limit, and whether the terms allow unattended
// agent use.
package providers

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alvintoh/forge-wingman/internal/money"
)

// Verdict is the owner's reading of whether a plan's terms allow unattended
// agent use. Unknown is never stored on a plan: it is what a run records when
// the lookup itself failed.
type Verdict string

const (
	VerdictAllowed     Verdict = "allowed"
	VerdictRestricted  Verdict = "restricted"
	VerdictUnconfirmed Verdict = "unconfirmed"
	VerdictUnknown     Verdict = "unknown"
)

// LimitBehaviour is what a plan does once its allowance runs out.
type LimitBehaviour string

const (
	LimitHardStop     LimitBehaviour = "hard-stop"
	LimitCanSpendPast LimitBehaviour = "can-spend-past"
	LimitUnknown      LimitBehaviour = "unknown"
)

// Billing is how a provider's plan charges for the models it serves.
type Billing string

const (
	BillingFree      Billing = "free"
	BillingAllowance Billing = "allowance"
	BillingPerToken  Billing = "per-token"
)

const (
	maxNameBytes    = 100
	maxWordingBytes = 2000
	maxReplyBytes   = 4000
	maxURLBytes     = 500
	dateLayout      = "2006-01-02"
)

// Locator names a vendor page the weekly refresh reads, and the region of it
// to read.
type Locator struct {
	URL      string `firestore:"url"`
	Selector string `firestore:"selector"`
}

// HarnessPair is a harness the plan is offered through and the model it serves.
type HarnessPair struct {
	Harness string `firestore:"harness"`
	Model   string `firestore:"model"`
}

// Definition is what the owner enters about a plan.
type Definition struct {
	Name           string         `firestore:"name"`
	MonthlyPrice   money.Micros   `firestore:"monthly_price_micros"`
	Pages          []Locator      `firestore:"pages"`
	Harnesses      []HarnessPair  `firestore:"harnesses"`
	LimitBehaviour LimitBehaviour `firestore:"limit_behaviour"`
	// Billing is empty on a plan recorded before billing was, which reads as unconfigured.
	Billing Billing `firestore:"billing"`
}

// Reply is a vendor support reply the owner recorded about a plan's terms.
type Reply struct {
	Text string    `firestore:"text"`
	At   time.Time `firestore:"at"`
}

// Plan is one provider_plans/<provider> document.
type Plan struct {
	Definition Definition `firestore:"definition"`
	Verdict    Verdict    `firestore:"verdict"`
	// Wording, Source and ReadOn are the vendor words, page and yyyy-mm-dd date the verdict rests on.
	Wording string  `firestore:"verdict_wording"`
	Source  string  `firestore:"verdict_source"`
	ReadOn  string  `firestore:"verdict_read_on"`
	Replies []Reply `firestore:"replies"`
	// OptedIn is the owner's consent to a per-token provider's spend, written only by plan-optin.
	OptedIn bool `firestore:"opted_in"`
}

// Configured reports whether the owner has recorded how the plan bills.
func (p Plan) Configured() bool { return p.Definition.Billing != "" }

// NeedsOptIn reports whether runs on the plan spend per token, so need the owner's opt-in first.
func (p Plan) NeedsOptIn() bool { return p.Definition.Billing == BillingPerToken }

// VerdictNote is a verdict with the evidence it rests on.
type VerdictNote struct {
	Verdict Verdict
	Wording string
	Source  string
	ReadOn  string
}

// ParseVerdict reads a verdict the owner may record.
func ParseVerdict(s string) (Verdict, error) {
	switch v := Verdict(s); v {
	case VerdictAllowed, VerdictRestricted, VerdictUnconfirmed:
		return v, nil
	case VerdictUnknown:
	}
	return "", fmt.Errorf("verdict %q is not allowed, restricted or unconfirmed", s)
}

// ParseLimitBehaviour reads a plan's behaviour at its limit.
func ParseLimitBehaviour(s string) (LimitBehaviour, error) {
	switch b := LimitBehaviour(s); b {
	case LimitHardStop, LimitCanSpendPast, LimitUnknown:
		return b, nil
	}
	return "", fmt.Errorf("limit behaviour %q is not hard-stop, can-spend-past or unknown", s)
}

// ParseBilling reads how a plan bills.
func ParseBilling(s string) (Billing, error) {
	switch b := Billing(s); b {
	case BillingFree, BillingAllowance, BillingPerToken:
		return b, nil
	}
	return "", fmt.Errorf("billing %q is not free, allowance or per-token", s)
}

// NewDefinition validates a plan definition entered as text. A page is a URL
// optionally followed by the selector of the region to read; a harness is
// harness:model.
func NewDefinition(name, priceUSD string, pages, harnesses []string, limit, billing string) (Definition, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Definition{}, errors.New("plan name is empty")
	}
	price, err := strconv.ParseFloat(strings.TrimSpace(priceUSD), 64)
	if err != nil || math.IsInf(price, 0) {
		return Definition{}, fmt.Errorf("monthly price %q is not a positive amount", priceUSD)
	}
	kind, err := ParseBilling(billing)
	if err != nil {
		return Definition{}, err
	}
	switch {
	case kind == BillingFree && !(price >= 0):
		return Definition{}, fmt.Errorf("monthly price %q is not zero or more", priceUSD)
	case kind != BillingFree && !(price > 0):
		return Definition{}, fmt.Errorf("monthly price %q is not a positive amount", priceUSD)
	}
	behaviour, err := ParseLimitBehaviour(limit)
	if err != nil {
		return Definition{}, err
	}
	def := Definition{
		Name:           capText(name, maxNameBytes),
		MonthlyPrice:   money.FromUSD(price),
		Pages:          []Locator{},
		Harnesses:      []HarnessPair{},
		LimitBehaviour: behaviour,
		Billing:        kind,
	}
	for _, p := range pages {
		raw, selector, _ := strings.Cut(strings.TrimSpace(p), " ")
		if err := checkURL(raw); err != nil {
			return Definition{}, fmt.Errorf("page %q: %w", p, err)
		}
		def.Pages = append(def.Pages, Locator{URL: raw, Selector: strings.TrimSpace(selector)})
	}
	for _, h := range harnesses {
		harness, model, ok := strings.Cut(strings.TrimSpace(h), ":")
		if !ok || harness == "" || model == "" {
			return Definition{}, fmt.Errorf("harness %q is not harness:model", h)
		}
		def.Harnesses = append(def.Harnesses, HarnessPair{Harness: harness, Model: model})
	}
	return def, nil
}

// NewVerdictNote validates a verdict and its evidence. The wording is capped on
// a character boundary; a source URL or read date that is malformed is refused
// rather than altered.
func NewVerdictNote(verdict, wording, source, readOn string) (VerdictNote, error) {
	v, err := ParseVerdict(verdict)
	if err != nil {
		return VerdictNote{}, err
	}
	if err := checkURL(source); err != nil {
		return VerdictNote{}, fmt.Errorf("source: %w", err)
	}
	if _, err := time.Parse(dateLayout, readOn); err != nil {
		return VerdictNote{}, fmt.Errorf("read-on %q is not a yyyy-mm-dd date", readOn)
	}
	return VerdictNote{Verdict: v, Wording: capText(wording, maxWordingBytes), Source: source, ReadOn: readOn}, nil
}

// NewReply validates a recorded support reply.
func NewReply(text string, at time.Time) (Reply, error) {
	if strings.TrimSpace(text) == "" {
		return Reply{}, errors.New("reply text is empty")
	}
	return Reply{Text: capText(text, maxReplyBytes), At: at}, nil
}

func checkURL(raw string) error {
	if len(raw) > maxURLBytes {
		return fmt.Errorf("URL is longer than %d bytes", maxURLBytes)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("not an https URL")
	}
	return nil
}

// capText caps s at n bytes on a character boundary, replacing invalid UTF-8,
// since Firestore rejects a document holding a string that is not valid UTF-8.
func capText(s string, n int) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
