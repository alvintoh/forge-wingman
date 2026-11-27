package dispatcher

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// scheduleTicks is the minutes of each hour the dispatcher's Cloud Scheduler job
// ticks at, which the poll gate's slack is sized for.
var scheduleTicks = []int{0, 10, 15, 20, 30, 40, 45, 50}

// TestTheDispatcherScheduleTicksAtTheMinutesTheGateAssumes holds the job's cron
// value to a plain list of minutes equal to scheduleTicks: a step or range form
// would change which ticks the gate test below runs over without failing it.
func TestTheDispatcherScheduleTicksAtTheMinutesTheGateAssumes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "infra", "dispatcher.tf"))
	if err != nil {
		t.Fatal(err)
	}
	schedule := dispatcherSchedule(t, string(raw))
	fields := strings.Fields(schedule)
	if len(fields) != 5 {
		t.Fatalf("dispatcher.tf's schedule %q has %d fields, want 5", schedule, len(fields))
	}
	for _, f := range fields[1:] {
		if f != "*" {
			t.Fatalf("dispatcher.tf's schedule %q restricts a field other than the minutes: %q", schedule, f)
		}
	}
	var got []int
	for _, m := range strings.Split(fields[0], ",") {
		n, err := strconv.Atoi(m)
		if err != nil {
			t.Fatalf("dispatcher.tf's schedule minutes %q must be a plain comma-separated list of minutes, not %q", fields[0], m)
		}
		got = append(got, n)
	}
	if !reflect.DeepEqual(got, scheduleTicks) {
		t.Fatalf("dispatcher.tf's schedule ticks at minutes %v, the poll gate assumes %v", got, scheduleTicks)
	}
}

// dispatcherSchedule is the schedule of the dispatcher's scheduler job.
func dispatcherSchedule(t *testing.T, tf string) string {
	t.Helper()
	i := strings.Index(tf, `resource "google_cloud_scheduler_job" "dispatcher" {`)
	if i < 0 {
		t.Fatal(`dispatcher.tf declares no google_cloud_scheduler_job "dispatcher"`)
	}
	rest := tf[i:]
	j := strings.Index(rest, "\n  schedule ")
	if j < 0 {
		t.Fatal("dispatcher.tf's scheduler job declares no schedule")
	}
	line, _, _ := strings.Cut(rest[j+1:], "\n")
	_, value, ok := strings.Cut(line, "=")
	value = strings.TrimSpace(value)
	if !ok || len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		t.Fatalf("dispatcher.tf's schedule line %q is not a quoted string", line)
	}
	return value[1 : len(value)-1]
}

// TestThePollGatePollsOnTheIntendedTicksOfTheSchedule runs the real gate over
// four hours of the schedule, each tick starting a minute early or late in every
// combination, and requires the polls to land on minutes 0, 15, 30 and 45 for a
// 15-minute setting and on 0, 10, 20, 30, 40 and 50 for a 10-minute one.
func TestThePollGatePollsOnTheIntendedTicksOfTheSchedule(t *testing.T) {
	const hours = 4
	cases := []struct {
		interval time.Duration
		want     []int
	}{
		{15 * time.Minute, []int{0, 15, 30, 45}},
		{10 * time.Minute, []int{0, 10, 20, 30, 40, 50}},
	}
	start := time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC)
	for _, c := range cases {
		for pattern := 0; pattern < 1<<len(scheduleTicks); pattern++ {
			var times PollTimes
			polled := make([]map[int]bool, hours)
			for h := range polled {
				polled[h] = map[int]bool{}
			}
			for h := 0; h < hours; h++ {
				for i, minute := range scheduleTicks {
					jitter := -time.Minute
					if pattern&(1<<i) != 0 {
						jitter = time.Minute
					}
					now := start.Add(time.Duration(h)*time.Hour + time.Duration(minute)*time.Minute + jitter)
					d := PollDue(times, c.interval, now)
					if !d.Due || !d.Scheduled {
						continue
					}
					times = PollTimes{LastPoll: now, LastScheduled: now}
					polled[h][minute] = true
				}
			}
			for h := 1; h < hours; h++ {
				var got []int
				for _, minute := range scheduleTicks {
					if polled[h][minute] {
						got = append(got, minute)
					}
				}
				if !reflect.DeepEqual(got, c.want) {
					t.Fatalf("interval %s, tick pattern %08b, hour %d polled at minutes %v, want %v", c.interval, pattern, h, got, c.want)
				}
			}
		}
	}
}
