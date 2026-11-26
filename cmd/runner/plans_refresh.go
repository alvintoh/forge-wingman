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
// leaderboard's scores, and the settled runs it ranks the
// plan's models from, diffs them against the facts last recorded for the plan
// and writes a new dated facts document. The ranking is a proposal for the owner
// to apply: it informs the owner and changes nothing about which provider or
// model runs.
func plansRefresh(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("plans-refresh", flag.ContinueOnError)
	provider := fs.String("provider", "", "refresh only this plan (default: every configured plan)")
	leaderboard := fs.String("leaderboard-url", "", "the Coding leaderboard to read scores from")
	// The free feed is no longer read, but plans-refresh.yml still passes this flag,
	// and an undefined one would fail every run.
	feedURL := fs.String("feed-url", "", "ignored: the free feed is no longer read")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *feedURL != "" {
		logger.Warn("plansRefreshFlagIgnored", "flag", "feed-url")
	}
	fsc, closeStore, err := openStore(ctx, e, *provider, *provider != "", nil)
	if err != nil {
		return err
	}
	defer closeStore()
	plans := store.NewPlans(fsc)

	// The leaderboard is one document for every plan, so it is read once here and
	// the reading handed to every plan's refresh.
	client := planrefresh.NewClient()
	refresher := newRefresher(client, store.NewRecords(fsc).WithLogger(logger), logger)
	refresher.Board, refresher.BoardErr = readLeaderboard(ctx, logger, planrefresh.Board{Endpoint: *leaderboard, HTTP: client})
	return refreshPlans(ctx, logger, plans, refresher, *provider)
}

// readLeaderboard reads the leaderboard. One that cannot be read is logged under
// the token plans-refresh.yml turns into a warning annotation, and returned for
// the refresh to record as could-not-read.
func readLeaderboard(ctx context.Context, logger *slog.Logger, board planrefresh.Board) (planrefresh.BoardReport, error) {
	report, err := board.Read(ctx)
	if err != nil {
		logger.Warn("plansRefreshSourceUnread", "source", planrefresh.SourceLeaderboard, "err", err.Error())
	}
	return report, err
}

// newRefresher is the refresher a plans-refresh run starts from: the vendor read
// through client, the real clock, and the plans' models ranked from the settled
// runs records holds. The run then sets the shared leaderboard reading.
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
// else. A plan whose facts cannot be read or written, whose model list was
// refused, or one of whose sources has stayed unread for more than
// planrefresh.DefaultMaxUnreadDays is logged and the rest are still refreshed; the
// error returned names every plan that failed. The leaderboard is one reading
// for every plan, so it stayed unread that long fails the run once, not once per
// plan.
func refreshPlans(ctx context.Context, logger *slog.Logger, plans planFactsStore, refresher planrefresh.Refresher, named string) error {
	rows, err := plans.List(ctx)
	if err != nil {
		return err
	}
	attempted := 0
	var failed []error
	var shared sharedUnread
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
		for _, u := range planrefresh.PlanUnread(facts, refused != nil) {
			logUnread(logger, row.Provider, u)
			if u.Stale {
				failed = append(failed, fmt.Errorf("plan %s: %w", row.Provider, staleError(u)))
			}
		}
		shared.note(facts)
		logger.Info("planFactsWritten", "provider", row.Provider, "read_on", facts.ReadOn, "flags", fmt.Sprint(facts.Flags))
	}
	switch {
	case attempted == 0 && named != "":
		return fmt.Errorf("plan %s is not a plan this store holds", named)
	case attempted == 0:
		logger.Info("plansRefreshNothingToRead")
	}
	return errors.Join(append(failed, shared.errors()...)...)
}

// logUnread logs one plan's source that its read could not reach, with the plan
// and the source as fields of their own and never the page's address, which can
// carry credentials; the reason is in the facts document's own note.
func logUnread(logger *slog.Logger, provider string, u planrefresh.Unread) {
	attrs := []any{"provider", provider, "source", u.Name, "unread_since", u.Since, "days_unread", u.Days}
	if u.Host != "" {
		attrs = append(attrs, "host", u.Host)
	}
	logger.Warn("plansRefreshSourceUnread", attrs...)
}

// staleError says which source has stayed unread past the limit, and since when.
func staleError(u planrefresh.Unread) error {
	source := u.Name
	if u.Host != "" {
		source += " " + u.Host
	}
	return fmt.Errorf("%s has not been read since %s, over the %d-day limit", source, u.Since, planrefresh.DefaultMaxUnreadDays)
}
