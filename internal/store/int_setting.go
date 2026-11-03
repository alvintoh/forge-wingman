package store

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const settingsCollection = "settings"

// intSetting is one whole-number setting the owner saves in the owner
// database's settings collection, read as def while none is saved. A zero max
// bounds it only from below.
type intSetting struct {
	client        *firestore.Client
	docID, field  string
	name          string
	min, max, def int
}

func (s intSetting) doc() *firestore.DocumentRef {
	return s.client.Collection(settingsCollection).Doc(s.docID)
}

// inRange reports whether n is a value the setting allows.
func (s intSetting) inRange(n int64) bool {
	return n >= int64(s.min) && (s.max == 0 || n <= int64(s.max))
}

// rangeWords is the setting's range in words, for an error.
func (s intSetting) rangeWords() string {
	if s.max == 0 {
		return fmt.Sprintf("a whole number of at least %d", s.min)
	}
	return fmt.Sprintf("a whole number from %d to %d", s.min, s.max)
}

// value returns the saved value, def while none is saved. A stored value that
// is not a whole number in range is an error, so a reader falls back rather
// than act on it.
func (s intSetting) value(ctx context.Context) (int, error) {
	snap, err := s.doc().Get(ctx)
	if status.Code(err) == codes.NotFound {
		return s.def, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading the %s: %w", s.name, err)
	}
	n, ok := snap.Data()[s.field].(int64)
	if !ok || !s.inRange(n) {
		return 0, fmt.Errorf("reading the %s: %q is not %s", s.name, s.field, s.rangeWords())
	}
	return int(n), nil
}

// set saves n, recording when.
func (s intSetting) set(ctx context.Context, n int, at time.Time) error {
	if !s.inRange(int64(n)) {
		return fmt.Errorf("setting the %s: %d is not %s", s.name, n, s.rangeWords())
	}
	if _, err := s.doc().Set(ctx, map[string]any{s.field: n, setAtField: at}); err != nil {
		return fmt.Errorf("setting the %s: %w", s.name, err)
	}
	return nil
}
