package providers

import (
	"strings"

	"github.com/alvintoh/forge-wingman/internal/money"
)

// ReadState is how one read of a source ended. A source that could not be
// reached is CouldNotRead and never Unchanged, so a failed read can never be
// recorded as a quiet week.
type ReadState string

const (
	// ReadChanged is a source read, and differing from the facts before it.
	ReadChanged ReadState = "changed"
	// ReadUnchanged is a source read, and the same as the facts before it.
	ReadUnchanged ReadState = "unchanged"
	// ReadCouldNotRead is a source this read could not reach: the figures it last
	// answered with stand, and LastGoodReadOn says when they were read.
	ReadCouldNotRead ReadState = "could-not-read"
)

// Source is one source's outcome in a facts document, which every source a
// refresh reads records.
type Source struct {
	State ReadState `firestore:"state"`
	// LastGoodReadOn is the yyyy-mm-dd date the source was last read
	// successfully: this read's date when it succeeded, and the date carried
	// forward from the facts before it when it did not.
	LastGoodReadOn string `firestore:"last_good_read_on"`
	// Changes names what this read found different from the facts before it, empty
	// when nothing did or the source could not be read.
	Changes []string `firestore:"changes"`
	// Note is why the source could not be read, empty otherwise.
	Note string `firestore:"note"`
}

// Provenance is who ran a model to produce a leaderboard figure.
type Provenance string

const (
	// ProvenanceIndependently is a figure the leaderboard ran itself.
	ProvenanceIndependently Provenance = "independently-run"
	// ProvenanceVendorReported is a figure the model's own vendor reported.
	ProvenanceVendorReported Provenance = "vendor-reported"
	// ProvenanceUnknown is a figure whose provenance the leaderboard did not state.
	ProvenanceUnknown Provenance = "unknown"
)

// ParseProvenance reads the word a leaderboard states for who ran a model. A word
// this build has not seen is unknown rather than an error, so a value the
// leaderboard adds is recorded rather than refusing the whole read.
func ParseProvenance(word string) Provenance {
	switch strings.ToUpper(strings.TrimSpace(word)) {
	case "INDEPENDENT", "INDEPENDENTLY", "INDEPENDENTLY-RUN", "INDEPENDENTLY_RUN":
		return ProvenanceIndependently
	case "VENDOR", "VENDOR-REPORTED", "VENDOR_REPORTED":
		return ProvenanceVendorReported
	}
	return ProvenanceUnknown
}

// Score is the Coding leaderboard's reading of one model: its figure, the
// benchmark the figure is on, the date the leaderboard states for it, who ran it
// and the margin the leaderboard states on it.
type Score struct {
	// Listed reports the leaderboard carried the model at all; the fields below
	// are the leaderboard's own and are absent when it did not.
	Listed     bool       `firestore:"listed"`
	Value      float64    `firestore:"value"`
	Benchmark  string     `firestore:"benchmark"`
	ScoredOn   string     `firestore:"scored_on"`
	Provenance Provenance `firestore:"provenance"`
	Margin     string     `firestore:"margin"`
}

// ModelFacts is one model of the vendor's own list, and what each source says
// about it. Model is the id the vendor's own models endpoint states.
type ModelFacts struct {
	Model string `firestore:"model"`
	// Feed reports the free feed lists it, a change signal only.
	Feed bool `firestore:"feed"`
	// Leaderboard is the Coding leaderboard's reading of it.
	Leaderboard Score `firestore:"leaderboard"`
}

// MaxPageBytes caps a page's stored text, so one long vendor page cannot carry a
// facts document past what the store accepts.
const MaxPageBytes = 8 * 1024

// PageFacts is one vendor page as read, with the region of it the plan's locator
// names. Text is the vendor's own words as read, quoted and capped: it is data
// for the owner to read, never an instruction to act on.
type PageFacts struct {
	Source   Source `firestore:"source"`
	URL      string `firestore:"url"`
	Selector string `firestore:"selector"`
	Text     string `firestore:"text"`
}

// NewPageFacts returns one vendor page's read with its text capped on a character
// boundary, since a vendor's own words may be any length.
func NewPageFacts(url, selector, text string) PageFacts {
	return PageFacts{URL: url, Selector: selector, Text: CapText(text, MaxPageBytes)}
}

// ModelsFacts is the vendor's own model list, as its models endpoint answered it,
// each model carrying the leaderboard's reading of it.
type ModelsFacts struct {
	Source Source       `firestore:"source"`
	Models []ModelFacts `firestore:"models"`
}

// FeedFacts is the free feed's own report of the plan, a change signal only: the
// figures it states are never the ones recorded, and Disagreements names where
// they differ from the recorded plan's.
type FeedFacts struct {
	Source         Source         `firestore:"source"`
	Price          money.Micros   `firestore:"price_micros"`
	LimitBehaviour LimitBehaviour `firestore:"limit_behaviour"`
	// Disagreements names where the feed's own figures differ from the recorded
	// plan's, which are the ones this document records.
	Disagreements []string `firestore:"disagreements"`
	// Models are the models the feed lists for the plan, a change signal only.
	Models []string `firestore:"models"`
	// Perks are what the feed lists the plan as including, as it states them.
	Perks []string `firestore:"perks"`
}

// Facts is one dated read of a plan's sources: the vendor's own pages, quoted,
// the vendor's own model list, the Coding leaderboard's reading of each model,
// and the free feed's report. It is written to
// provider_plans/<provider>/facts/<date>, one document per read date.
//
// Price and Limits are the figures in the recorded plan, which the owner took from
// the vendor's own page; a refresh copies them from the plan and never reads them
// off a page. The free feed is a change signal and never the figure recorded here.
//
// The document records what was read and decides nothing, so
// nothing in it changes which provider runs.
type Facts struct {
	// ReadOn is the yyyy-mm-dd date the sources were read, which is also the
	// document's own id.
	ReadOn string `firestore:"read_on"`
	// Price is the plan's monthly price, as the recorded plan states it.
	Price money.Micros `firestore:"price_micros"`
	// Limits is what the plan does once its allowance runs out, as the recorded
	// plan states it.
	Limits LimitBehaviour `firestore:"limit_behaviour"`
	// Pages is one entry per page the plan's definition names, in its own order.
	Pages []PageFacts `firestore:"pages"`
	// Models is the vendor's own model list, each model with the leaderboard's
	// reading of it.
	Models ModelsFacts `firestore:"models"`
	// Leaderboard is the leaderboard's own read: whether it answered at all, and
	// when it last did.
	Leaderboard Source `firestore:"leaderboard"`
	// Feed is the free feed's own read of the plan.
	Feed FeedFacts `firestore:"free_feed"`
	// Flags is what about the plan the owner should look at, as plan-list shows it.
	Flags []Flag `firestore:"flags"`
}

// Page returns the facts of the region of the page at url that selector names, ok
// false when this document recorded none, so two regions of one page stay apart.
func (f Facts) Page(url, selector string) (PageFacts, bool) {
	for _, p := range f.Pages {
		if p.URL == url && p.Selector == selector {
			return p, true
		}
	}
	return PageFacts{}, false
}

// Model returns the facts of one model, ok false when this document recorded none.
func (f Facts) Model(model string) (ModelFacts, bool) {
	for _, m := range f.Models.Models {
		if m.Model == model {
			return m, true
		}
	}
	return ModelFacts{}, false
}
