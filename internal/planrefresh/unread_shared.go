package planrefresh

import "github.com/alvintoh/forge-wingman/internal/providers"

// The names a source every plan shares one reading of is logged and reported
// under.
const (
	SourceLeaderboard = "leaderboard"
	SourceFreeFeed    = "free-feed"
)

// SharedUnread lists the sources every plan's facts records one reading of, the
// leaderboard and the free feed, that facts records as unreadable.
func SharedUnread(facts providers.Facts) []Unread {
	var found []Unread
	if facts.Leaderboard.State == providers.ReadCouldNotRead {
		found = append(found, unread(SourceLeaderboard, "", facts.Leaderboard, facts.ReadOn))
	}
	if facts.Feed.Source.State == providers.ReadCouldNotRead {
		found = append(found, unread(SourceFreeFeed, "", facts.Feed.Source, facts.ReadOn))
	}
	return found
}
