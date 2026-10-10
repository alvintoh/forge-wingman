// Command surface serves the embedded SPA and the JSON endpoints under /api.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path"
	"syscall"
	"time"

	"cloud.google.com/go/firestore"

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

	// The comparison endpoint reads past runs, so it needs the run store. A
	// local run without GOOGLE_CLOUD_PROJECT has none and serves the SPA
	// alone, answering that one endpoint with 503.
	var comparison ComparisonReader
	if project := os.Getenv("GOOGLE_CLOUD_PROJECT"); project != "" {
		fsc, err := firestore.NewClient(context.Background(), project)
		if err != nil {
			return fmt.Errorf("firestore client: %w", err)
		}
		defer func() { _ = fsc.Close() }()
		comparison = store.NewRecords(fsc)
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           newMux(spa, logger, comparison),
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

// newMux claims /api/ explicitly so an unknown endpoint 404s instead of
// falling through to the SPA's index.html. A nil comparison serves every
// endpoint but /api/comparison, which has no store to read.
func newMux(spa fs.FS, logger *slog.Logger, comparison ComparisonReader) *http.ServeMux {
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
	mux.Handle("GET /api/", http.NotFoundHandler())
	mux.Handle("GET /", spaHandler(spa))

	return mux
}

// comparisonResponse is the comparison endpoint's wire shape. It carries
// aggregates only: the run records it reads hold ticket titles and bodies, and
// this endpoint is public, so none of them is ever served from here.
type comparisonResponse struct {
	Models    []modelComparison    `json:"models"`
	Providers []providerComparison `json:"providers"`
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

type driftedRun struct {
	RunID     string  `json:"run_id"`
	Provider  string  `json:"provider"`
	Model     string  `json:"model"`
	CostUSD   float64 `json:"cost_usd"`
	CostDrift string  `json:"cost_drift"`
}

// renderComparison renders the store's comparison as JSON, converting its
// exact micro-dollar costs to the dollar figures the endpoint displays.
func renderComparison(c store.Comparison) comparisonResponse {
	out := comparisonResponse{}
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
	for _, d := range c.Drifted {
		out.Drifted = append(out.Drifted, driftedRun{
			RunID: d.RunID, Provider: d.Provider, Model: d.Model,
			CostUSD: d.Cost.USD(), CostDrift: d.CostDrift,
		})
	}
	return out
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
