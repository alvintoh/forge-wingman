package modelprobe

import (
	"bytes"
	"encoding/json"
	"strings"
)

type eventKind int

const (
	eventUnknown eventKind = iota
	eventTool
	eventError
	eventOther
)

// classify maps one line of opencode's JSON event stream to a kind.
//
// The only place event names live. step_finish and text are read elsewhere in
// this repository; tool_use and error are not yet confirmed against a live run.
func classify(line []byte) eventKind {
	var e struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(line, &e) != nil {
		return eventUnknown
	}
	switch e.Type {
	case "tool_use":
		return eventTool
	case "error":
		return eventError
	case "step_finish", "text":
		return eventOther
	}
	return eventUnknown
}

// errorEvents returns the lines of an event stream that classify as errors.
func errorEvents(transcript []byte) string {
	var out []string
	for _, line := range bytes.Split(transcript, []byte("\n")) {
		if classify(line) == eventError {
			out = append(out, string(line))
		}
	}
	return strings.Join(out, "\n")
}
