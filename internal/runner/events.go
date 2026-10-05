package runner

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
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
	} `json:"part"`
}

// SumUsage totals the step_finish events in the agent's JSON event stream.
//
// Lines that are not JSON events are skipped.
func SumUsage(r io.Reader) (Usage, error) {
	var u Usage
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, min(64*1024, maxEventLine)), maxEventLine)
	for sc.Scan() {
		var e event
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Type != "step_finish" {
			continue
		}
		t := e.Part.Tokens
		u.Input += t.Input
		u.Output += t.Output
		u.Reasoning += t.Reasoning
		u.CacheRead += t.Cache.Read
		u.CacheWrite += t.Cache.Write
		u.Cost += e.Part.Cost
		u.Steps++
	}
	if err := sc.Err(); err != nil {
		return u, fmt.Errorf("reading events: %w", err)
	}
	return u, nil
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
