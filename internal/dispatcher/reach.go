package dispatcher

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

// ReachReason is why a repository cannot be read, from a closed set so the
// record never carries GitHub's response text.
type ReachReason string

const (
	ReachForbidden    ReachReason = "forbidden"
	ReachNotFound     ReachReason = "not-found"
	ReachOther        ReachReason = "other"
	ReachUnauthorized ReachReason = "unauthorized"
	ReachUnreachable  ReachReason = "unreachable"
)

// ReachStatus is what a repository's record says now.
type ReachStatus string

const (
	ReachChecking   ReachStatus = "checking"
	ReachCannotRead ReachStatus = "cannot-read"
	ReachReachable  ReachStatus = "reachable"
)

// RepoReach is one repository's last read: whether it succeeded, and if not the
// reason and HTTP status (zero when none was received).
type RepoReach struct {
	Repository string
	Reachable  bool
	Reason     ReachReason
	Status     int
	CheckedAt  time.Time
}

// ReachRecorder stores each poll's RepoReach records.
type ReachRecorder interface {
	RecordReach(ctx context.Context, reaches []RepoReach) error
}

func classifyReach(repo string, err error, at time.Time) RepoReach {
	r := RepoReach{Repository: repo, CheckedAt: at, Reachable: err == nil}
	if err == nil {
		return r
	}
	var api *APIError
	var netErr net.Error
	switch {
	case errors.As(err, &api):
		r.Status = api.Status
		switch api.Status {
		case http.StatusUnauthorized:
			r.Reason = ReachUnauthorized
		case http.StatusForbidden:
			r.Reason = ReachForbidden
		case http.StatusNotFound:
			r.Reason = ReachNotFound
		default:
			r.Reason = ReachOther
		}
	case errors.As(err, &netErr), errors.Is(err, context.DeadlineExceeded):
		r.Reason = ReachUnreachable
	default:
		r.Reason = ReachOther
	}
	return r
}

// ReachState reads a record against when its project was added: no record, or
// one checked before the project existed, is still checking.
func ReachState(record *RepoReach, addedAt time.Time) ReachStatus {
	switch {
	case record == nil || record.CheckedAt.Before(addedAt):
		return ReachChecking
	case record.Reachable:
		return ReachReachable
	default:
		return ReachCannotRead
	}
}
