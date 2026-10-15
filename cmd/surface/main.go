// Command surface serves the embedded SPA and the JSON endpoints under /api.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path"
	"syscall"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
	"github.com/alvintoh/forge-wingman/web"
)

// commitSHA is the git commit the binary was built from, injected at build time
// via -ldflags "-X main.commitSHA=<sha>"; "dev" for a local build.
var commitSHA = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if err := run(logger); err != nil {
		logger.Error("server exited", "err", err)
		os.Exit(1)
	}
}

// run wires the server and blocks until the process is signalled, returning the
// first error that is not a clean shutdown.
func run(logger *slog.Logger) error {
	spa, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		return err
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// The comparison, providers and autonomy endpoints read past runs, the
	// owner's plan records and the level, so they need the store. A local run
	// without GOOGLE_CLOUD_PROJECT has none and serves the SPA alone, answering
	// those endpoints with 503.
	var comparison ComparisonReader
	var providerPlans ProviderPlansReader
	var autonomy AutonomyReader
	if project := os.Getenv("GOOGLE_CLOUD_PROJECT"); project != "" {
		fsc, err := firestore.NewClient(context.Background(), project)
		if err != nil {
			return fmt.Errorf("firestore client: %w", err)
		}
		defer func() { _ = fsc.Close() }()
		comparison = store.NewRecords(fsc)
		providerPlans = store.NewProviderPlans(fsc)
		autonomy = store.NewAutonomies(fsc)
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           newMux(spa, logger, comparison, providerPlans, autonomy),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", srv.Addr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// ComparisonReader reads the comparison of past runs the endpoint reports.
type ComparisonReader interface {
	Comparison(ctx context.Context, since time.Time) (store.Comparison, error)
}

// ProviderPlansReader reads what the providers view lists: every plan the owner
// has recorded, each with what its provider's own allowance windows hold spent.
// It has no write method, because the records behind it are written by the owner
// CLI alone (adr/0014).
type ProviderPlansReader interface {
	Providers(ctx context.Context, at time.Time) ([]store.ProviderPlan, error)
}

// AutonomyReader reads what the autonomy view shows: the level the owner has
// set, the earned snapshot behind it and the changes recorded, newest first. It
// has no method that sets the level, which stays the runner autonomy command.
type AutonomyReader interface {
	AutonomyView(ctx context.Context, history int) (store.AutonomyView, error)
}

// newMux claims /api/ explicitly so an unknown endpoint 404s instead of
// falling through to the SPA's index.html. A nil comparison serves every
// endpoint but /api/comparison, a nil providerPlans every endpoint but
// /api/providers, and a nil autonomy every endpoint but /api/autonomy, each of
// which then has no store to read.
func newMux(spa fs.FS, logger *slog.Logger, comparison ComparisonReader, providerPlans ProviderPlansReader, autonomy AutonomyReader) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			SHA string `json:"sha"`
		}{commitSHA})
	})
	mux.HandleFunc("GET /api/comparison", func(w http.ResponseWriter, r *http.Request) {
		if comparison == nil {
			http.Error(w, "the comparison needs a run store", http.StatusServiceUnavailable)
			return
		}
		cmp, err := comparison.Comparison(r.Context(), time.Now().Add(-runner.TrailingWindow))
		if err != nil {
			logger.Error("comparisonNotRead", "err", err.Error())
			http.Error(w, "reading the comparison", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(renderComparison(cmp))
	})
	mux.HandleFunc("GET /api/providers", func(w http.ResponseWriter, r *http.Request) {
		if providerPlans == nil {
			http.Error(w, "the providers view needs a plan store", http.StatusServiceUnavailable)
			return
		}
		plans, err := providerPlans.Providers(r.Context(), time.Now())
		if err != nil {
			logger.Error("providersNotRead", "err", err.Error())
			http.Error(w, "reading the plans", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(renderProviders(plans))
	})
	mux.HandleFunc("GET /api/autonomy", func(w http.ResponseWriter, r *http.Request) {
		if autonomy == nil {
			http.Error(w, "the autonomy view needs a store", http.StatusServiceUnavailable)
			return
		}
		view, err := autonomy.AutonomyView(r.Context(), autonomyHistoryShown)
		if err != nil {
			logger.Error("autonomyNotRead", "err", err.Error())
			http.Error(w, "reading the autonomy level", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(renderAutonomy(view))
	})
	mux.Handle("GET /api/", http.NotFoundHandler())
	mux.Handle("GET /", spaHandler(spa))

	return mux
}

// comparisonResponse is the comparison endpoint's wire shape. It carries no
// ticket text: the run records it reads hold ticket titles and bodies, and what
// gates this binary today is IAP rather than the endpoint itself, so none of them
// is ever served from here.
type comparisonResponse struct {
	Models    []modelComparison    `json:"models"`
	Providers []providerComparison `json:"providers"`
	Runs      []runCache           `json:"runs"`
	Drifted   []driftedRun         `json:"drifted_runs"`
}

type modelComparison struct {
	Model string `json:"model"`
	// Tickets is how many runs built on this model and Passed how many of
	// them succeeded, so the rate is Passed/Tickets.
	Tickets          int     `json:"tickets"`
	Passed           int     `json:"passed"`
	CostPerTicketUSD float64 `json:"cost_per_ticket_usd"`
}

type providerComparison struct {
	Provider                 string  `json:"provider"`
	TrailingCostPerTicketUSD float64 `json:"trailing_cost_per_ticket_usd"`
	Sample                   int     `json:"sample"`
	PeakTickets              int     `json:"peak_tickets"`
	PeakCostUSD              float64 `json:"peak_cost_usd"`
	OffPeakTickets           int     `json:"offpeak_tickets"`
	OffPeakCostUSD           float64 `json:"offpeak_cost_usd"`
}

// runCache is one run's own prompt-cache share: the share of its input tokens
// that came from the provider's cache, and each phase's own share as the run
// view lists it.
type runCache struct {
	RunID       string  `json:"run_id"`
	Provider    string  `json:"provider"`
	Model       string  `json:"model"`
	CachedShare float64 `json:"cached_share"`
	// BuildCache and PlanCache are the phase's own figures as the view prints
	// them: the share of its first request and of its whole input, or the word
	// that stands in for a share the run recorded none of.
	BuildCache string `json:"build_cache"`
	PlanCache  string `json:"plan_cache"`
}

type driftedRun struct {
	RunID     string  `json:"run_id"`
	Provider  string  `json:"provider"`
	Model     string  `json:"model"`
	CostUSD   float64 `json:"cost_usd"`
	CostDrift string  `json:"cost_drift"`
}

// renderComparison renders the store's comparison as JSON, converting its
// exact micro-dollar costs to the dollar figures the endpoint displays. Every
// list is rendered whether or not it holds anything, so a view narrowing to one
// of them reads an empty list rather than a null.
func renderComparison(c store.Comparison) comparisonResponse {
	out := comparisonResponse{
		Models:    []modelComparison{},
		Providers: []providerComparison{},
		Runs:      []runCache{},
		Drifted:   []driftedRun{},
	}
	for _, m := range c.Models {
		out.Models = append(out.Models, modelComparison{
			Model: m.Model, Tickets: m.Tickets, Passed: m.Passed,
			CostPerTicketUSD: m.CostPerTicket.USD(),
		})
	}
	for _, p := range c.Providers {
		out.Providers = append(out.Providers, providerComparison{
			Provider: p.Provider, TrailingCostPerTicketUSD: p.TrailingCostPerTicket.USD(), Sample: p.Sample,
			PeakTickets: p.PeakTickets, PeakCostUSD: p.PeakCost.USD(),
			OffPeakTickets: p.OffPeakTickets, OffPeakCostUSD: p.OffPeakCost.USD(),
		})
	}
	for _, r := range c.Runs {
		out.Runs = append(out.Runs, runCache{
			RunID: r.RunID, Provider: r.Provider, Model: r.Model, CachedShare: r.CachedShare,
			BuildCache: r.BuildCache, PlanCache: r.PlanCache,
		})
	}
	for _, d := range c.Drifted {
		out.Drifted = append(out.Drifted, driftedRun{
			RunID: d.RunID, Provider: d.Provider, Model: d.Model,
			CostUSD: d.Cost.USD(), CostDrift: d.CostDrift,
		})
	}
	return out
}

// providersResponse is the providers endpoint's wire shape: every plan the owner
// has recorded, in the order a run walks them. It carries no API key — a plan's
// key is a repository secret the runner reads directly and is never a field of
// the record — so nothing of one can reach a reader.
type providersResponse struct {
	Plans []providerPlan `json:"plans"`
}

type providerPlan struct {
	Provider string  `json:"provider"`
	PriceUSD float64 `json:"price_usd"`
	Verdict  string  `json:"verdict"`
	// WindowUsage is the plan's own allowance windows with what each holds
	// spent, or usageUnknown for a plan whose provider reports none.
	WindowUsage any `json:"window_usage"`
}

// windowUsage is one of a plan's own allowance windows and what is spent in it.
type windowUsage struct {
	Name     string  `json:"name"`
	LimitUSD float64 `json:"limit_usd"`
	UsedUSD  float64 `json:"used_usd"`
}

// usageUnknown is what the endpoint reports for a plan whose provider states no
// window: it has no allowance to have spent.
const usageUnknown = "unknown"

// renderProviders renders the store's plans as JSON, converting each exact
// micro-dollar figure to the dollars the endpoint displays.
func renderProviders(plans []store.ProviderPlan) providersResponse {
	out := providersResponse{Plans: make([]providerPlan, 0, len(plans))}
	for _, plan := range plans {
		entry := providerPlan{
			Provider: plan.Provider,
			PriceUSD: plan.Price.USD(),
			Verdict:  string(plan.Verdict),
		}
		entry.WindowUsage = usageUnknown
		if len(plan.Windows) > 0 {
			usage := make([]windowUsage, 0, len(plan.Windows))
			for _, w := range plan.Windows {
				usage = append(usage, windowUsage{
					Name:     w.Name,
					LimitUSD: w.Limit.USD(),
					UsedUSD:  w.Used.USD(),
				})
			}
			entry.WindowUsage = usage
		}
		out.Plans = append(out.Plans, entry)
	}
	return out
}

// autonomyHistoryShown is how many level changes the autonomy view lists, newest
// first. The history gains one document per change, so it is many times the
// number of changes a level's own range can produce.
const autonomyHistoryShown = 50

// autonomyResponse is the autonomy endpoint's wire shape: the level the owner
// has set and what it allows, the earned snapshot behind it, and the changes
// recorded, newest first. It carries no ticket text and no credential — the
// level is a number and the evidence is counts and PR numbers — and no field of
// it sets the level, which stays the runner autonomy command's.
type autonomyResponse struct {
	Level   int              `json:"level"`
	Allows  string           `json:"allows"`
	Earned  *earnedSnapshot  `json:"earned"`
	History []autonomyChange `json:"history"`
}

// earnedSnapshot is the earned level as last computed: the evidence behind it,
// and the stale mark left by a recomputation that could not store one.
type earnedSnapshot struct {
	Level      int       `json:"level"`
	Stale      bool      `json:"stale"`
	ComputedAt time.Time `json:"computed_at"`
	Evidence   evidence  `json:"evidence"`
}

// autonomyChange is one recorded change of the level: when it happened, the
// levels it moved between, its cause and the evidence at that moment.
type autonomyChange struct {
	At       time.Time `json:"at"`
	From     int       `json:"from"`
	To       int       `json:"to"`
	Cause    string    `json:"cause"`
	Evidence evidence  `json:"evidence"`
}

// evidence is the rewrite rate's numbers: the rate, the PRs it covers and the
// lines behind it, with the owner's lines split by the Rework label that marks
// them.
type evidence struct {
	Rate             string `json:"rate"`
	PRs              []int  `json:"prs"`
	AgentLines       int64  `json:"agent_lines"`
	OwnerLines       int64  `json:"owner_lines"`
	ReviewFoundLines int64  `json:"review_found_lines"`
	OwnerChangeLines int64  `json:"owner_change_lines"`
	UnlabelledLines  int64  `json:"unlabelled_lines"`
	Reverts          int    `json:"reverts"`
	Unavailable      bool   `json:"unavailable"`
}

// renderAutonomy renders the store's view as JSON. The history is rendered
// whether or not it holds anything, so the view reads an empty list rather than
// a null; the earned snapshot is null while none is stored.
func renderAutonomy(v store.AutonomyView) autonomyResponse {
	out := autonomyResponse{
		Level:   int(v.Level),
		Allows:  v.Level.Describe(),
		History: make([]autonomyChange, 0, len(v.History)),
	}
	if v.HasEarned {
		out.Earned = &earnedSnapshot{
			Level:      int(v.Earned.Level),
			Stale:      v.Earned.Stale,
			ComputedAt: v.Earned.ComputedAt,
			Evidence:   renderEvidence(v.Earned.Numbers),
		}
	}
	for _, c := range v.History {
		out.History = append(out.History, autonomyChange{
			At:       c.At,
			From:     int(c.From),
			To:       int(c.To),
			Cause:    c.Cause,
			Evidence: renderEvidence(c.Numbers),
		})
	}
	return out
}

// renderEvidence renders the numbers a level was computed from, with the rate as
// the view prints it.
func renderEvidence(n dispatcher.Numbers) evidence {
	prs := n.PRs
	if prs == nil {
		prs = []int{}
	}
	return evidence{
		Rate:             rateText(n.Rate),
		PRs:              prs,
		AgentLines:       n.AgentLines,
		OwnerLines:       n.OwnerLines,
		ReviewFoundLines: n.ReviewFoundLines,
		OwnerChangeLines: n.OwnerChangeLines,
		UnlabelledLines:  n.UnlabelledLines,
		Reverts:          n.Reverts,
		Unavailable:      n.Unavailable,
	}
}

// rateText is the rewrite rate as the endpoint prints it: a percentage, or the
// words for a rate measured over none of the agent's lines at all, which is
// +Inf and has no percentage to print.
func rateText(rate float64) string {
	if math.IsInf(rate, 1) {
		return "no agent lines"
	}
	return fmt.Sprintf("%.1f%%", rate*100)
}

// spaHandler serves index.html for client-side routes, but 404s a missing path
// with a file extension — a missing asset, not a route.
func spaHandler(spa fs.FS) http.Handler {
	files := http.FileServerFS(spa)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Path[1:]
		if name != "" {
			if _, err := fs.Stat(spa, name); err == nil {
				files.ServeHTTP(w, r)
				return
			}
			if path.Ext(name) != "" {
				http.NotFound(w, r)
				return
			}
		}
		http.ServeFileFS(w, r, spa, "index.html")
	})
}
