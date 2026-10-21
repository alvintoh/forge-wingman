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
	"strings"
	"syscall"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/providers"
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

	// The comparison, providers, autonomy and peak endpoints read past runs,
	// the owner's plan records, the measured merges and the peak mode, so they
	// need the store. A local run without GOOGLE_CLOUD_PROJECT has none and
	// serves the SPA alone, answering those endpoints with 503.
	var comparison ComparisonReader
	var providerPlans ProviderPlansReader
	var measured MeasuredReader
	var peak PeakModeStore
	var autoMerge AutoMergeStore
	var projects ProjectStores
	if project := os.Getenv("GOOGLE_CLOUD_PROJECT"); project != "" {
		fsc, err := firestore.NewClient(context.Background(), project)
		if err != nil {
			return fmt.Errorf("firestore client: %w", err)
		}
		defer func() { _ = fsc.Close() }()
		comparison = store.NewRecords(fsc)
		providerPlans = store.NewProviderPlans(fsc)
		measured = store.NewRewrites(fsc)
		peak = store.NewPeakModes(fsc)
		autoMerge = store.NewAutoMergeSwitches(fsc)
		// A project's phase is requested on its document in the owner database.
		owner, err := firestore.NewClientWithDatabase(context.Background(), project, store.OwnerDatabase)
		if err != nil {
			return fmt.Errorf("owner database client: %w", err)
		}
		defer func() { _ = owner.Close() }()
		phases, queue, reaches := store.NewPhases(owner), store.NewQueue(fsc), store.NewReaches(fsc)
		projects = ProjectStores{
			Projects: phases, Snapshots: store.NewPhaseSnapshots(fsc), Reaches: reaches,
			Loads: queue, Activity: queue, Starts: phases,
		}
	}

	deps := Deps{
		SPA:           spa,
		Logger:        logger,
		Comparison:    comparison,
		ProviderPlans: providerPlans,
		Measured:      measured,
		Peak:          peak,
		AutoMerge:     autoMerge,
		Projects:      projects,
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           newMux(deps),
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

// MeasuredReader reads the measured merges the autonomy view's figures are
// computed from: every merged runner PR, newest merge first. It reads only; the
// measurement is written by the dispatcher's poll.
type MeasuredReader interface {
	Recent(ctx context.Context, limit int) ([]dispatcher.Sample, error)
}

// PeakModeStore reads and sets what the peak view shows: whether a run waits out
// its model's peak-price hours. Unlike the readers above it writes, since the
// view's control sets the mode; the service sits behind IAP, whose only
// authorised principal is the owner, so those are the owner's changes alone.
type PeakModeStore interface {
	Mode(ctx context.Context) (dispatcher.PeakMode, error)
	Set(ctx context.Context, mode dispatcher.PeakMode, at time.Time) error
}

// AutoMergeStore reads and sets the auto-merge switch: the dispatch/auto-merge
// document the runner's pr-meta reads and the runner auto-merge command sets.
// Like the peak mode's, the view's control sets it and the service sits behind
// IAP, whose only authorised principal is the owner.
type AutoMergeStore interface {
	Enabled(ctx context.Context) (bool, error)
	Set(ctx context.Context, enabled bool, at time.Time) error
}

// Deps are what the surface serves: the SPA it embeds, the stores behind its
// JSON endpoints, and where it reports. A nil store serves every endpoint but
// its own, which answers 503 for want of one.
type Deps struct {
	SPA           fs.FS
	Logger        *slog.Logger
	Comparison    ComparisonReader
	ProviderPlans ProviderPlansReader
	Measured      MeasuredReader
	Peak          PeakModeStore
	AutoMerge     AutoMergeStore
	Projects      ProjectStores
}

// newMux claims /api/ explicitly so an unknown endpoint 404s instead of
// falling through to the SPA's index.html.
func newMux(d Deps) *http.ServeMux {
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
		if d.Comparison == nil {
			http.Error(w, "the comparison needs a run store", http.StatusServiceUnavailable)
			return
		}
		cmp, err := d.Comparison.Comparison(r.Context(), time.Now().Add(-runner.TrailingWindow))
		if err != nil {
			d.Logger.Error("comparisonNotRead", "err", err.Error())
			http.Error(w, "reading the comparison", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(renderComparison(cmp))
	})
	mux.HandleFunc("GET /api/providers", func(w http.ResponseWriter, r *http.Request) {
		if d.ProviderPlans == nil {
			http.Error(w, "the providers view needs a plan store", http.StatusServiceUnavailable)
			return
		}
		plans, err := d.ProviderPlans.Providers(r.Context(), time.Now())
		if err != nil {
			d.Logger.Error("providersNotRead", "err", err.Error())
			http.Error(w, "reading the plans", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(renderProviders(plans))
	})
	mux.HandleFunc("GET /api/autonomy", func(w http.ResponseWriter, r *http.Request) {
		if d.Measured == nil {
			http.Error(w, "the autonomy view needs a store", http.StatusServiceUnavailable)
			return
		}
		measured, err := d.Measured.Recent(r.Context(), measuredShown)
		if err != nil {
			d.Logger.Error("autonomyNotRead", "err", err.Error())
			http.Error(w, "reading the measured merges", http.StatusInternalServerError)
			return
		}
		switchOn, switchRead := d.autoMergeSwitch(r.Context())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(renderAutonomy(measured, switchOn, switchRead))
	})
	mux.HandleFunc("GET /api/peak", func(w http.ResponseWriter, r *http.Request) {
		if d.Peak == nil {
			http.Error(w, "the peak view needs a store", http.StatusServiceUnavailable)
			return
		}
		mode, err := d.Peak.Mode(r.Context())
		if err != nil {
			d.Logger.Error("peakModeNotRead", "err", err.Error())
			http.Error(w, "reading the peak mode", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(renderPeak(mode, time.Now()))
	})
	// Setting the mode is one of this surface's two writes, and it requires a
	// JSON body, which a browser cannot send cross-origin without a preflight,
	// so a page that is not the surface cannot switch the mode with the session
	// cookie IAP establishes.
	mux.HandleFunc("POST /api/peak", func(w http.ResponseWriter, r *http.Request) {
		if d.Peak == nil {
			http.Error(w, "the peak view needs a store", http.StatusServiceUnavailable)
			return
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			http.Error(w, "the peak mode is set with a JSON body", http.StatusUnsupportedMediaType)
			return
		}
		var body struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "the request body is not JSON naming a mode", http.StatusBadRequest)
			return
		}
		mode := dispatcher.PeakMode(body.Mode)
		if !mode.Valid() {
			http.Error(w, fmt.Sprintf("mode %q is not %s or %s", body.Mode, dispatcher.PeakOffPeakOnly, dispatcher.PeakAlways), http.StatusBadRequest)
			return
		}
		now := time.Now()
		if err := d.Peak.Set(r.Context(), mode, now); err != nil {
			d.Logger.Error("peakModeNotSet", "err", err.Error())
			http.Error(w, "setting the peak mode", http.StatusInternalServerError)
			return
		}
		d.Logger.Info("peakModeSet", "mode", string(mode))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(renderPeak(mode, now))
	})
	// Setting the switch is this surface's other write, gated as the peak
	// mode's is: a JSON body names the setting and nothing else, and the write
	// reaches the one document its store holds.
	mux.HandleFunc("POST /api/auto-merge", func(w http.ResponseWriter, r *http.Request) {
		if d.AutoMerge == nil {
			http.Error(w, "the auto-merge switch needs a store", http.StatusServiceUnavailable)
			return
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			http.Error(w, "the auto-merge switch is set with a JSON body", http.StatusUnsupportedMediaType)
			return
		}
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Enabled == nil {
			http.Error(w, "the request body is not JSON saying whether auto-merge is enabled", http.StatusBadRequest)
			return
		}
		if err := d.AutoMerge.Set(r.Context(), *body.Enabled, time.Now()); err != nil {
			d.Logger.Error("autoMergeSwitchNotSet", "err", err.Error())
			http.Error(w, "setting the auto-merge switch", http.StatusInternalServerError)
			return
		}
		d.Logger.Info("autoMergeSwitchSet", "enabled", *body.Enabled)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Enabled bool `json:"enabled"`
		}{*body.Enabled})
	})
	mux.HandleFunc("GET /api/projects", d.projects)
	mux.HandleFunc("POST /api/projects/start", d.startProject)
	mux.Handle("GET /api/", http.NotFoundHandler())
	mux.Handle("GET /", spaHandler(d.SPA))

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
	// MinutesPerTicket is the mean wall-clock minutes of those runs, from the
	// build's start to the run's settlement.
	MinutesPerTicket float64 `json:"minutes_per_ticket"`
	// RewriteRate is the model's rewrite rate as the view prints it — a
	// percentage, or the words for a rate measured over none of the agent's
	// lines — and RewriteSample how many of its runs a merge measured.
	RewriteRate   string `json:"rewrite_rate"`
	RewriteSample int    `json:"rewrite_sample"`
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
		// A model no merge has measured has a zero rate, which would print as a
		// clean 0.0%.
		rate := "not measured"
		if m.RewriteSample > 0 {
			rate = rateText(m.RewriteRate)
		}
		out.Models = append(out.Models, modelComparison{
			Model: m.Model, Tickets: m.Tickets, Passed: m.Passed, RewriteSample: m.RewriteSample,
			CostPerTicketUSD: m.CostPerTicket.USD(), MinutesPerTicket: m.MinutesPerTicket,
			RewriteRate: rate,
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

// measuredShown is how many measured merges the autonomy view reads, newest
// first: enough for the tiles, the rolling series and the split by size and
// repository a page draws.
const measuredShown = 200

// autonomyResponse is the autonomy endpoint's wire shape: whether auto-merge is
// on, with the rate's verdict and the reason, and the rewrite figures of the
// measured merges. It carries no ticket text and no credential — the evidence is
// counts, PR numbers and links, run ids and repository names — and no field of it
// sets anything; the switch is set through its own endpoint.
type autonomyResponse struct {
	// AutoMerge is the owner's switch beside the rate's verdict, the two facts
	// the effective state needs both of.
	AutoMerge autoMergeState `json:"auto_merge"`
	// Rewrite is the measured merges' own figures: the rates a page draws.
	Rewrite rewriteView `json:"rewrite"`
}

// renderAutonomy renders the auto-merge state and the rewrite figures of the
// measured merges as JSON. switchRead is false for a switch that could not be
// read, which reports off whatever switchOn holds.
func renderAutonomy(samples []dispatcher.Sample, switchOn, switchRead bool) autonomyResponse {
	return autonomyResponse{
		AutoMerge: renderAutoMerge(samples, switchOn, switchRead),
		Rewrite:   renderRewrite(samples),
	}
}

// rateNoAgentLines is what a rate over none of the agent's lines prints as.
const rateNoAgentLines = "no agent lines"

// linesRateText is rateText for a rate pooled over agentLines of the agent's:
// with none there is nothing measured, whatever the rate reads.
func linesRateText(agentLines int64, rate float64) string {
	if agentLines <= 0 {
		return rateNoAgentLines
	}
	return rateText(rate)
}

// rateText is the rewrite rate as the endpoint prints it: a percentage, or the
// words for a rate measured over none of the agent's lines at all, which is
// +Inf and has no percentage to print.
func rateText(rate float64) string {
	if math.IsInf(rate, 1) {
		return rateNoAgentLines
	}
	return fmt.Sprintf("%.1f%%", rate*100)
}

// autoMergeState is the owner's auto-merge switch beside the rate's verdict, the
// two facts the effective state needs both of, computed here so the page decides
// nothing.
type autoMergeState struct {
	// SwitchOn is the owner's setting, on while no document is stored.
	SwitchOn bool `json:"switch_on"`
	// RateAllows is the rate's verdict alone, the dispatcher's own rule.
	RateAllows bool `json:"rate_allows"`
	// RateValue is the trailing rate as the view prints it.
	RateValue string `json:"rate_value"`
	// Limit is the bar the rate is compared to.
	Limit string `json:"limit"`
	// Window is how many trailing merged PRs the rate covers.
	Window int `json:"window"`
	// Effective is the switch on and the rate allowing it.
	Effective bool `json:"effective"`
	// Reason is why auto-merge is not effective: the rule's own reason, the
	// switch being off, or the switch being unreadable.
	Reason string `json:"reason"`
}

// renderAutoMerge renders the owner's switch beside the rate's verdict over the
// measured merges, taken from the dispatcher's own rule. switchRead is false for
// a switch that could not be read, which reports off whatever switchOn holds.
func renderAutoMerge(samples []dispatcher.Sample, switchOn, switchRead bool) autoMergeState {
	rate := dispatcher.AutoMergeOn(dispatcher.Evidence{Trailing: samples})
	on := switchOn && switchRead
	out := autoMergeState{
		SwitchOn:   on,
		RateAllows: rate.On,
		RateValue:  rate.RateText(),
		Limit:      rateText(dispatcher.AutoMergeRate),
		Window:     dispatcher.TrailingPRs,
		Effective:  on && rate.On,
		Reason:     rate.Reason,
	}
	switch {
	case !switchRead:
		out.Reason = dispatcher.AutoMergeReasonSwitchUnread
	case !on:
		out.Reason = dispatcher.AutoMergeReasonSwitchOff
	}
	return out
}

// autoMergeSwitch reads the owner's auto-merge switch, reporting it and whether
// it could be read. A switch that cannot be read is off and is logged, so the
// view never reports auto-merge on from a switch nobody read.
func (d Deps) autoMergeSwitch(ctx context.Context) (on, read bool) {
	if d.AutoMerge == nil {
		return false, false
	}
	on, err := d.AutoMerge.Enabled(ctx)
	if err != nil {
		d.Logger.Warn("autoMergeSwitchUnread", "err", err.Error())
		return false, false
	}
	return on, true
}

// rewriteView is the measured merges' rewrite figures as a page draws them: the
// pooled rate over each trailing window, the rolling rate behind them, the PRs
// they are measured on, and the same figures split by size and by repository.
// Every headline rate is the pooled owner lines over the agent lines, never a
// mean of the PRs' own rates; a size's median and p90 are the spread of those
// own rates.
type rewriteView struct {
	Tiles  []rewriteTile  `json:"tiles"`
	Series []rewritePoint `json:"series"`
	PRs    []rewritePR    `json:"prs"`
	BySize []sizeRate     `json:"by_size"`
	ByRepo []repoRewrite  `json:"by_repo"`
}

// rewriteTile is one trailing window: the PRs it covers, the count it asked for
// — which a shorter history may not reach — and the rate pooled over them.
type rewriteTile struct {
	PRs        []int  `json:"prs"`
	Asked      int    `json:"asked"`
	AgentLines int64  `json:"agent_lines"`
	OwnerLines int64  `json:"owner_lines"`
	Rate       string `json:"rate"`
}

// rewritePoint is one point of the rolling series: the merge the window ends at
// and the rate pooled over that window.
type rewritePoint struct {
	PR       int       `json:"pr"`
	MergedAt time.Time `json:"merged_at"`
	Rate     string    `json:"rate"`
}

// rewritePR is one merged runner PR as the list shows it, with its own rate
// beside the lines the rate is read from.
type rewritePR struct {
	PR         int       `json:"pr"`
	Run        string    `json:"run"`
	Repo       string    `json:"repo"`
	Size       string    `json:"size"`
	Title      string    `json:"title"`
	AgentLines int64     `json:"agent_lines"`
	OwnerLines int64     `json:"owner_lines"`
	Rate       string    `json:"rate"`
	MergedAt   time.Time `json:"merged_at"`
	PRURL      string    `json:"pr_url"`
}

// sizeRate is one size's figures: its count, the rate pooled over its PRs, and
// the median and p90 of their own rates — null below the PRs a rate needs, and
// for a size whose PRs have no rate of their own, since a rate of zero is a real
// measurement.
type sizeRate struct {
	Size   string  `json:"size"`
	PRs    int     `json:"prs"`
	Rate   *string `json:"rate"`
	Median *string `json:"median"`
	P90    *string `json:"p90"`
}

// repoRewrite is one repository's own windows and rolling rate.
type repoRewrite struct {
	Repo   string         `json:"repo"`
	Tiles  []rewriteTile  `json:"tiles"`
	Series []rewritePoint `json:"series"`
}

// renderRewrite renders the rewrite figures of the measured merges, newest
// first. Every list is rendered whether or not it holds anything, so a view
// narrowing to one of them reads an empty list rather than a null.
func renderRewrite(samples []dispatcher.Sample) rewriteView {
	return rewriteView{
		Tiles:  renderTiles(dispatcher.Tiles(samples)),
		Series: renderSeries(dispatcher.RollingSeries(samples, dispatcher.TrailingPRs, dispatcher.SeriesPoints)),
		PRs:    renderPRs(samples),
		BySize: renderSizes(dispatcher.BySize(samples)),
		ByRepo: renderRepos(dispatcher.ByRepo(samples)),
	}
}

// renderTiles renders each trailing window, its rate as the view prints it.
func renderTiles(windows []dispatcher.RateWindow) []rewriteTile {
	out := make([]rewriteTile, 0, len(windows))
	for _, w := range windows {
		prs := w.PRs
		if prs == nil {
			prs = []int{}
		}
		out = append(out, rewriteTile{
			PRs:        prs,
			Asked:      w.Asked,
			AgentLines: w.Lines.AgentLines,
			OwnerLines: w.Lines.OwnerLines,
			Rate:       linesRateText(w.Lines.AgentLines, w.Rate),
		})
	}
	return out
}

// renderSeries renders the rolling rate, oldest merge first.
func renderSeries(points []dispatcher.SeriesPoint) []rewritePoint {
	out := make([]rewritePoint, 0, len(points))
	for _, p := range points {
		out = append(out, rewritePoint{PR: p.PR, MergedAt: p.MergedAt, Rate: rateText(p.Rate)})
	}
	return out
}

// renderPRs renders the newest merged PRs, up to the trailing window the tiles
// read over.
func renderPRs(samples []dispatcher.Sample) []rewritePR {
	shown := samples
	if len(shown) > dispatcher.TrailingPRs {
		shown = shown[:dispatcher.TrailingPRs]
	}
	out := make([]rewritePR, 0, len(shown))
	for _, s := range shown {
		out = append(out, rewritePR{
			PR:         s.PR,
			Run:        s.Run,
			Repo:       s.Repo,
			Size:       dispatcher.SizeBucket(s.Size),
			Title:      s.Title,
			AgentLines: s.AgentLines,
			OwnerLines: s.OwnerLines,
			Rate:       linesRateText(s.AgentLines, s.Rate()),
			MergedAt:   s.MergedAt,
			PRURL:      s.PRURL,
		})
	}
	return out
}

// renderSizes renders each size's figures, withholding a rate below the PRs it
// needs and a spread the size has none of, so a withheld figure reads as absent
// rather than as a clean zero.
func renderSizes(rows []dispatcher.SizeRow) []sizeRate {
	out := make([]sizeRate, 0, len(rows))
	for _, r := range rows {
		entry := sizeRate{Size: r.Size, PRs: r.PRs}
		if r.Rated {
			entry.Rate = ratePtr(r.Rate)
		}
		if r.Spread {
			entry.Median, entry.P90 = ratePtr(r.Median), ratePtr(r.P90)
		}
		out = append(out, entry)
	}
	return out
}

// renderRepos renders each repository's own windows and rolling rate.
func renderRepos(rows []dispatcher.RepoRow) []repoRewrite {
	out := make([]repoRewrite, 0, len(rows))
	for _, r := range rows {
		out = append(out, repoRewrite{Repo: r.Repo, Tiles: renderTiles(r.Tiles), Series: renderSeries(r.Series)})
	}
	return out
}

// ratePtr returns the rate as the view prints it, for a figure that carries one.
func ratePtr(rate float64) *string {
	text := rateText(rate)
	return &text
}

// peakResponse is the peak endpoint's wire shape: the mode runs wait out their
// model's peak-price hours under, whether admission is holding a run now, and
// every model a run may use with the window its rate prices peak hours in.
type peakResponse struct {
	Mode string `json:"mode"`
	// Holding is true while admission is holding a run out of its model's peak
	// window: the mode holds runs and a window is open.
	Holding bool `json:"holding"`
	// Models is every model a run may use, whether or not it prices peak hours,
	// so the view can show a flat-priced model as such.
	Models []peakModel `json:"models"`
}

type peakModel struct {
	Model string `json:"model"`
	// Peak is the window the model's rate prices peak hours in, null for a model
	// priced the same at every hour and for one the plan does not price.
	Peak *peakWindow `json:"peak"`
	// Priced is whether the plan has a rate card for the model at all, so the
	// view tells a model priced the same at every hour from one it never prices.
	Priced bool `json:"priced"`
}

type peakWindow struct {
	Multiplier float64 `json:"multiplier"`
	// Hours are the weekday UTC hour ranges the multiplier applies in, each
	// [from, to).
	Hours []peakHour `json:"hours"`
}

type peakHour struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// renderPeak renders the peak view as JSON at now, converting each model's rate
// card to the window the view displays. Every model is rendered whether or not
// it prices peak hours, so a view narrowing to the peak-priced ones reads an
// empty list rather than a null.
func renderPeak(mode dispatcher.PeakMode, now time.Time) peakResponse {
	models := providers.RunModels()
	out := peakResponse{
		Mode:   string(mode),
		Models: make([]peakModel, 0, len(models)),
	}
	for _, model := range models {
		rates, priced := providers.RatesFor(model)
		entry := peakModel{Model: model, Priced: priced}
		if priced && rates.Peak != nil {
			window := peakWindow{Multiplier: rates.Peak.Multiplier, Hours: make([]peakHour, 0, len(rates.Peak.Hours))}
			for _, span := range rates.Peak.Hours {
				window.Hours = append(window.Hours, peakHour{From: span.From, To: span.To})
			}
			entry.Peak = &window
		}
		out.Models = append(out.Models, entry)
	}
	out.Holding = holdingNow(mode, out.Models, now)
	return out
}

// holdingNow reports whether some model a run may use is inside its own peak
// window right now. It asks as a run of no estimated minutes does, so it answers
// the narrower window-open question: admission also holds a run whose estimate
// reaches the next window, which this view does not read.
func holdingNow(mode dispatcher.PeakMode, models []peakModel, now time.Time) bool {
	for _, model := range models {
		if dispatcher.PeakHeld(mode, model.Model, now, 0) {
			return true
		}
	}
	return false
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
