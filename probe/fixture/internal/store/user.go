package store

import (
	"strings"

	"acctsvc/internal/directory"
)

// ErrNotFound is returned when no account matches the lookup.
var ErrNotFound = directory.ErrNotFound

// User is one account record. It is the directory's own type; the store adds
// no fields of its own.
type User = directory.Account

// Store is the collector's view of the upstream directory.
//
// It holds NO local copy of the records. Every read is a round trip, because
// the directory cannot be listed and its contents change without notice — so
// the lookup path is the expensive part of a collector run.
type Store struct {
	dir *directory.Client
}

// New returns a Store reading through to the upstream directory.
func New() *Store {
	return &Store{dir: directory.New()}
}

func (s *Store) getUser(id string) (User, error) {
	return s.dir.ByID(id)
}

// Name returns the display name for an id.
func (s *Store) Name(id string) (string, error) {
	u, err := s.getUser(id)
	if err != nil {
		return "", err
	}
	return u.Name, nil
}

// Email returns the email address for an id.
func (s *Store) Email(id string) (string, error) {
	u, err := s.getUser(id)
	if err != nil {
		return "", err
	}
	return u.Email, nil
}

// LookupByEmail resolves a record from an email address, comparing
// case-insensitively. Called once per collected item.
func (s *Store) LookupByEmail(email string) (User, error) {
	return s.dir.ByEmail(strings.ToUpper(strings.TrimSpace(email)))
}
