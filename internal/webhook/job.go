package webhook

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const maxJobReply = 64 << 10

// recordWakeTimeout bounds the wake record, so the record and the run request
// together fit the webhook's own call deadline.
const recordWakeTimeout = 800 * time.Millisecond

// ErrWakeNotRecorded is a wake whose execution was started although its record
// could not be written: the execution may then take itself for a scheduled tick
// and wait for the owner's interval, so a caller that can retry should.
var ErrWakeNotRecorded = errors.New("the wake was not recorded")

// WakeRequester records that a poll was asked for, which is how the execution
// a wake starts tells itself from a scheduled tick.
type WakeRequester interface {
	RequestWake(ctx context.Context) error
}

// Job starts executions of a Cloud Run job through its v2 run endpoint.
type Job struct {
	RunURI string
	// Client carries the caller's Google credentials.
	Client *http.Client
	// Requests records each wake before the execution starts, so the execution
	// polls whatever the owner's interval.
	Requests WakeRequester
}

// Wake records the wake, then starts one execution. It returns once Cloud Run
// has accepted the request, without waiting on the long-running operation it
// answers with. A record that fails does not hold the execution back, since
// the execution may still poll, and a lost wake waits for a later tick: the
// error then wraps ErrWakeNotRecorded when the execution did start.
func (j Job) Wake(ctx context.Context) error {
	recordErr := j.record(ctx)
	if err := j.start(ctx); err != nil {
		if recordErr != nil {
			return fmt.Errorf("%w; %v", err, recordErr)
		}
		return err
	}
	if recordErr != nil {
		return fmt.Errorf("%w: %v", ErrWakeNotRecorded, recordErr)
	}
	return nil
}

// record writes the wake request on its own short deadline.
func (j Job) record(ctx context.Context) error {
	if j.Requests == nil {
		return errors.New("no store to record the wake in")
	}
	ctx, cancel := context.WithTimeout(ctx, recordWakeTimeout)
	defer cancel()
	return j.Requests.RequestWake(ctx)
}

// start sends the run request.
func (j Job) start(ctx context.Context) error {
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
