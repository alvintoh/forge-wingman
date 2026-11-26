package planrefresh

import "github.com/alvintoh/forge-wingman/internal/providers"

// SourceLeaderboard is the name the leaderboard, the one source every plan shares
// a reading of, is logged and reported under.
const SourceLeaderboard = "leaderboard"

// SharedUnread lists the sources every plan's facts records one reading of, the
// leaderboard, that facts records as unreadable.
func SharedUnread(facts providers.Facts) []Unread {
	var found []Unread
	if facts.Leaderboard.State == providers.ReadCouldNotRead {
		found = append(found, unread(SourceLeaderboard, "", facts.Leaderboard, facts.ReadOn))
	}
	return found
}
