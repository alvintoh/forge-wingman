//go:build unix

package runner

import (
	"os"
	"syscall"
)

// openForCount opens path for reading without following a symlink or blocking
// on a FIFO, so a file swapped after it was checked cannot hang the count.
func openForCount(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}
