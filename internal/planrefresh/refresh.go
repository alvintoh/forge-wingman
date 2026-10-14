package planrefresh

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

// Refresher reads one plan's sources and diffs them against the facts last
// written for it. The Coding leaderboard and the free feed are one document for
// every plan, so a run reads each once and sets it here.
type Refresher struct {
	// Vendor reads the plan's own pages and its models endpoint.
	Vendor Vendor
	// Board is the leaderboard as read and Feed is the free feed as read, both
	// shared by every plan in the run; BoardErr and FeedErr are why one could not
	// be read, nil when it was.
	Board    BoardReport
	BoardErr error
	Feed     FeedReport
	FeedErr  error
	// Now is the clock the read date comes from; nil means time.Now.
	Now func() time.Time
}

// Refresh reads provider's sources and returns the facts document to record,
// dated by the refresher's clock. A source it could not read is recorded as
// could-not-read rather than failing the refresh, with its last good read date
// and the figures it last answered with carried forward.
func (r Refresher) Refresh(ctx context.Context, provider string, def providers.Definition, last providers.Facts) providers.Facts {
	on := r.now().UTC().Format(dateLayout)
	facts := providers.Facts{
		ReadOn: on,
		Price:  def.MonthlyPrice,
		Limits: def.LimitBehaviour,
		Flags:  providers.Flags(def),
	}
	facts.Pages = r.pages(ctx, def, last, on)
	facts.Models, facts.Leaderboard = r.models(ctx, provider, last, on)
	facts.Feed = r.feed(provider, def, last, on)
	return facts
}

// now is the time the read is dated from.
func (r Refresher) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

// pages reads each page the plan names, in the plan's own order, and diffs each
// against what the last document recorded for the same URL.
func (r Refresher) pages(ctx context.Context, def providers.Definition, last providers.Facts, on string) []providers.PageFacts {
	pages := make([]providers.PageFacts, 0, len(def.Pages))
	for _, loc := range def.Pages {
		before, seen := last.Page(loc.URL, loc.Selector)
		text, err := r.Vendor.Page(ctx, loc)
		if err != nil {
			pages = append(pages, carriedPage(before, loc, err, on))
			continue
		}
		fact := providers.NewPageFacts(loc.URL, loc.Selector, text)
		fact.Source = classify(on, nil, pageChanges(before, seen, fact), before.Source)
		pages = append(pages, fact)
	}
	return pages
}

// carriedPage is a page that could not be read: the text it last answered with,
// beside a could-not-read source carrying that read's date forward.
func carriedPage(before providers.PageFacts, loc providers.Locator, err error, on string) providers.PageFacts {
	carried := before
	carried.URL, carried.Selector = loc.URL, loc.Selector
	carried.Source = classify(on, err, nil, before.Source)
	return carried
}

// pageChanges names what one page's read found different from the last document's
// own reading of it.
func pageChanges(before providers.PageFacts, seen bool, now providers.PageFacts) []string {
	switch {
	case !seen:
		return []string{"read for the first time"}
	case now.Text != before.Text:
		return []string{"the page's own words changed"}
	}
	return nil
}

// models reads the vendor's own model list and the leaderboard's reading of each
// model, and diffs each against the last document. A list or a leaderboard that
// could not be read keeps what the last document recorded, beside the date it was
// read.
func (r Refresher) models(ctx context.Context, provider string, last providers.Facts, on string) (providers.ModelsFacts, providers.Source) {
	names, err := r.Vendor.Models(ctx, provider)
	if err != nil {
		names = modelNames(last.Models.Models)
	}
	feed := r.Feed.Plans[provider]
	models := make([]providers.ModelFacts, 0, len(names))
	for _, name := range names {
		models = append(models, r.model(name, feed, last))
	}
	facts := providers.ModelsFacts{Models: models}
	facts.Source = classify(on, err, modelChanges(last.Models.Models, models), last.Models.Source)
	return facts, classify(on, r.BoardErr, scoreChanges(last.Models.Models, models), last.Leaderboard)
}

