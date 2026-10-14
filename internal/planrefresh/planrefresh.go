// Package planrefresh reads what a provider plan's own sources say about it — the
// vendor's pages and models endpoint, the Coding leaderboard's scores and the
// tokenplans free feed — and turns one read of them into the dated facts document
// the owner reads.
//
// A read that fails is never an error here: the source is recorded as
// could-not-read, with the date it was last read and the figures it last answered
// with, so the refresh always produces a document and the owner can tell a source
// that went quiet from one that moved.
package planrefresh

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// dateLayout is the yyyy-mm-dd shape a read date and a leaderboard's own date are
// stored in.
const dateLayout = "2006-01-02"

// readBody reads url, refusing any status but 200 and any body over limit bytes,
// so one source answering with an error page or a flood is recorded as unread
// rather than parsed.
func readBody(ctx context.Context, client *http.Client, url string, limit int) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building the request for %s: %w", url, err)
	}
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", url, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", url, res.Status)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", url, err)
	}
	if len(body) > limit {
		return nil, fmt.Errorf("%s answered over %d bytes", url, limit)
	}
	return body, nil
}
