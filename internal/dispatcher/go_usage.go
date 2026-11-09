package dispatcher

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// GoUsagePlan is the paid plan whose live usage GoUsageEndpoint reports.
const GoUsagePlan = "opencode-go"

// GoUsageEndpoint is the plan's usage route: first-party but undocumented, and
// the only source of the usage a plan's own fixed limits cannot see. The vendor's
// own client reads it, and on 2026-11-09 it answered {"usage":{"rolling":…,
// "weekly":…,"monthly":…}} with a status, a whole-number percent and an ISO
// resetsAt in each window.
const GoUsageEndpoint = "https://opencode.ai/zen/go/v1/usage"

// CeilingGoUsage names the hold a spent OpenCode Go window puts on a run; the
// binding carries the window's name and its reset time after it.
const CeilingGoUsage = "go-usage"

// GoUsageHeldPercent is the share of a window at which admission holds a run.
const GoUsageHeldPercent = 90

// GoUsageRateLimited is the window status the route reports once the vendor has
// stopped serving it.
const GoUsageRateLimited = "rate-limited"

const (
	goUsageOKStatus = "ok"
	// goUsageSession attributes the read to this dispatcher, as the route
	// requires a session header.
	goUsageSession = "forge-wingman-dispatcher"
	// DefaultGoUsageTimeout bounds one read, below the dispatcher's own
	// request timeout so a hung route cannot outlast the poll.
	DefaultGoUsageTimeout = 5 * time.Second
	goUsageMaxBody        = 1 << 20
)

// GoUsageReader reads the OpenCode Go plan's live window usage.
type GoUsageReader interface {
	Read(ctx context.Context) (GoUsage, error)
}

// GoUsage is the plan's windows as the usage route reports them.
type GoUsage struct {
	Windows []GoUsageWindow
}

// GoUsageWindow is one of the plan's windows: the vendor's own status, the
// share of it used, and when it next resets.
type GoUsageWindow struct {
	Name     string
	Status   string
	Percent  float64
	ResetsAt time.Time
}

// SpentBinding names the window withholding a run at at, empty when every
// window has room: the window that frees last, since that is when the run can
// start. A window whose reset has passed holds nothing, so a run is admitted on
// the first poll after its window resets with no owner action.
func (u GoUsage) SpentBinding(at time.Time) string {
	var held *GoUsageWindow
	for i := range u.Windows {
		w := &u.Windows[i]
		if !w.ResetsAt.After(at) {
			continue
		}
		if w.Status != GoUsageRateLimited && w.Percent < GoUsageHeldPercent {
			continue
		}
		if held == nil || w.ResetsAt.After(held.ResetsAt) {
			held = w
		}
	}
	if held == nil {
		return ""
	}
	return CeilingGoUsage + " " + held.Name + " resets " + held.ResetsAt.UTC().Format(time.RFC3339)
}

// newGoUsageHold is a poll's live OpenCode Go usage: the first candidate on
// that plan reads it and every later one reuses the binding, so the route is
// read at most once a poll. A read that fails, times out or comes back in an
// unknown shape is logged once as goUsageNotRead and holds nothing, leaving the
// plan's fixed limits to decide.
func newGoUsageHold(ctx context.Context, d Deps) func(at time.Time) string {
	var (
		read bool
		held string
	)
	return func(at time.Time) string {
		if read {
			return held
		}
		read = true
		if d.GoUsage == nil {
			d.Logger.Warn("goUsageNotRead", "err", "no usage reader configured")
			return ""
		}
		usage, err := d.GoUsage.Read(ctx)
		if err != nil {
			d.Logger.Warn("goUsageNotRead", "err", err.Error())
			return ""
		}
		held = usage.SpentBinding(at)
		return held
	}
}

// GoUsageClient reads the plan's usage with the key Token yields: the same key
// the run's model calls bill against, so the windows it reports are the run's
// own.
type GoUsageClient struct {
	Token func(context.Context) (string, error)
	// URL is the usage route; empty reads GoUsageEndpoint.
	URL string
	// Timeout bounds one read; zero takes DefaultGoUsageTimeout.
	Timeout time.Duration
	// Client is the HTTP client; nil takes http.DefaultClient.
	Client *http.Client
}

