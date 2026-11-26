package providers

import "github.com/alvintoh/forge-wingman/internal/money"

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
	// ReadNotApplicable is a source the plan has no way to read, such as a model
	// list for a plan with no API base: a fact about the plan, not an outage, so it
	// is never aged and the figures beside it are carried forward.
	ReadNotApplicable ReadState = "not-applicable"
)

// Source is one source's outcome in a facts document, which every source a
// refresh reads records.
type Source struct {
	State ReadState `firestore:"state"`
	// LastGoodReadOn is the yyyy-mm-dd date the source was last read
	// successfully: this read's date when it succeeded, and the date carried
	// forward from the facts before it when it did not.
	LastGoodReadOn string `firestore:"last_good_read_on"`
	// FirstUnreadOn is the yyyy-mm-dd date a source that has never been read well
	// was first found unreadable, carried forward until a read succeeds. It is set
	// only while LastGoodReadOn is empty, and is what a source with no good read yet
	// is aged from.
	FirstUnreadOn string `firestore:"first_unread_on"`
	// Changes names what this read found different from the facts before it, empty
	// when nothing did or the source could not be read.
	Changes []string `firestore:"changes"`
	// Note is why the source could not be read, why a model list that would have
	// been refused was accepted, or that a ranking's list was cut; empty
	// otherwise.
	Note string `firestore:"note"`
}

// ReadOK reports whether the source's last read succeeded, so the figures
// beside it are this read's and not carried forward from an earlier one. A
// document recorded no read at all has an empty state, which is not a success.
func (s Source) ReadOK() bool { return s.State == ReadChanged || s.State == ReadUnchanged }

// Provenance is who ran a model to produce a leaderboard figure.
type Provenance string

const (
	// ProvenanceIndependently is a figure the leaderboard ran itself.
	ProvenanceIndependently Provenance = "independently-run"
	// ProvenanceVendorReported is a figure the model's own vendor reported.
	ProvenanceVendorReported Provenance = "vendor-reported"
	// ProvenanceUnknown is a figure whose provenance the leaderboard did not state
	// in terms this build can read.
	ProvenanceUnknown Provenance = "unknown"
)

// Score is the Coding leaderboard's reading of one model: its figure, the
// benchmark the figure is on, the date the leaderboard states for it, who ran it
// and the margin the leaderboard states on it.
type Score struct {
	// Listed reports the leaderboard carried the model at all; the fields below
	// are the leaderboard's own and are absent when it did not.
	Listed     bool       `firestore:"listed" json:"listed"`
	Value      float64    `firestore:"value" json:"value"`
	Benchmark  string     `firestore:"benchmark" json:"benchmark"`
	ScoredOn   string     `firestore:"scored_on" json:"scored_on"`
	Provenance Provenance `firestore:"provenance" json:"provenance"`
	Margin     string     `firestore:"margin" json:"margin"`
}

// ModelFacts is one model of the vendor's own list, and what each source says
// about it. Model is the id the vendor's own models endpoint states.
type ModelFacts struct {
	Model string `firestore:"model"`
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
	// Refused is the list last refused for dropping too many of Models, kept
	// apart from them: Models stays the last good list while a refusal stands.
	Refused RefusedList `firestore:"refused"`
}

// RefusedList is a model list a refresh refused for dropping too many models,
// and the date it was first refused. Empty when the last read was not such a
// refusal.
type RefusedList struct {
	// Models are the refused list's ids, sorted.
	Models []string `firestore:"models"`
	// FirstOn is the yyyy-mm-dd date this list was first refused.
	FirstOn string `firestore:"first_on"`
}

// RankedModel is one model's settled record of one size, as the ranking read it.
// Model is the full id a run names, the plan's own prefix included.
type RankedModel struct {
	Model   string `firestore:"model"`
	Settled int    `firestore:"settled"`
	Passed  int    `firestore:"passed"`
}

// SizeRanking is where the plan's models stand for one ticket size: Ranked are
// those clearing the bar, in the order the claim's own ranking gives, and
// BelowBar those that do not, each with its counts and none ranked.
type SizeRanking struct {
	Size     string        `firestore:"size"`
	Ranked   []RankedModel `firestore:"ranked"`
	BelowBar []RankedModel `firestore:"below_bar"`
}

// RankingFacts is the plan's models ranked from its settled runs, one entry per
// ticket size. It is a proposal for the owner to apply: nothing reads it to
// choose a model, and the escalation ladder is only ever changed by hand.
type RankingFacts struct {
	Source Source        `firestore:"source"`
	Sizes  []SizeRanking `firestore:"sizes"`
}

// Facts is one dated read of a plan's sources: the vendor's own pages, quoted,
// the vendor's own model list, the Coding leaderboard's reading of each model
// and the plan's models ranked from its settled runs. It is written to
// provider_plans/<provider>/facts/<date>, one document per read date.
//
// Price and Limits are the figures in the recorded plan, which the owner took from
// the vendor's own page; a refresh copies them from the plan and never reads them
// off a page.
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
	// Ranking is the plan's models ranked from its settled runs, per ticket size.
	Ranking RankingFacts `firestore:"ranking"`
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
