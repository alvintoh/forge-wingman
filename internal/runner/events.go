package runner

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

var maxEventLine = 64 << 20

// Usage is the token and cost total across every model step of one agent run.
type Usage struct {
	Input      int64   `firestore:"input" json:"input"`
	Output     int64   `firestore:"output" json:"output"`
	Reasoning  int64   `firestore:"reasoning" json:"reasoning"`
	CacheRead  int64   `firestore:"cache_read" json:"cache_read"`
	CacheWrite int64   `firestore:"cache_write" json:"cache_write"`
	Cost       float64 `firestore:"cost" json:"cost"`
	Steps      int     `firestore:"steps" json:"steps"`
}

// event is one line of the agent's own event stream: every harness translates
// its frames into this shape, so the parsers below stay harness-neutral.
type event struct {
	Type string `json:"type"`
	Part struct {
		Tokens struct {
			Input     int64 `json:"input"`
			Output    int64 `json:"output"`
			Reasoning int64 `json:"reasoning"`
			Cache     struct {
				Read  int64 `json:"read"`
				Write int64 `json:"write"`
			} `json:"cache"`
		} `json:"tokens"`
		Cost float64 `json:"cost"`
		// Time is when a step's request was made, in epoch milliseconds; zero when the harness does not say.
		Time int64 `json:"time,omitempty"`
	} `json:"part"`
	// Warning is a usage_warning event's text: usage the harness could not measure.
	Warning string `json:"warning,omitempty"`
}

// sessionEvent reports a session id in the runner's own event shape.
type sessionEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
}

// textEvent is a text part in the runner's own event shape.
type textEvent struct {
	Type string `json:"type"`
	Part struct {
		Text string `json:"text"`
	} `json:"part"`
}

func newTextEvent(text string) textEvent {
	var e textEvent
	e.Type = "text"
	e.Part.Text = text
	return e
}

// scanEvents calls visit for every JSON event in the agent's event stream,
// skipping lines that are not JSON events.
func scanEvents(r io.Reader, visit func(e event)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, min(64*1024, maxEventLine)), maxEventLine)
	for sc.Scan() {
		var e event
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		visit(e)
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("reading events: %w", err)
	}
	return nil
}

// stepUsage is one step_finish event's tokens and cost, counted as one step.
func stepUsage(e event) Usage {
	t := e.Part.Tokens
	return Usage{Input: t.Input, Output: t.Output, Reasoning: t.Reasoning, CacheRead: t.Cache.Read,
		CacheWrite: t.Cache.Write, Cost: e.Part.Cost, Steps: 1}
}

// SumUsage totals the step_finish events in the agent's JSON event stream,
// plus the cost of any cost event, which a harness emits when its per-step
// events carry none; a cost event is not a step.
//
// Lines that are not JSON events are skipped.
func SumUsage(r io.Reader) (Usage, error) { return sumUsage(r, nil) }

// sumUsage is SumUsage, priced from rates when rates is non-nil: a timed step
// at the rates in force at its time, replacing the harness's figure. Untimed
// steps keep the harness's own figures when the run carries a cost event, and
// otherwise price at peak; a run whose steps are all timed ignores cost events.
func sumUsage(r io.Reader, rates *providers.Rates) (Usage, error) {
	var u Usage
	var untimed int
	var untimedOwn, untimedPeak, eventCost float64
	var costEvent bool
	err := scanEvents(r, func(e event) {
		switch e.Type {
		case "step_finish":
			s := stepUsage(e)
			switch {
			case rates == nil:
			case e.Part.Time == 0:
				untimed++
				untimedOwn += s.Cost
				untimedPeak += rates.At(time.Time{}).CostUSD(s.Input, s.Output, s.CacheRead, s.CacheWrite)
				s.Cost = 0
			default:
				s.Cost = rates.At(time.UnixMilli(e.Part.Time)).CostUSD(s.Input, s.Output, s.CacheRead, s.CacheWrite)
			}
			u.Input += s.Input
			u.Output += s.Output
			u.Reasoning += s.Reasoning
			u.CacheRead += s.CacheRead
			u.CacheWrite += s.CacheWrite
			u.Cost += s.Cost
			u.Steps++
		case "cost":
			if rates == nil {
				u.Cost += e.Part.Cost
			}
			eventCost += e.Part.Cost
			costEvent = true
		}
	})
	switch {
	case untimed == 0:
	case costEvent:
		u.Cost += untimedOwn + eventCost
	default:
		u.Cost += untimedPeak
	}
	return u, err
}

// RequestUsage returns each step_finish event's usage in stream order: one
// entry per model request.
func RequestUsage(r io.Reader) ([]Usage, error) {
	var reqs []Usage
	err := scanEvents(r, func(e event) {
		if e.Type == "step_finish" {
			reqs = append(reqs, stepUsage(e))
		}
	})
	return reqs, err
}

// UsageWarnings returns the text of every usage_warning event in the agent's
// JSON event stream, in order.
func UsageWarnings(r io.Reader) ([]string, error) {
	var warnings []string
	err := scanEvents(r, func(e event) {
		if e.Type == "usage_warning" && e.Warning != "" {
			warnings = append(warnings, e.Warning)
		}
	})
	return warnings, err
}

// FinalText returns the last text part in the agent's JSON event stream, empty
// when none appeared.
//
// A text part carries the message accumulated so far rather than a delta, so
// the last one seen holds the final text; lines that are not JSON events are
// skipped, the same as SumUsage.
func FinalText(r io.Reader) (string, error) {
	var text string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, min(64*1024, maxEventLine)), maxEventLine)
	for sc.Scan() {
		var e struct {
			Type string `json:"type"`
			Part struct {
				Text string `json:"text"`
			} `json:"part"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Type != "text" {
			continue
		}
		text = e.Part.Text
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("reading events: %w", err)
	}
	return text, nil
}

// SessionID returns the session id the agent's JSON event stream reports,
// read from the first event that carries one — every event does, including
// an error, so this works even on a run that never completed. Empty when
// none appeared.
func SessionID(r io.Reader) (string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, min(64*1024, maxEventLine)), maxEventLine)
	for sc.Scan() {
		var e struct {
			SessionID string `json:"sessionID"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.SessionID == "" {
			continue
		}
		return e.SessionID, nil
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("reading events: %w", err)
	}
	return "", nil
}
