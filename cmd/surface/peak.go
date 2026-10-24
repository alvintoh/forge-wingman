package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/providers"
)

// PeakModeStore reads and sets what the peak view shows: whether a run waits out
// its model's peak-price hours. Unlike the readers above it writes, since the
// view's control sets the mode; the service sits behind IAP, whose only
// authorised principal is the owner, so those are the owner's changes alone.
type PeakModeStore interface {
	Mode(ctx context.Context) (dispatcher.PeakMode, error)
	Set(ctx context.Context, mode dispatcher.PeakMode, at time.Time) error
}

// peak serves GET /api/peak.
func (d Deps) peak(w http.ResponseWriter, r *http.Request) {
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
}

// setPeak serves POST /api/peak. Setting the mode is one of this surface's two
// writes, and it requires a JSON body, which a browser cannot send cross-origin
// without a preflight, so a page that is not the surface cannot switch the mode
// with the session cookie IAP establishes.
func (d Deps) setPeak(w http.ResponseWriter, r *http.Request) {
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

// renderPeak renders the peak view as JSON at now.
func renderPeak(mode dispatcher.PeakMode, now time.Time) peakResponse {
	models := peakModels()
	return peakResponse{Mode: string(mode), Models: models, Holding: holdingNow(mode, models, now)}
}

// peakModels converts each run model's rate card to the window the views
// display. Every model is rendered whether or not it prices peak hours, so a
// view narrowing to the peak-priced ones reads an empty list rather than a null.
func peakModels() []peakModel {
	models := providers.RunModels()
	out := make([]peakModel, 0, len(models))
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
		out = append(out, entry)
	}
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
