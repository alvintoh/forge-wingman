package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/planrefresh"
	"github.com/alvintoh/forge-wingman/internal/providers"
	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

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
// leaderboard's scores and the free feed, and the settled runs it ranks the
// plan's models from, diffs them against the facts last recorded for the plan
// and writes a new dated facts document. The ranking is a proposal for the owner
// to apply: it informs the owner and changes nothing about which provider or
// model runs.
func plansRefresh(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("plans-refresh", flag.ContinueOnError)
	provider := fs.String("provider", "", "refresh only this plan (default: every configured plan)")
	leaderboard := fs.String("leaderboard-url", "", "the Coding leaderboard to read scores from")
	feedURL := fs.String("feed-url", "", "the free feed to read as a change signal")
	if err := fs.Parse(args); err != nil {
		return err
	}
	fsc, closeStore, err := openStore(ctx, e, *provider, *provider != "", nil)
	if err != nil {
		return err
	}
	defer closeStore()
	plans := store.NewPlans(fsc)

	// The leaderboard and the free feed are one document for every plan, so each
	// is read once here and the readings handed to every plan's refresh.
	client := planrefresh.NewClient()
	refresher := newRefresher(client, store.NewRecords(fsc).WithLogger(logger), logger)
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

// newRefresher is the refresher a plans-refresh run starts from: the vendor read
// through client, the real clock, and the plans' models ranked from the settled
// runs records holds. The run then sets the shared leaderboard and feed readings.
func newRefresher(client *http.Client, records dispatcher.ModelRecords, logger *slog.Logger) planrefresh.Refresher {
	return planrefresh.Refresher{
		Vendor: planrefresh.Vendor{HTTP: client},
		Now:    time.Now,
		Rank:   settledRanker(records, logger),
	}
}

// settledRanker ranks a plan's models from the settled runs records holds over
// the trailing window, by the same bar and ranking a claim chooses by. The
// candidates are the models on the plan's rate card, which an accepted model
// list holds every one of. A read that fails is logged here, since the refresh
// records it as could-not-read and still succeeds.
func settledRanker(records dispatcher.ModelRecords, logger *slog.Logger) planrefresh.Ranker {
	return func(ctx context.Context, provider string, now time.Time) ([]providers.SizeRanking, error) {
		settled, err := records.ModelRecords(ctx, now.Add(-runner.TrailingWindow))
		if err != nil {
			logger.Warn("planRankingUnread", "provider", provider, "err", err.Error())
			return nil, err
		}
		var candidates []string
		for _, id := range providers.RateCardModels(provider) {
			candidates = append(candidates, provider+"/"+id)
		}
		return dispatcher.RankSizes(settled, candidates, providers.RatesFor), nil
	}
}

// refreshPlans refreshes every configured plan the store lists, or the one named,
// writing one facts document per plan under the plan's own read date and nothing
// else. A plan whose facts cannot be read or written, or whose model list was
// refused, is logged and the rest are still refreshed; the error returned names
// every plan that failed.
func refreshPlans(ctx context.Context, logger *slog.Logger, plans planFactsStore, refresher planrefresh.Refresher, named string) error {
	rows, err := plans.List(ctx)
	if err != nil {
		return err
	}
	attempted := 0
	var failed []error
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
		attempted++
		last, _, _, err := plans.LatestFacts(ctx, row.Provider)
		if err != nil {
			logger.Error("planFactsReadFailed", "provider", row.Provider, "err", err.Error())
			failed = append(failed, fmt.Errorf("reading the facts last recorded for %s: %w", row.Provider, err))
			continue
		}
		facts, refused := refresher.Refresh(ctx, row.Provider, row.Plan.Definition, last)
		if err := plans.PutFacts(ctx, row.Provider, facts.ReadOn, facts); err != nil {
			logger.Error("planFactsWriteFailed", "provider", row.Provider, "err", err.Error())
			failed = append(failed, fmt.Errorf("writing the facts for %s: %w", row.Provider, err))
			continue
		}
		if refused != nil {
			logger.Error("planModelListRefused", "provider", row.Provider, "err", refused.Error())
			failed = append(failed, refused)
		}
		logger.Info("planFactsWritten", "provider", row.Provider, "read_on", facts.ReadOn, "flags", fmt.Sprint(facts.Flags))
	}
	switch {
	case attempted == 0 && named != "":
		return fmt.Errorf("plan %s is not a plan this store holds", named)
	case attempted == 0:
		logger.Info("plansRefreshNothingToRead")
	}
	return errors.Join(failed...)
}
