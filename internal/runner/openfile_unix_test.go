//go:build unix

package runner

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestOpenForCountNeitherBlocksOnAFIFONorFollowsASymlink(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		f, err := openForCount(fifo)
		if err == nil {
			_ = f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("opening the FIFO: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("opening a FIFO blocked")
	}
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := openForCount(link); !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("err = %v, want the symlink refused", err)
	}
}
