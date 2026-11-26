package planrefresh

import (
	"net/url"
	"slices"
	"time"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

// The names a plan-owned source that could not be read is logged and reported
// under.
const (
	SourceVendorPage = "vendor-page"
	SourceModelList  = "model-list"
)

// DefaultMaxUnreadDays is how many whole days a source may stay unread before a
// refresh fails naming it. A source read well 21 days ago is still within it.
const DefaultMaxUnreadDays = 21

// Unread is one source of a facts document that its read could not reach.
type Unread struct {
	// Name is one of the Source constants.
	Name string
	// Host is the host of the page that could not be read; empty for any other source.
	Host string
	// Since is the yyyy-mm-dd date the source was last read well, or, for a source
	// never read well, the date it was first found unreadable.
	Since string
	// Days is how many whole days Since is before the document's read date.
	Days int
	// Stale reports the source has been unread for more than DefaultMaxUnreadDays.
	// A date that cannot be read, or that is after the read date, is stale, so a
	// corrupt record fails the run rather than hiding an unread source.
	Stale bool
}

// PlanUnread lists the sources one plan owns that facts records as unreadable:
// each vendor page and the model list. A model list that answered and was refused
// is left out, since the refusal already fails the run under its own event.
func PlanUnread(facts providers.Facts, listRefused bool) []Unread {
	var found []Unread
	for _, page := range facts.Pages {
		if page.Source.State == providers.ReadCouldNotRead {
			found = append(found, unread(SourceVendorPage, pageHost(page.URL), page.Source, facts.ReadOn))
		}
	}
	if !listRefused && facts.Models.Source.State == providers.ReadCouldNotRead {
		found = append(found, unread(SourceModelList, "", facts.Models.Source, facts.ReadOn))
	}
	return found
}

// withLegacyUnreadDates is last with each source it records as could-not-read
// under neither date dated from the read that wrote last. A document written
// before FirstUnreadOn existed records an unread source that way, and it has
// been unread since that read, so the next failure counts from there rather than
// from its own date.
func withLegacyUnreadDates(last providers.Facts) providers.Facts {
	date := func(src *providers.Source) {
		if src.State == providers.ReadCouldNotRead && src.LastGoodReadOn == "" && src.FirstUnreadOn == "" {
			src.FirstUnreadOn = last.ReadOn
		}
	}
	last.Pages = slices.Clone(last.Pages)
	for i := range last.Pages {
		date(&last.Pages[i].Source)
	}
	date(&last.Models.Source)
	date(&last.Leaderboard)
	return last
}

// unread is src, found unreadable in a document read on readOn, aged from its
// last good read, or from the first time it was found unreadable when it has had
// none.
func unread(name, host string, src providers.Source, readOn string) Unread {
	since := src.LastGoodReadOn
	if since == "" {
		since = src.FirstUnreadOn
	}
	found := Unread{Name: name, Host: host, Since: since, Stale: true}
	then, err := time.Parse(dateLayout, since)
	if err != nil {
		return found
	}
	now, err := time.Parse(dateLayout, readOn)
	if err != nil || then.After(now) {
		return found
	}
	found.Days = int(now.Sub(then).Hours() / 24)
	found.Stale = found.Days > DefaultMaxUnreadDays
	return found
}

// pageHost is the host a page's address names, the only part of it a log or an
// error repeats, since an address can carry credentials.
func pageHost(address string) string {
	u, err := url.Parse(address)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
