// Package store adapts Cloud Storage and Firestore to the runner's interfaces.
package store

import (
	"context"
	"errors"
	"fmt"
	"io"

	"cloud.google.com/go/storage"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// Bucket reads and creates objects in one Cloud Storage bucket.
type Bucket struct {
	handle *storage.BucketHandle
}

// NewBucket returns a Bucket for name on client.
func NewBucket(client *storage.Client, name string) *Bucket {
	return &Bucket{handle: client.Bucket(name)}
}

// ReadObject returns the whole object, or runner.ErrObjectNotFound.
func (b *Bucket) ReadObject(ctx context.Context, name string) ([]byte, error) {
	r, err := b.handle.Object(name).NewReader(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return nil, fmt.Errorf("%s: %w", name, runner.ErrObjectNotFound)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	return io.ReadAll(r)
}

// CreateObject writes the object only if none exists by that name, and commits
// nothing if src fails partway.
func (b *Bucket) CreateObject(ctx context.Context, name string, src io.Reader) error {
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := b.handle.Object(name).If(storage.Conditions{DoesNotExist: true}).NewWriter(wctx)
	if _, err := io.Copy(w, src); err != nil {
		cancel()
		_ = w.Close()
		return err
	}
	return w.Close()
}
