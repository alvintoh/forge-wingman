package main

import (
	"slices"

	"github.com/alvintoh/forge-wingman/internal/planrefresh"
	"github.com/alvintoh/forge-wingman/internal/providers"
)

// sharedUnread collects the shared sources, the leaderboard and the free feed,
// that stayed unread for more than planrefresh.DefaultMaxUnreadDays. Every plan's
// facts records the same reading of each, so a source stale in several plans'
// facts is kept once and fails the run once.
type sharedUnread struct{ stale []planrefresh.Unread }

// note adds each shared source facts records as stale and not already noted.
func (s *sharedUnread) note(facts providers.Facts) {
	for _, u := range planrefresh.SharedUnread(facts) {
		if u.Stale && !slices.ContainsFunc(s.stale, func(n planrefresh.Unread) bool { return n.Name == u.Name }) {
			s.stale = append(s.stale, u)
		}
	}
}

// errors is one error per shared source noted.
func (s *sharedUnread) errors() []error {
	errs := make([]error, len(s.stale))
	for i, u := range s.stale {
		errs[i] = staleError(u)
	}
	return errs
}
