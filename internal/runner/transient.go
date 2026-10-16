package runner

import "strings"

// TransientClass names a known transient infrastructure fault: one that says
// nothing about the ticket, so a rerun is worth an attempt where a real
// failure is not.
type TransientClass string

const (
	// TransientModuleProxy is a read the Go module proxy could not serve: a
	// download or checksum fetch that failed on the network between the runner
	// and proxy.golang.org.
	TransientModuleProxy TransientClass = "module-proxy"
	// TransientNetwork is a network-level failure reaching a dependency: a
	// connection that timed out, was reset, or could not resolve a name.
	TransientNetwork TransientClass = "network-timeout"
	// TransientLostRunner is the runner itself disappearing mid-job, which the
	// job log reports as a shutdown the run never reported for.
	TransientLostRunner TransientClass = "lost-runner"
)

// transientSignature is one listed fault: the class it names and the markers a
// failing step's log carries for it.
type transientSignature struct {
	class   TransientClass
	markers []string
}

// transientSignatures is the closed list of transient faults. Markers are
// lowercase, and classes are checked in order, so a proxy read that also timed
// out is the proxy's class. A fault outside this list is never requeued
// automatically, which is what keeps an agent failure, a failing gate and an
// unproduced plan out.
var transientSignatures = []transientSignature{
	{TransientModuleProxy, []string{"proxy.golang.org", "sum.golang.org"}},
	{TransientNetwork, []string{"i/o timeout", "connection timed out", "tls handshake timeout", "connection reset by peer", "temporary failure in name resolution", "could not resolve host", "no such host", "network is unreachable", "unexpected eof"}},
	{TransientLostRunner, []string{"the runner has received a shutdown signal", "lost communication with the server"}},
}

// ClassifyTransient is the class of the transient fault stepLog names, or ""
// when it names none: the match is a closed list of markers, so a fault the
// list does not carry is never requeued automatically.
func ClassifyTransient(stepLog string) TransientClass {
	log := strings.ToLower(stepLog)
	for _, sig := range transientSignatures {
		for _, marker := range sig.markers {
			if strings.Contains(log, marker) {
				return sig.class
			}
		}
	}
	return ""
}

// Listed reports whether c is one of the known transient classes, so a row
// carrying anything else is never requeued automatically.
func (c TransientClass) Listed() bool {
	for _, sig := range transientSignatures {
		if sig.class == c {
			return true
		}
	}
	return false
}

// TransientClasses is every known transient class, in the order the list
// checks them: the classes a reader outside this package queries a run row by.
func TransientClasses() []TransientClass {
	classes := make([]TransientClass, 0, len(transientSignatures))
	for _, sig := range transientSignatures {
		classes = append(classes, sig.class)
	}
	return classes
}
