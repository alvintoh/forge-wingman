// Command surface serves the embedded SPA and the JSON endpoints under /api.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cloud.google.com/go/firestore"
	"golang.org/x/sync/singleflight"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
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

	// The comparison, run history, providers, autonomy and peak endpoints read
	// past runs, the owner's plan records, the measured merges and the peak mode, so they
	// need the store. A local run without GOOGLE_CLOUD_PROJECT has none and
	// serves the SPA alone, answering those endpoints with 503.
	var comparison ComparisonReader
	var providerPlans ProviderPlansReader
	var measured MeasuredReader
	var peak PeakModeStore
	var autoMerge AutoMergeStore
	var settings SettingsStore
	var projects ProjectStores
	var inbox InboxStore
	var runs MergeRuns
	var cancels CancelRuns
	var history RunHistoryReader
	var retries RunRetries
	if project := os.Getenv("GOOGLE_CLOUD_PROJECT"); project != "" {
		fsc, err := firestore.NewClient(context.Background(), project)
		if err != nil {
			return fmt.Errorf("firestore client: %w", err)
		}
		defer func() { _ = fsc.Close() }()
		records := store.NewRecords(fsc)
		comparison, inbox, runs, history = records, records, records, records
		cancels = records
		providerPlans = store.NewProviderPlans(fsc)
		measured = store.NewRewrites(fsc)
		peak = store.NewPeakModes(fsc)
		autoMerge = store.NewAutoMergeSwitches(fsc)
		settings = store.NewSettings(fsc)
		// A project's phase is requested, and a project added, in the owner database.
		owner, err := firestore.NewClientWithDatabase(context.Background(), project, store.OwnerDatabase)
		if err != nil {
			return fmt.Errorf("owner database client: %w", err)
		}
		defer func() { _ = owner.Close() }()
		phases, queue, reaches := store.NewPhases(owner), store.NewQueue(fsc), store.NewReaches(fsc)
		retries = queue
		projects = ProjectStores{
			Projects: phases, Snapshots: store.NewPhaseSnapshots(fsc), Reaches: reaches,
			Loads: queue, Activity: queue, Starts: phases, Pauses: phases,
			Adds: store.NewProjects(owner), Names: store.NewProjectNames(fsc),
		}
	}

	// Merging needs the surface's own GitHub token, which no run job holds.
	// Without it the merge route answers 503.
	var merger PRMerger
	var pulls PullLister
	if token := os.Getenv("WINGMAN_MERGE_TOKEN"); token != "" {
		gh := dispatcher.GitHub{Token: token}
		merger, pulls = gh, gh
	}

	deps := Deps{
		SPA:           spa,
		Logger:        logger,
		Comparison:    comparison,
		ProviderPlans: providerPlans,
		Measured:      measured,
		Peak:          peak,
		AutoMerge:     autoMerge,
		Settings:      settings,
		PlatformCap:   platformCap(os.Getenv),
		Projects:      projects,
		Inbox:         inbox,
		Runs:          runs,
		Cancels:       cancels,
		History:       history,
		Retries:       retries,
		Merger:        merger,
		Pulls:         pulls,
	}

	handler, err := gate(context.Background(), logger, newMux(deps))
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
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
	Settings      SettingsStore
	Projects      ProjectStores
	Inbox         InboxStore
	Runs          MergeRuns
	Cancels       CancelRuns
	History       RunHistoryReader
	Retries       RunRetries
	Merger        PRMerger
	Pulls         PullLister
	// PlatformCap is the GitHub job limit the Config settings modal shows; zero
	// takes the default.
	PlatformCap int
}

// newMux claims /api/ explicitly so an unknown endpoint 404s instead of
// falling through to the SPA's index.html.
func newMux(d Deps) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("GET /api/version", version)
	mux.HandleFunc("GET /api/me", me)
	mux.HandleFunc("GET /api/comparison", d.comparison)
	mux.HandleFunc("GET /api/providers", d.providers)
	mux.HandleFunc("GET /api/run-history", d.runHistory)
	mux.HandleFunc("GET /api/autonomy", d.autonomy)
	mux.HandleFunc("GET /api/peak", d.peak)
	mux.HandleFunc("POST /api/peak", d.setPeak)
	mux.HandleFunc("POST /api/auto-merge", d.setAutoMerge)
	mux.HandleFunc("GET /api/settings", d.settings)
	mux.HandleFunc("POST /api/settings/runs-per-repo", d.setRunsPerRepo)
	mux.HandleFunc("GET /api/projects", d.projects)
	mux.HandleFunc("POST /api/projects/start", d.startProject)
	mux.HandleFunc("POST /api/projects/stop", d.stopProject)
	mux.HandleFunc("POST /api/projects/resume", d.resumeProject)
	mux.HandleFunc("POST /api/projects/add", d.addProject)
	mux.HandleFunc("GET /api/projects/options", d.projectOptions)
	var inboxFlight singleflight.Group
	mux.HandleFunc("GET /api/inbox", d.inbox(&inboxFlight))
	mux.HandleFunc("POST /api/runs/{run}/merge", d.mergeRun)
	mux.HandleFunc("POST /api/runs/{run}/retry", d.retryRun)
	mux.HandleFunc("POST /api/runs/{run}/cancel", d.cancelRun)
	mux.Handle("GET /api/", http.NotFoundHandler())
	mux.Handle("GET /", spaHandler(d.SPA))

	return mux
}
