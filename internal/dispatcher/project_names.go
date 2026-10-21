package dispatcher

import "context"

// LinearProjects lists the names of the Linear projects the poll reads, the ones
// an added project may be picked from.
type LinearProjects interface {
	ProjectNames(ctx context.Context) ([]string, error)
}

// ProjectNameRecorder stores the names of the Linear projects one poll read.
type ProjectNameRecorder interface {
	RecordProjectNames(ctx context.Context, names []string) error
}

// recordProjectNames records the names of the Linear projects this poll read, so
// the surface can offer them without a Linear credential of its own (adr/0019).
// A read that fails leaves the last record in place, which still offers a
// choice, and a failed write is logged: neither fails the poll.
func recordProjectNames(ctx context.Context, d Deps) {
	if d.LinearProjects == nil || d.ProjectRecords == nil {
		return
	}
	names, err := d.LinearProjects.ProjectNames(ctx)
	if err != nil {
		d.Logger.Warn("linearProjectsUnread", "err", err.Error())
		return
	}
	if err := d.ProjectRecords.RecordProjectNames(ctx, names); err != nil {
		d.Logger.Warn("linearProjectsNotRecorded", "projects", len(names), "err", err.Error())
	}
}
