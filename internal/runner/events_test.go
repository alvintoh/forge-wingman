package runner

import (
	"strconv"
	"strings"
	"testing"
)

func TestSumUsage(t *testing.T) {
	step := func(in, out, reasoning, read, write, cost string) string {
		return `{"type":"step_finish","part":{"type":"step-finish","tokens":{"input":` + in + `,"output":` + out +
			`,"reasoning":` + reasoning + `,"cache":{"read":` + read + `,"write":` + write + `}},"cost":` + cost + `}}`
	}
	tests := []struct {
		name   string
		stream string
		want   Usage
	}{
		{"empty stream", "", Usage{}},
		{"one step", step("100", "20", "5", "1000", "7", "0.25"),
			Usage{Input: 100, Output: 20, Reasoning: 5, CacheRead: 1000, CacheWrite: 7, Cost: 0.25, Steps: 1}},
		{"steps sum and other events are ignored", strings.Join([]string{
			`{"type":"step_start","part":{}}`,
			step("100", "20", "0", "1000", "0", "0.5"),
			`{"type":"text","part":{"text":"hello","tokens":{"input":999}}}`,
			step("50", "10", "3", "2000", "4", "0.25"),
		}, "\n"), Usage{Input: 150, Output: 30, Reasoning: 3, CacheRead: 3000, CacheWrite: 4, Cost: 0.75, Steps: 2}},
		{"a cost event adds cost but is not a step", step("100", "20", "0", "1000", "0", "0") + "\n" +
			`{"type":"cost","part":{"cost":0.0030955680000000004}}`,
			Usage{Input: 100, Output: 20, CacheRead: 1000, Cost: 0.0030955680000000004, Steps: 1}},
		{"malformed lines are skipped", "not json\n" + step("1", "2", "0", "0", "0", "0") + "\n{",
			Usage{Input: 1, Output: 2, Steps: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SumUsage(strings.NewReader(tt.stream))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("usage = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestFinalText(t *testing.T) {
	textEvent := func(text string) string {
		return `{"type":"text","part":{"type":"text","text":` + strconv.Quote(text) + `}}`
	}
	tests := []struct {
		name   string
		stream string
		want   string
	}{
		{"no text event", `{"type":"step_finish","part":{}}`, ""},
		{"one text event", textEvent("hello"), "hello"},
		{"later events accumulate, so the last wins", strings.Join([]string{
			textEvent("## plan\n\nfile"),
			`{"type":"step_finish","part":{}}`,
			textEvent("## plan\n\nfile one\nfile two"),
		}, "\n"), "## plan\n\nfile one\nfile two"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FinalText(strings.NewReader(tt.stream))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("text = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSessionID(t *testing.T) {
	tests := []struct {
		name   string
		stream string
		want   string
	}{
		{"no session", `{"type":"step_finish","part":{}}`, ""},
		{"reported on an error event", `{"type":"error","sessionID":"ses_f19e","error":{}}`, "ses_f19e"},
		{"reported on the first event and reused", strings.Join([]string{
			`{"type":"step_start","sessionID":"ses_1"}`,
			`{"type":"step_finish","sessionID":"ses_1"}`,
		}, "\n"), "ses_1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SessionID(strings.NewReader(tt.stream))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("session = %q, want %q", got, tt.want)
			}
		})
	}
}
