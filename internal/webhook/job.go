package webhook

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

const maxJobReply = 64 << 10

// Job starts executions of a Cloud Run job through its v2 run endpoint.
type Job struct {
	RunURI string
	// Client carries the caller's Google credentials.
	Client *http.Client
}

// Wake starts one execution. It returns once Cloud Run has accepted the
// request, without waiting on the long-running operation it answers with.
func (j Job) Wake(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, j.RunURI, http.NoBody)
	if err != nil {
		return fmt.Errorf("building the run request: %w", err)
	}
	res, err := j.Client.Do(req)
	if err != nil {
		return fmt.Errorf("starting the dispatcher: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, maxJobReply))
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("starting the dispatcher: Cloud Run answered %s", res.Status)
	}
	return nil
}