// model is one model's facts: whether the feed lists it, and the leaderboard's
// reading of it. A source that could not be read keeps what the last document
// recorded for it, so a model never reads as unlisted because its source went
// quiet.
func (r Refresher) model(name string, feed FeedPlan, last providers.Facts) providers.ModelFacts {
	before, seen := last.Model(name)
	facts := providers.ModelFacts{Model: name, Feed: listsModel(feed, name)}
	if seen && r.FeedErr != nil {
		facts.Feed = before.Feed
	}
	if seen && r.BoardErr != nil {
		facts.Leaderboard = before.Leaderboard
	} else {
		facts.Leaderboard = r.Board.Models[name]
	}
	return facts
}

// listsModel reports whether the free feed lists model among the plan's own,
// matched by the name the feed states and by the same name's own last segment.
func listsModel(feed FeedPlan, model string) bool {
	short := bare(model)
	for _, m := range feed.Models {
		m = strings.TrimSpace(m)
		if m == model || bare(m) == short {
			return true
		}
	}
	return false
}

// feed is the free feed's own report of the plan, diffed against the last
// document. Nothing here supplies a figure the document records: where the feed's
// own figures differ from the recorded plan's, the recorded plan's stand and the
// difference is named.
func (r Refresher) feed(provider string, def providers.Definition, last providers.Facts, on string) providers.FeedFacts {
	if r.FeedErr != nil {
		carried := last.Feed
		carried.Source = classify(on, r.FeedErr, nil, last.Feed.Source)
		return carried
	}
	plan, listed := r.Feed.Plans[provider]
	if !listed {
		facts := providers.FeedFacts{}
		facts.Source = classify(on, nil, feedChanges(last.Feed, facts), last.Feed.Source)
		return facts
	}
	facts := providers.FeedFacts{
		Price:          plan.Price,
		LimitBehaviour: plan.LimitBehaviour,
		Disagreements:  disagreements(def, plan),
		Models:         plan.Models,
		Perks:          plan.Perks,
	}
	facts.Source = classify(on, nil, feedChanges(last.Feed, facts), last.Feed.Source)
	return facts
}

// disagreements names where the free feed's own figures differ from the recorded
// plan's, which are the figures the facts document records.
func disagreements(def providers.Definition, plan FeedPlan) []string {
	var found []string
	if plan.Price != def.MonthlyPrice {
		found = append(found, fmt.Sprintf("price: the feed states $%.2f a month, the recorded plan $%.2f",
			plan.Price.USD(), def.MonthlyPrice.USD()))
	}
	if plan.LimitBehaviour != "" && plan.LimitBehaviour != def.LimitBehaviour {
		found = append(found, fmt.Sprintf("limit: the feed states %s, the recorded plan %s",
			plan.LimitBehaviour, def.LimitBehaviour))
	}
	return found
}

// feedChanges names what the feed's own report moved: the figures it states, the
// models it lists and the perks it names.
func feedChanges(before, now providers.FeedFacts) []string {
	changes := listChanges("model", before.Models, now.Models)
	changes = append(changes, listChanges("perk", before.Perks, now.Perks)...)
	if before.Price != now.Price {
		changes = append(changes, fmt.Sprintf("price $%.2f -> $%.2f", before.Price.USD(), now.Price.USD()))
	}
	if before.LimitBehaviour != now.LimitBehaviour {
		changes = append(changes, "limit "+orNone(string(before.LimitBehaviour))+" -> "+orNone(string(now.LimitBehaviour)))
	}
	return changes
}

// modelChanges names the models a read added to and dropped from the vendor's own
// list.
func modelChanges(before, now []providers.ModelFacts) []string {
	older, newer := index(before), index(now)
	var changes []string
	for _, m := range now {
		if _, seen := older[m.Model]; !seen {
			changes = append(changes, "added model "+m.Model)
		}
	}
	for _, m := range before {
		if _, seen := newer[m.Model]; !seen {
			changes = append(changes, "dropped model "+m.Model)
		}
	}
	return changes
}

