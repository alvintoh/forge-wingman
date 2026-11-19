package dispatcher

import (
	"testing"
	"time"
)

func TestPollDue(t *testing.T) {
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	ten, fifteen := 10*time.Minute, 15*time.Minute
	wake := PollStart{Due: true, Reason: "wake"}
	tick := func(reason string) PollStart { return PollStart{Due: true, Scheduled: true, Reason: reason} }
	skip := PollStart{Reason: "interval-not-passed"}
	for name, tt := range map[string]struct {
		times    PollTimes
		interval time.Duration
		want     PollStart
	}{
		"a wake asked for since the last poll": {
			PollTimes{LastPoll: ago(time.Minute), LastScheduled: ago(time.Minute), WakeRequested: ago(30 * time.Second)}, fifteen, wake},
		"a wake stamped at the same instant as the last poll": {
			PollTimes{LastPoll: ago(time.Minute), LastScheduled: ago(time.Minute), WakeRequested: ago(time.Minute)}, fifteen, wake},
		"a wake with no poll yet": {
			PollTimes{WakeRequested: ago(time.Second)}, fifteen, wake},
		"a wake the last poll already covered": {
			PollTimes{LastPoll: ago(time.Minute), LastScheduled: ago(time.Minute), WakeRequested: ago(2 * time.Minute)}, fifteen, skip},
		"no scheduled poll recorded": {
			PollTimes{}, fifteen, tick("first-poll")},
		"a last scheduled poll later than now": {
			PollTimes{LastPoll: now.Add(time.Hour), LastScheduled: now.Add(time.Hour)}, fifteen, tick("last-poll-ahead")},
		"a wake poll since the last scheduled one": {
			PollTimes{LastPoll: ago(11 * time.Minute), LastScheduled: ago(16 * time.Minute), WakeRequested: ago(11*time.Minute + 30*time.Second)}, fifteen, tick("interval-passed")},
		"a tick 2.5 minutes early on a fifteen-minute interval": {
			PollTimes{LastPoll: ago(12*time.Minute + 30*time.Second), LastScheduled: ago(12*time.Minute + 30*time.Second)}, fifteen, tick("interval-passed")},
		"a tick too early on a ten-minute interval": {
			PollTimes{LastPoll: ago(7*time.Minute + 29*time.Second), LastScheduled: ago(7*time.Minute + 29*time.Second)}, ten, skip},
		"a ten-minute tick on a fifteen-minute interval": {
			PollTimes{LastPoll: ago(ten + 2*time.Minute), LastScheduled: ago(ten + 2*time.Minute)}, fifteen, skip},
	} {
		t.Run(name, func(t *testing.T) {
			if got := PollDue(tt.times, tt.interval, now); got != tt.want {
				t.Fatalf("PollDue = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestValidPollMinutesAllowsTenAndFifteenOnly(t *testing.T) {
	for _, n := range []int{10, 15} {
		if !ValidPollMinutes(n) {
			t.Errorf("%d refused", n)
		}
	}
	for _, n := range []int{0, 9, 11, 14, 16, -10} {
		if ValidPollMinutes(n) {
			t.Errorf("%d allowed", n)
		}
	}
}
