package planrefresh

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

// Refresher reads one plan's sources and diffs them against the facts last
// written for it. The Coding leaderboard is one document for every plan, so a run
// reads it once and sets it here.
type Refresher struct {
	// Vendor reads the plan's own pages and its models endpoint.
	Vendor Vendor
	// Board is the leaderboard as read, shared by every plan in the run; BoardErr
	// is why it could not be read, nil when it was.
	Board    BoardReport
	BoardErr error
	// Now is the clock the read date comes from; nil means time.Now.
	Now func() time.Time
	// RateCard is the vendor's ids for the models a plan's rate card prices,
	// each of which a model list must hold; nil means the embedded provider
	// configuration's own.
	RateCard func(provider string) []string
	// Rank ranks a plan's models from its settled runs; nil means no ranking is
	// read and the last one stands.
	Rank Ranker
}

// Refresh reads provider's sources and returns the facts document to record,
// dated by the refresher's clock. A source it could not read is recorded as
// could-not-read rather than failing the refresh, with its last good read date
// and the figures it last answered with carried forward.
//
// The error is non-nil only when the vendor's model list answered and was
// refused as partial or malformed. The document is still returned, recording the
// list and the ranking as could-not-read beside the last good ones, and is the
// one to write.
func (r Refresher) Refresh(ctx context.Context, provider string, def providers.Definition, last providers.Facts) (providers.Facts, error) {
	on := r.now().UTC().Format(dateLayout)
	last = withLegacyUnreadDates(last)
	facts := providers.Facts{
		ReadOn: on,
		Price:  def.MonthlyPrice,
		Limits: def.LimitBehaviour,
		Flags:  providers.Flags(def),
	}
	facts.Pages = r.pages(ctx, def, last, on)
	var refused error
	facts.Models, facts.Leaderboard, refused = r.models(ctx, provider, last, on)
	facts.Ranking = r.ranking(ctx, provider, last, on, refused)
	return facts, refused
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
// read, and so does a list that answered and was refused, which is returned too.
func (r Refresher) models(ctx context.Context, provider string, last providers.Facts, on string) (providers.ModelsFacts, providers.Source, error) {
	names, err := r.Vendor.Models(ctx, provider)
	var refused error
	pending, accepted := last.Models.Refused, ""
	if err == nil {
		var check *listRefusal
		check, pending, accepted = judgeList(names, r.rateCard(provider), last.Models, on)
		if check != nil {
			refused = fmt.Errorf("refusing the model list of %s: %w", provider, check)
			err = refused
		}
	}
	if err != nil {
		names = modelNames(last.Models.Models)
	}
	models := make([]providers.ModelFacts, 0, len(names))
	for _, name := range names {
		models = append(models, r.model(name, names, last))
	}
	facts := providers.ModelsFacts{Models: models, Refused: pending}
	facts.Source = classify(on, err, modelChanges(last.Models.Models, models), last.Models.Source)
	if accepted != "" {
		facts.Source.Note = accepted
	}
	return facts, classify(on, r.BoardErr, scoreChanges(last.Models.Models, models), last.Leaderboard), refused
}

// judgeList decides a model list read on the date on against the last
// document's: the refusal, nil when the list stands; the refused list to
// remember; and, for a list accepted only because the same list was refused for
// dropping too many models on an earlier date, why it was accepted, as the
// vendor's own retirements rather than a partial reply. Only that refusal is
// remembered, so an empty list, or one over the cap, holding a malformed id or
// missing a rate-carded model, is never accepted however often it repeats, and
// any other outcome clears what was remembered. A repeat read on the date the
// list was first refused stays refused, so a retry cannot accept a partial
// reply the vendor gives every time.
func judgeList(ids, rateCard []string, last providers.ModelsFacts, on string) (*listRefusal, providers.RefusedList, string) {
	check := checkModelList(ids, rateCard, last.Models)
	if check == nil || check.reason != refusedDroppedShare || len(ids) == 0 {
		return check, providers.RefusedList{}, ""
	}
	sorted := slices.Sorted(slices.Values(ids))
	if !slices.Equal(sorted, last.Refused.Models) {
		return check, providers.RefusedList{Models: sorted, FirstOn: on}, ""
	}
	if on <= last.Refused.FirstOn {
		return check, last.Refused, ""
	}
	return nil, providers.RefusedList{}, "accepted as the new list, read the same on 2 dates running: " + check.detail
}

// rateCard is the vendor's ids for the models provider's rate card prices.
func (r Refresher) rateCard(provider string) []string {
	if r.RateCard == nil {
		return providers.RateCardModels(provider)
	}
	return r.RateCard(provider)
}

// model is one model's facts: the leaderboard's reading of it, among names, the
// plan's whole model list. A leaderboard that could not be read keeps what the
// last document recorded for the model, so a model never reads as unscored
// because its source went quiet.
func (r Refresher) model(name string, names []string, last providers.Facts) providers.ModelFacts {
	facts := providers.ModelFacts{Model: name}
	if before, seen := last.Model(name); seen && r.BoardErr != nil {
		facts.Leaderboard = before.Leaderboard
	} else {
		facts.Leaderboard = boardScore(r.Board.Models, name, names)
	}
	return facts
}

// boardScore is the leaderboard's reading of the model name, by the id the
// vendor states, else by that id's bare name: its own segment after the last "/",
// without a ":free" suffix. The bare name is a match only where no other model of
// the plan has it too, so one score is never attributed to two models.
func boardScore(board map[string]providers.Score, name string, names []string) providers.Score {
	if score, ok := board[name]; ok {
		return score
	}
	short := bare(name)
	if short == name {
		return providers.Score{}
	}
	for _, other := range names {
		if other != name && bare(other) == short {
			return providers.Score{}
		}
	}
	return board[short]
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
// forward when it was not. A source with no good read yet records the date it
// was first found unreadable instead, so it can be aged, and a source the plan
// has no way to read is recorded as not applicable and never aged.
func classify(on string, err error, changes []string, last providers.Source) providers.Source {
	if err != nil {
		note := providers.CapText(err.Error(), maxNoteBytes)
		if errors.Is(err, ErrNoModelList) {
			return providers.Source{State: providers.ReadNotApplicable, LastGoodReadOn: last.LastGoodReadOn, Note: note}
		}
		unread := providers.Source{State: providers.ReadCouldNotRead, LastGoodReadOn: last.LastGoodReadOn, Note: note}
		if unread.LastGoodReadOn == "" {
			unread.FirstUnreadOn = cmp.Or(last.FirstUnreadOn, on)
		}
		return unread
	}
	state := providers.ReadUnchanged
	if len(changes) > 0 {
		state = providers.ReadChanged
	}
	return providers.Source{State: state, LastGoodReadOn: on, Changes: changes}
}

// bare is the name a leaderboard is likely to carry a model under: its own
// segment after the last "/", without the ":free" suffix a plan's model id may
// carry. A leaderboard that names the model differently is recorded as one that
// does not carry it, rather than matched to a guess.
func bare(model string) string {
	name := model[strings.LastIndexByte(model, '/')+1:]
	if i := strings.IndexByte(name, ':'); i >= 0 {
		name = name[:i]
	}
	return name
}
