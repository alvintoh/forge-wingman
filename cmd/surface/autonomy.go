package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

// MeasuredReader reads the measured merges the autonomy view's figures are
// computed from: every merged runner PR, newest merge first. It reads only; the
// measurement is written by the dispatcher's poll.
type MeasuredReader interface {
	Recent(ctx context.Context, limit int) ([]dispatcher.Sample, error)
}

// AutoMergeStore reads and sets the auto-merge switch: the dispatch/auto-merge
// document the runner's pr-meta reads and the runner auto-merge command sets.
// Like the peak mode's, the view's control sets it and the service sits behind
// IAP, whose only authorised principal is the owner.
type AutoMergeStore interface {
	Enabled(ctx context.Context) (bool, error)
	Set(ctx context.Context, enabled bool, at time.Time) error
}

// autonomy serves GET /api/autonomy.
func (d Deps) autonomy(w http.ResponseWriter, r *http.Request) {
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
}

// setAutoMerge serves POST /api/auto-merge. Setting the switch is this
// surface's other write, gated as the peak mode's is: a JSON body names the
// setting and nothing else, and the write reaches the one document its store
// holds.
func (d Deps) setAutoMerge(w http.ResponseWriter, r *http.Request) {
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
