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

// AutoMergeStore reads and sets the auto-merge settings: the dispatch/auto-merge
// document the runner's pr-meta reads and the runner auto-merge command sets.
// Like the peak mode's, the Configuration page's switch sets it and the service
// sits behind IAP, whose only authorised principal is the owner.
type AutoMergeStore interface {
	Read(ctx context.Context) (store.AutoMergeSetting, error)
	SetEnabled(ctx context.Context, enabled bool, at time.Time) error
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
	setting, readErr := d.autoMergeReading(r.Context())
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(renderAutonomy(measured, repo, setting, readErr))
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
	if err := d.AutoMerge.SetEnabled(r.Context(), *body.Enabled, time.Now()); err != nil {
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
// first: enough for the tiles, the rolling series, the split by size, model and
// repository a page draws, and the list of every PR behind them. A repository's
// scoped figures filter this same read, so they cover only its merges among these.
const measuredShown = 200

// autonomyResponse is the autonomy endpoint's wire shape: whether auto-merge is
// on, with the rate's verdict and the reason, and the rewrite figures of the
// measured merges, or of one repository's when the request names it. It carries
// no ticket text and no credential — the evidence is counts, PR numbers and
// links, run ids and repository names — and no field of it sets anything; the
// switch is set through its own endpoint.
type autonomyResponse struct {
	// AutoMerge is the owner's switch beside the rate's verdict; both gate auto-merge.
	AutoMerge autoMergeState `json:"auto_merge"`
	// Rewrite is the measured merges' own figures, the rates a page draws, or
	// one repository's alone when the request names it.
	Rewrite rewriteView `json:"rewrite"`
}

// renderAutonomy renders the auto-merge state and the rewrite figures of the
// measured merges as JSON. The rate reads only known human merges: a PR that
// merged itself and one whose row names no merge account are left out of it. The
// figures drawn beside it cover every measured merge. A repo scopes the figures
// to its merges and never the rate, which reads every merge given.
func renderAutonomy(samples []dispatcher.Sample, repo string, setting store.AutoMergeSetting, readErr error) autonomyResponse {
	human := dispatcher.Evidence{Trailing: samples}.HumanWindow(len(samples))
	shown := samples
	if repo != "" {
		shown = dispatcher.ForRepo(samples, repo)
	}
	return autonomyResponse{
		AutoMerge: renderAutoMerge(human, setting, readErr),
		Rewrite:   renderRewrite(shown),
	}
}

// autoMergeState is the owner's auto-merge settings beside the rewrite rate's
// standing, computed here so the page decides nothing: auto-merge is on while the
// switch is on and readable and no escaped PR holds it. The rate is shown against
// the 20% guideline and holds nothing.
type autoMergeState struct {
	// SwitchOn is the owner's setting, on while no document is stored.
	SwitchOn bool `json:"switch_on"`
	// RateValue is the trailing rate as the view prints it.
	RateValue string `json:"rate_value"`
	// Guideline is the fixed figure the rate is shown against, as the view prints
	// it: 20%. It blocks nothing.
	Guideline string `json:"guideline"`
	// Window is how many trailing PRs a human merged the rate covers.
	Window int `json:"window"`
	// Effective is the switch on and readable, no escaped PR holding it. The rate
	// does not enter it.
	Effective bool `json:"effective"`
	// Reason is the first thing that turns auto-merge off: an unreadable switch,
	// the switch off, or an escaped PR.
	Reason string `json:"reason"`
	// RateReason is the rate's own reason: whether it was read, where it sits
	// against the guideline, or which condition stopped it being computed.
	RateReason string `json:"rate_reason"`
	// EscapedPR, EscapedRepo, EscapedBy and EscapedReason name the auto-merged PR
	// that escaped the owner's reading, zero and empty while none holds.
	EscapedPR     int    `json:"escaped_pr"`
	EscapedRepo   string `json:"escaped_repo"`
	EscapedBy     int    `json:"escaped_by"`
	EscapedReason string `json:"escaped_reason"`
	// EscapeDays is how many days after an auto-merge a revert or a fix still
	// counts as an escape.
	EscapeDays int `json:"escape_days"`
}

// renderAutoMerge renders the auto-merge settings beside the rewrite rate's
// standing over the PRs a human merged, each computed here so the page decides
// nothing.
func renderAutoMerge(samples []dispatcher.Sample, setting store.AutoMergeSetting, readErr error) autoMergeState {
	rate := dispatcher.AutoMergeOn(dispatcher.Evidence{Trailing: samples})
	read := readErr == nil
	on := setting.Enabled && read
	out := autoMergeState{
		SwitchOn:      on,
		RateValue:     rate.RateText(),
		Guideline:     rateText(dispatcher.RateGuideline),
		Window:        dispatcher.TrailingPRs,
		Effective:     on && setting.Escaped.PR == 0,
		Reason:        dispatcher.AutoMergeReasonOn,
		RateReason:    rate.Reason,
		EscapedPR:     setting.Escaped.PR,
		EscapedRepo:   setting.Escaped.Repo,
		EscapedBy:     setting.Escaped.By,
		EscapedReason: setting.Escaped.Reason,
		EscapeDays:    int(dispatcher.EscapeWindow / (24 * time.Hour)),
	}
	// The escape latch is named before the switch, as pr-meta and the inbox
	// name it: the latch is the fact that holds auto-merge off until the owner
	// switches it on again, whether or not the switch is already off.
	switch {
	case readErr != nil:
		out.Reason = dispatcher.AutoMergeReasonSwitchUnread
	case setting.Escaped.PR > 0:
		out.Reason = dispatcher.AutoMergeReasonEscaped
	case !setting.Enabled:
		out.Reason = dispatcher.AutoMergeReasonSwitchOff
	}
	return out
}

// autoMergeReading reads the owner's auto-merge settings, returning the stored
// ones and why they could not be read. A settings read that fails is logged and
// holds auto-merge off, so the view never reports it on from what nobody read.
func (d Deps) autoMergeReading(ctx context.Context) (store.AutoMergeSetting, error) {
	if d.AutoMerge == nil {
		return store.AutoMergeSetting{}, errors.New("the surface holds no auto-merge store")
	}
	setting, err := d.AutoMerge.Read(ctx)
	if err != nil {
		d.Logger.Warn("autoMergeSettingsUnread", "err", err.Error())
	}
	return setting, err
}
