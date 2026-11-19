package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

// PollIntervalStore reads and sets the owner's poll interval, in minutes.
type PollIntervalStore interface {
	Value(ctx context.Context) (int, error)
	Set(ctx context.Context, n int, at time.Time) error
}

// pollIntervalResponse is the poll-interval endpoint's wire shape.
type pollIntervalResponse struct {
	PollMinutes int `json:"poll_minutes"`
}

// writePollInterval answers with the interval.
func writePollInterval(w http.ResponseWriter, n int) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(pollIntervalResponse{PollMinutes: n})
}

// pollInterval serves GET /api/poll-interval: the saved interval, or the
// default while the owner has not saved one.
func (d Deps) pollInterval(w http.ResponseWriter, r *http.Request) {
	if d.PollInterval == nil {
		http.Error(w, "the poll interval needs a store", http.StatusServiceUnavailable)
		return
	}
	n, err := d.PollInterval.Value(r.Context())
	if err != nil {
		d.Logger.Error("pollIntervalNotRead", "err", err.Error())
		http.Error(w, "reading the poll interval", http.StatusInternalServerError)
		return
	}
	writePollInterval(w, n)
}

// setPollInterval serves POST /api/poll-interval, gated as the runs-per-repo
// cap's write is: a JSON body names the setting and nothing else, which a page
// that is not the surface cannot send cross-origin without a preflight.
func (d Deps) setPollInterval(w http.ResponseWriter, r *http.Request) {
	if d.PollInterval == nil {
		http.Error(w, "the poll interval needs a store", http.StatusServiceUnavailable)
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "the poll interval is set with a JSON body", http.StatusUnsupportedMediaType)
		return
	}
	var body struct {
		PollMinutes *int `json:"poll_minutes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.PollMinutes == nil {
		http.Error(w, "the request body is not JSON naming the poll interval", http.StatusBadRequest)
		return
	}
	n := *body.PollMinutes
	if !dispatcher.ValidPollMinutes(n) {
		http.Error(w, fmt.Sprintf("a poll interval of %d minutes is not %d or %d",
			n, dispatcher.FastPollMinutes, dispatcher.DefaultPollMinutes), http.StatusBadRequest)
		return
	}
	if err := d.PollInterval.Set(r.Context(), n, time.Now()); err != nil {
		d.Logger.Error("pollIntervalNotSet", "err", err.Error())
		http.Error(w, "setting the poll interval", http.StatusInternalServerError)
		return
	}
	d.Logger.Info("pollIntervalSet", "poll_minutes", n)
	writePollInterval(w, n)
}