// scoreChanges names what moved in each model's leaderboard reading: whether the
// leaderboard carries it at all, and then each figure it states.
func scoreChanges(before, now []providers.ModelFacts) []string {
	older := index(before)
	var changes []string
	for _, m := range now {
		was, seen := older[m.Model]
		scored := m.Leaderboard.Listed
		wasScored := seen && was.Leaderboard.Listed
		switch {
		case scored && !wasScored:
			changes = append(changes, "scored "+m.Model)
		case !scored && wasScored:
			changes = append(changes, "no longer scored "+m.Model)
		case seen && m.Leaderboard != was.Leaderboard:
			changes = append(changes, scoreFields(m.Model, was.Leaderboard, m.Leaderboard)...)
		}
	}
	return changes
}

// scoreFields names each figure of one model's leaderboard reading that moved.
func scoreFields(model string, before, now providers.Score) []string {
	var changes []string
	if before.Value != now.Value {
		changes = append(changes, moved(model, "score", number(before.Value), number(now.Value)))
	}
	if before.Benchmark != now.Benchmark {
		changes = append(changes, moved(model, "benchmark", before.Benchmark, now.Benchmark))
	}
	if before.Provenance != now.Provenance {
		changes = append(changes, moved(model, "provenance", string(before.Provenance), string(now.Provenance)))
	}
	if before.Margin != now.Margin {
		changes = append(changes, moved(model, "margin", before.Margin, now.Margin))
	}
	if before.ScoredOn != now.ScoredOn {
		changes = append(changes, moved(model, "scored on", before.ScoredOn, now.ScoredOn))
	}
	return changes
}

// moved is one figure's move on one model, as a source's own changes list states
// it.
func moved(model, figure, before, now string) string {
	return model + " " + figure + " " + orNone(before) + " -> " + orNone(now)
}

// number renders a leaderboard figure as its own text, with no trailing zero the
// leaderboard did not state.
func number(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// orNone is a value a source did not state, so an empty field reads as such in a
// change rather than as a gap.
func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// listChanges names the entries a source's own list added and dropped.
func listChanges(what string, before, now []string) []string {
	var changes []string
	for _, name := range now {
		if !slices.Contains(before, name) {
			changes = append(changes, "added "+what+" "+name)
		}
	}
	for _, name := range before {
		if !slices.Contains(now, name) {
			changes = append(changes, "dropped "+what+" "+name)
		}
	}
	return changes
}

// index is a list of model facts by their own model id.
func index(models []providers.ModelFacts) map[string]providers.ModelFacts {
	byModel := make(map[string]providers.ModelFacts, len(models))
	for _, m := range models {
		byModel[m.Model] = m
	}
	return byModel
}

// modelNames is the model ids a list of model facts carries, in its own order.
func modelNames(models []providers.ModelFacts) []string {
	names := make([]string, 0, len(models))
	for _, m := range models {
		names = append(names, m.Model)
	}
	return names
}

// classify is one source's read as a source record: today's date and the changes
// when it was read, and the previous state with its last good read date carried
// forward when it was not.
func classify(on string, err error, changes []string, last providers.Source) providers.Source {
	if err != nil {
		return providers.Source{State: providers.ReadCouldNotRead, LastGoodReadOn: last.LastGoodReadOn, Note: providers.CapText(err.Error(), maxNoteBytes)}
	}
	state := providers.ReadUnchanged
	if len(changes) > 0 {
		state = providers.ReadChanged
	}
	return providers.Source{State: state, LastGoodReadOn: on, Changes: changes}
}

// bare is the name a leaderboard or a feed is likely to carry a model under: its
// own segment after the last "/", without the ":free" suffix a plan's model id
// may carry. A source that names the model differently is recorded as one that
// does not carry it, rather than matched to a guess.
func bare(model string) string {
	name := model[strings.LastIndexByte(model, '/')+1:]
	if i := strings.IndexByte(name, ':'); i >= 0 {
		name = name[:i]
	}
	return name
}
