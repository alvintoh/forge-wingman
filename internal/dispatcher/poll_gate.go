package dispatcher

import "time"

// The owner's poll interval, in minutes: how long after the last scheduled poll
// a scheduled tick waits before it polls again. A wake polls at once whatever it
// is, and an interval that cannot be read is the default.
const (
	FastPollMinutes    = 10
	DefaultPollMinutes = 15
)

// ValidPollMinutes reports whether n is an interval the owner may save.
func ValidPollMinutes(n int) bool {
	return n == FastPollMinutes || n == DefaultPollMinutes
}

// pollSlack is how far short of the interval a tick still polls: half the gap
// between the two intervals. A 15-minute setting never polls on the tick 10
// minutes after a poll, and a start-up variance of a minute either way still
// lands on the intended ticks.
const pollSlack = (DefaultPollMinutes - FastPollMinutes) * time.Minute / 2

// PollTimes is what an execution reads to tell a wake from a scheduled tick,
// each time zero while never recorded. LastPoll is the start of the last poll
// of either kind, LastScheduled the start of the last poll a tick made, and
// WakeRequested when a waker last asked for a poll.
type PollTimes struct {
	LastPoll      time.Time
	LastScheduled time.Time
	WakeRequested time.Time
}

// Why an execution polls, or does not.
const (
	PollReasonWake           = "wake"
	PollReasonFirst          = "first-poll"
	PollReasonLastPollAhead  = "last-poll-ahead"
	PollReasonIntervalPassed = "interval-passed"
	PollReasonNotDue         = "interval-not-passed"
)

// PollStart is an execution's decision: whether it polls, whether that poll is
// a scheduled one, which the next tick measures its interval from, and why.
type PollStart struct {
	Due       bool
	Scheduled bool
	Reason    string
}

// PollDue decides an execution starting at now. A wake asked for at or since
// the last poll polls at once, since an extra poll costs less than a missed
// wake, and is not a scheduled poll, so it never delays the next tick.
// Otherwise the execution is a scheduled tick, which polls once interval has
// passed since the last scheduled poll. A last scheduled poll later than now is
// a clock that cannot be trusted, so it polls rather than wait on it.
func PollDue(times PollTimes, interval time.Duration, now time.Time) PollStart {
	switch {
	case !times.WakeRequested.IsZero() && !times.WakeRequested.Before(times.LastPoll):
		return PollStart{Due: true, Reason: PollReasonWake}
	case times.LastScheduled.IsZero():
		return PollStart{Due: true, Scheduled: true, Reason: PollReasonFirst}
	case times.LastScheduled.After(now):
		return PollStart{Due: true, Scheduled: true, Reason: PollReasonLastPollAhead}
	case now.Sub(times.LastScheduled) >= interval-pollSlack:
		return PollStart{Due: true, Scheduled: true, Reason: PollReasonIntervalPassed}
	}
	return PollStart{Reason: PollReasonNotDue}
}
