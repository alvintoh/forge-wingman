package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// MeasuredReader reads the measured merges the autonomy view's figures are
// computed from: every merged runner PR, newest merge first. It reads only; the
// measurement is written by the dispatcher's poll.
type MeasuredReader interface {
	Recent(ctx context.Context, limit int) ([]dispatcher.Sample, error)
}

// AutoMergeStore reads and sets the auto-merge switch: the dispatch/auto-merge
// document the runner's pr-meta reads and the runner auto-merge command sets.
// Like the peak mode's, the Configuration page's switch sets it and the service
// sits behind IAP, whose only authorised principal is the owner.
type AutoMergeStore interface {
	Enabled(ctx context.Context) (bool, error)
	Set(ctx context.Context, enabled bool, at time.Time) error
}

// autonomy serves GET /api/autonomy: the auto-merge state, and the rewrite
// figures, which a repo parameter scopes to one repository.
func (d Deps) autonomy(w http.ResponseWriter, r *http.Request) {
	if d.Measured == nil {
		http.Error(w, "the autonomy view needs a store", http.StatusServiceUnavailable)
		return
	}
	repo, err := scopeRepo(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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
	_ = json.NewEncoder(w).Encode(renderAutonomy(measured, repo, switchOn, switchRead))
}

// scopeRepo reads the repository a request scopes the figures to: empty when the
// request has no repo parameter, and an error when it names anything but one
// owner/name repository, an empty or repeated parameter included. A query that
// does not parse is an error too, since the pair it drops may be the repo.
func scopeRepo(r *http.Request) (string, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", errors.New("the query does not parse")
	}
	values, named := query["repo"]
	if !named {
		return "", nil
	}
	if len(values) != 1 || len(values[0]) > maxRepository || !store.ValidRepository(values[0]) {
		return "", errors.New("repo is not one owner/name repository")
	}
	return values[0], nil
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
// first: enough for the tiles, the rolling series and the split by size, model
// and repository a page draws. A repository's scoped figures filter this same
// read, so they cover only its merges among these.
const measuredShown = 200

// autonomyResponse is the autonomy endpoint's wire shape: whether auto-merge is
// on, with the rate's verdict and the reason, and the rewrite figures of the
// measured merges, or of one repository's when the request names it. It carries
// no ticket text and no credential — the evidence is counts, PR numbers and
// links, run ids and repository names — and no field of it sets anything; the
// switch is set through its own endpoint.
type autonomyResponse struct {
	// AutoMerge is the owner's switch beside the rate's verdict, which the page
	// words as information beside it.
	AutoMerge autoMergeState `json:"auto_merge"`
	// Rewrite is the measured merges' own figures, the rates a page draws, or
	// one repository's alone when the request names it.
	Rewrite rewriteView `json:"rewrite"`
}

// renderAutonomy renders the auto-merge state and the rewrite figures of the
// measured merges as JSON. A repo scopes the rewrite figures to its merges and
// never the rate's verdict, which reads every merge given. switchRead is false
// for a switch that could not be read, which reports off whatever switchOn holds.
func renderAutonomy(samples []dispatcher.Sample, repo string, switchOn, switchRead bool) autonomyResponse {
	shown := samples
	if repo != "" {
		shown = dispatcher.ForRepo(samples, repo)
	}
	return autonomyResponse{
		AutoMerge: renderAutoMerge(samples, switchOn, switchRead),
		Rewrite:   renderRewrite(shown),
	}
}

// autoMergeState is the owner's auto-merge switch beside the rate's verdict,
// which the switch alone makes effective, computed here so the page decides
// nothing.
type autoMergeState struct {
	// SwitchOn is the owner's setting, on while no document is stored.
	SwitchOn bool `json:"switch_on"`
	// RateAllows is whether the rate is within its bar. It is information, and
	// it does not change Effective.
	RateAllows bool `json:"rate_allows"`
	// RateValue is the trailing rate as the view prints it.
	RateValue string `json:"rate_value"`
	// Limit is the bar the rate is compared to.
	Limit string `json:"limit"`
	// Window is how many trailing merged PRs the rate covers.
	Window int `json:"window"`
	// Effective is the switch on and readable. The rate does not enter it.
	Effective bool `json:"effective"`
	// Reason is switch-unread for a switch that could not be read and switch-off
	// for one that is off; for a switch that is on it is the rate's own reason,
	// which Effective does not follow.
	Reason string `json:"reason"`
}

// renderAutoMerge renders the owner's switch beside the rate's verdict over the
// measured merges. The switch alone decides Effective; the rate is served as
// information. switchRead is false for a switch that could not be read, which
// reports off whatever switchOn holds.
func renderAutoMerge(samples []dispatcher.Sample, switchOn, switchRead bool) autoMergeState {
	rate := dispatcher.AutoMergeOn(dispatcher.Evidence{Trailing: samples})
	on := switchOn && switchRead
	out := autoMergeState{
		SwitchOn:   on,
		RateAllows: rate.WithinBar,
		RateValue:  rate.RateText(),
		Limit:      rateText(dispatcher.AutoMergeRate),
		Window:     dispatcher.TrailingPRs,
		Effective:  on,
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