// Read returns the plan's windows, or why they could not be read: a failed
// request, a non-200 answer, a timeout, or a shape this route has not carried.
func (c GoUsageClient) Read(ctx context.Context) (GoUsage, error) {
	ctx, cancel := context.WithTimeout(ctx, cmp.Or(c.Timeout, DefaultGoUsageTimeout))
	defer cancel()
	key, err := c.Token(ctx)
	if err != nil {
		return GoUsage{}, err
	}
	if key == "" {
		return GoUsage{}, errors.New("the OpenCode Go key is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cmp.Or(c.URL, GoUsageEndpoint), nil)
	if err != nil {
		return GoUsage{}, fmt.Errorf("building the OpenCode Go usage request: %w", err)
	}
	req.Header.Set("accept", "application/json")
	req.Header.Set("authorization", "Bearer "+key)
	req.Header.Set("x-opencode-session", goUsageSession)
	res, err := cmp.Or(c.Client, http.DefaultClient).Do(req)
	if err != nil {
		return GoUsage{}, fmt.Errorf("reading OpenCode Go usage: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return GoUsage{}, fmt.Errorf("OpenCode Go usage answered %s", res.Status)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, goUsageMaxBody))
	if err != nil {
		return GoUsage{}, fmt.Errorf("reading OpenCode Go usage body: %w", err)
	}
	return parseGoUsage(body)
}

// goUsageDoc is the route's payload. Windows this route adds are ignored, and
// one of the known three that is missing or malformed makes the whole read
// unusable rather than silently dropping a window a run could be spending.
type goUsageDoc struct {
	Usage *goUsageWindows `json:"usage"`
}

type goUsageWindows struct {
	Rolling *goUsageWindowDoc `json:"rolling"`
	Weekly  *goUsageWindowDoc `json:"weekly"`
	Monthly *goUsageWindowDoc `json:"monthly"`
}

type goUsageWindowDoc struct {
	Status   *string  `json:"status"`
	Percent  *float64 `json:"percent"`
	ResetsAt *string  `json:"resetsAt"`
}

// parseGoUsage decodes the route's payload into the plan's windows.
func parseGoUsage(body []byte) (GoUsage, error) {
	var doc goUsageDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return GoUsage{}, fmt.Errorf("OpenCode Go usage is not JSON: %w", err)
	}
	if doc.Usage == nil {
		return GoUsage{}, errors.New("OpenCode Go usage carries no usage object")
	}
	known := []struct {
		name string
		doc  *goUsageWindowDoc
	}{
		{"5h", doc.Usage.Rolling},
		{"week", doc.Usage.Weekly},
		{"month", doc.Usage.Monthly},
	}
	usage := GoUsage{Windows: make([]GoUsageWindow, 0, len(known))}
	for _, w := range known {
		window, err := parseGoUsageWindow(w.name, w.doc)
		if err != nil {
			return GoUsage{}, err
		}
		usage.Windows = append(usage.Windows, window)
	}
	return usage, nil
}

// parseGoUsageWindow decodes one window, rejecting a shape carrying a value
// admission could not act on.
func parseGoUsageWindow(name string, doc *goUsageWindowDoc) (GoUsageWindow, error) {
	if doc == nil || doc.Status == nil || doc.Percent == nil || doc.ResetsAt == nil {
		return GoUsageWindow{}, fmt.Errorf("OpenCode Go usage has no complete %s window", name)
	}
	if *doc.Status != goUsageOKStatus && *doc.Status != GoUsageRateLimited {
		return GoUsageWindow{}, fmt.Errorf("OpenCode Go usage reports the %s window as %q", name, *doc.Status)
	}
	if *doc.Percent < 0 || *doc.Percent > 100 {
		return GoUsageWindow{}, fmt.Errorf("OpenCode Go usage reports the %s window at %v percent", name, *doc.Percent)
	}
	resetsAt, err := time.Parse(time.RFC3339, *doc.ResetsAt)
	if err != nil {
		return GoUsageWindow{}, fmt.Errorf("OpenCode Go usage reports the %s window resetting at %q", name, *doc.ResetsAt)
	}
	return GoUsageWindow{Name: name, Status: *doc.Status, Percent: *doc.Percent, ResetsAt: resetsAt}, nil
}
