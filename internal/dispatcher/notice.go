package dispatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// pollInterval is the scheduled poll's period in infra/dispatcher.tf, the
// latest the next poll comes; noticeLease must outlast the job's 600s timeout,
// so a live poll's claim is never taken from it.
const (
	pollInterval      = 15 * time.Minute
	noticeLease       = 15 * time.Minute
	deferralClass     = "deferral:"
	deferralNoticeKey = "deferral-"
)

// Notice is one systemic-failure notice: what failed, and the link that
// leads to it. ID names it in the store, so it is raised at most once.
type Notice struct {
	ID    string
	Class string
	Link  string
}

// Text is the notice as posted: its class and link, never anything the run or
// its ticket wrote.
func (n Notice) Text() string {
	return strings.TrimSpace(n.Class + " " + n.Link)
}

// Notices keeps the notices waiting to be posted.
//
// Raise creates a notice unless one with its ID exists. Claim takes every
// pending notice, and any claimed before staleBefore, in one transaction, so
// two polls never post the same one; Posted and Unclaim settle a claim.
type Notices interface {
	Raise(ctx context.Context, n Notice, at time.Time) error
	Claim(ctx context.Context, at, staleBefore time.Time) ([]Notice, error)
	Posted(ctx context.Context, id string, at time.Time) error
	Unclaim(ctx context.Context, id string) error
}

// Breaker reports whether a systemic stop has halted every dispatch, until an
// operator's runner reset-breaker clears it.
type Breaker interface {
	Tripped(ctx context.Context) (bool, error)
}

// Poster posts one notice.
type Poster interface {
	Post(ctx context.Context, n Notice) error
}

// Spender reads the spend FR-22's ceilings measure as it stands at a moment:
// what the ledger holds reserved, and the settled cost inside each of cfg's
// windows ending then.
type Spender interface {
	Spend(ctx context.Context, cfg BudgetConfig, model string, at time.Time) (Totals, Settled, error)
}

// postNotices posts every notice it can claim, opening the poster only once
// one is claimed. A notice it could not post is unclaimed for the next poll.
func postNotices(ctx context.Context, d Deps) error {
	at := d.Now()
	notices, err := d.Notices.Claim(ctx, at, at.Add(-noticeLease))
	if err != nil {
		return err
	}
	if len(notices) == 0 {
		return nil
	}
	poster, err := d.OpenPoster(ctx)
	if err != nil {
		d.Logger.Error("noticePosterUnavailable", "notices", len(notices), "err", err.Error())
		errs := []error{err}
		for _, n := range notices {
			errs = append(errs, d.Notices.Unclaim(ctx, n.ID))
		}
		return errors.Join(errs...)
	}
	var errs []error
	for _, n := range notices {
		if err := poster.Post(ctx, n); err != nil {
			d.Logger.Error("noticePostFailed", "notice", n.ID, "class", n.Class, "err", err.Error())
			errs = append(errs, err, d.Notices.Unclaim(ctx, n.ID))
			continue
		}
		if err := d.Notices.Posted(ctx, n.ID, d.Now()); err != nil {
			errs = append(errs, err)
			continue
		}
		d.Logger.Info("noticePosted", "notice", n.ID, "class", n.Class)
	}
	return errors.Join(errs...)
}

// noticeDeferral raises a notice for a run the budget ceiling withheld, when
// that ceiling would still withhold it at the next poll: once per ceiling per
// window, reporting whether it raised one. A run with no issue link raises
// none, since a notice carries one. A failed read or write is logged, and the
// next poll asks again.
func noticeDeferral(ctx context.Context, d Deps, cfg BudgetConfig, model string, estimate Reservation, ceiling, link string) bool {
	at := d.Now()
	start, ok := cfg.ceilingStart(ceiling, at)
	if !ok || link == "" {
		return false
	}
	reserved, settled, err := d.Queue.Spend(ctx, cfg, model, at.Add(pollInterval))
	if err != nil {
		d.Logger.Warn("deferralSpendUnread", "ceiling", ceiling, "err", err.Error())
		return false
	}
	if !stillBinds(cfg, model, reserved, settled, estimate, ceiling) {
		return false
	}
	n := Notice{ID: deferralNoticeID(ceiling, start), Class: deferralClass + ceiling, Link: link}
	if err := d.Notices.Raise(ctx, n, at); err != nil {
		d.Logger.Warn("deferralNoticeNotRaised", "ceiling", ceiling, "err", err.Error())
		return false
	}
	return true
}

// deferralNoticeID names the one notice a ceiling raises in the window
// starting at start; a model cap's name carries a slash, which a document id
// cannot.
func deferralNoticeID(ceiling string, start time.Time) string {
	return deferralNoticeKey + url.PathEscape(ceiling) + "-" + strconv.FormatInt(start.Unix(), 10)
}

// Slack posts notices to a Slack incoming webhook.
type Slack struct {
	// Webhook is the incoming webhook's URL, a credential no error may carry.
	Webhook string
	Client  *http.Client
}

// Post sends the notice's text. Any 2xx is a post.
func (s Slack) Post(ctx context.Context, n Notice) error {
	body, err := json.Marshal(map[string]string{"text": n.Text()})
	if err != nil {
		return fmt.Errorf("encoding notice %s: %w", n.ID, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Webhook, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building notice %s: the webhook URL does not parse", n.ID)
	}
	req.Header.Set("Content-Type", "application/json")
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("posting notice %s: %w", n.ID, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return nil
	}
	raw, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBytes))
	return fmt.Errorf("slack refused notice %s: %s: %s", n.ID, res.Status, snippet(raw))
}
