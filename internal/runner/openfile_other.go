//go:build !unix

package runner

import "os"

// openForCount opens path for reading.
func openForCount(path string) (*os.File, error) {
	return os.Open(path)
}
