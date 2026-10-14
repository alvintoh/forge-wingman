package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/alvintoh/forge-wingman/internal/planrefresh"
	"github.com/alvintoh/forge-wingman/internal/providers"
)

// refreshTimeout bounds one request the refresh makes, so a source that hangs
// cannot hold the weekly read open.
const refreshTimeout = 30 * time.Second

// planFactsStore is everything plans-refresh reads and the one document it
// writes: the plans themselves, the facts last recorded for one, and the facts
// this read produces. It holds no method that writes a plan, so a refresh can
// neither record a verdict or a reply nor change an opt-in: the plan's own record
// stays the owner CLI's to write.
type planFactsStore interface {
	List(ctx context.Context) ([]providers.Listing, error)
	LatestFacts(ctx context.Context, provider string) (facts providers.Facts, date string, ok bool, err error)
	PutFacts(ctx context.Context, provider, date string, facts providers.Facts) error
}

// plansRefresh reads each configured plan's own pages and model list, the Coding
// leaderboard's scores and the free feed, diffs them against the facts last
// recorded for the plan and writes a new dated facts document. It informs the
// owner and changes nothing about which provider runs.
func plansRefresh(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("plans-refresh", flag.ContinueOnError)
	provider := fs.String("provider", "", "refresh only this plan (default: every configured plan)")
	leaderboard := fs.String("leaderboard-url", "", "the Coding leaderboard to read scores from")
	feedURL := fs.String("feed-url", "", "the free feed to read as a change signal")
	if err := fs.Parse(args); err != nil {
		return err
	}
	plans, closeStore, err := openPlans(ctx, e, *provider, *provider != "", nil)
	if err != nil {
		return err
	}
	defer closeStore()

	// The leaderboard and the free feed are one document for every plan, so each
	// is read once here and the readings handed to every plan's refresh.
	client := &http.Client{Timeout: refreshTimeout}
	refresher := planrefresh.Refresher{Vendor: planrefresh.Vendor{HTTP: client}, Now: time.Now}
	board := planrefresh.Board{Endpoint: *leaderboard, HTTP: client}
	refresher.Board, refresher.BoardErr = board.Read(ctx)
	if refresher.BoardErr != nil {
		logger.Warn("plansRefreshSourceUnread", "source", "leaderboard", "err", refresher.BoardErr.Error())
	}
	feed := planrefresh.Feed{Endpoint: *feedURL, HTTP: client}
	refresher.Feed, refresher.FeedErr = feed.Read(ctx)
	if refresher.FeedErr != nil {
		logger.Warn("plansRefreshSourceUnread", "source", "free-feed", "err", refresher.FeedErr.Error())
	}
	return refreshPlans(ctx, logger, plans, refresher, *provider)
}

// refreshPlans refreshes every configured plan the store lists, or the one named,
// writing one facts document per plan under the plan's own read date and nothing
// else.
func refreshPlans(ctx context.Context, logger *slog.Logger, plans planFactsStore, refresher planrefresh.Refresher, named string) error {
	rows, err := plans.List(ctx)
	if err != nil {
		return err
	}
	refreshed := 0
	for _, row := range rows {
		if named != "" && row.Provider != named {
			continue
		}
		if !row.Plan.Configured() {
			if named != "" {
				return fmt.Errorf("plan %s has no billing recorded, so it has no facts to refresh", named)
			}
			continue
		}
		last, _, _, err := plans.LatestFacts(ctx, row.Provider)
		if err != nil {
			return err
		}
		facts := refresher.Refresh(ctx, row.Provider, row.Plan.Definition, last)
		if err := plans.PutFacts(ctx, row.Provider, facts.ReadOn, facts); err != nil {
			return err
		}
		refreshed++
		logger.Info("planFactsWritten", "provider", row.Provider, "read_on", facts.ReadOn, "flags", fmt.Sprint(facts.Flags))
	}
	switch {
	case refreshed == 0 && named != "":
		return fmt.Errorf("plan %s is not a plan this store holds", named)
	case refreshed == 0:
		logger.Info("plansRefreshNothingToRead")
	}
	return nil
}
