package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// cdDocID is the dispatch/cd document: whether each target repository is
// recorded as deploying on merge, the CD flag the level-4 advance condition
// reads. One document carries every repository's entry, so the condition's read
// is a single one.
const (
	cdDocID        = "cd"
	cdEntriesField = "entries"
)

// CDEntry is one repository's recorded CD flag: whether it deploys a merge, and
// when the owner recorded that.
type CDEntry struct {
	Repo      string    `firestore:"repo"`
	Deploying bool      `firestore:"deploying"`
	At        time.Time `firestore:"at"`
}

// cdDoc is the dispatch/cd document's shape.
type cdDoc struct {
	Entries []CDEntry `firestore:"entries"`
}

// CDs keeps the recorded CD flag of each target repository.
type CDs struct {
	client *firestore.Client
}

// NewCDs returns a CDs backed by client.
func NewCDs(client *firestore.Client) *CDs {
	return &CDs{client: client}
}

func (c *CDs) doc() *firestore.DocumentRef {
	return c.client.Collection(dispatchCollection).Doc(cdDocID)
}

// Recorded returns each repository's recorded flag, and false for one the owner
// never recorded. Keys are lowercased: GitHub names a repository
// case-insensitively, and the level's condition compares them the same way. A
// document that cannot be read is an error — a missing flag withholds a level,
// so a broken read must not read as "recorded".
func (c *CDs) Recorded(ctx context.Context) (map[string]bool, error) {
	snap, err := c.doc().Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the recorded CD flags: %w", err)
	}
	var doc cdDoc
	if err := snap.DataTo(&doc); err != nil {
		return nil, fmt.Errorf("decoding the recorded CD flags: %w", err)
	}
	flags := make(map[string]bool, len(doc.Entries))
	for _, e := range doc.Entries {
		flags[strings.ToLower(e.Repo)] = e.Deploying
	}
	return flags, nil
}

// Deploying reports whether repo was recorded as deploying on merge, and false
// for a repository no entry names.
func (c *CDs) Deploying(ctx context.Context, repo string) (bool, error) {
	flags, err := c.Recorded(ctx)
	if err != nil {
		return false, err
	}
	return flags[strings.ToLower(repo)], nil
}

// Set records repo's CD flag: an operator's runner cd action. The entry is
// replaced in one transaction, so recording one repository never drops another's.
func (c *CDs) Set(ctx context.Context, repo string, deploying bool, at time.Time) error {
	err := c.client.RunTransaction(ctx, func(_ context.Context, tx *firestore.Transaction) error {
		var doc cdDoc
		snap, err := tx.Get(c.doc())
		switch {
		case err == nil:
			if err := snap.DataTo(&doc); err != nil {
				return fmt.Errorf("decoding the recorded CD flags: %w", err)
			}
		case status.Code(err) == codes.NotFound:
			// The first repository recorded creates the document.
		default:
			return err
		}
		doc.Entries = setCD(doc.Entries, CDEntry{Repo: repo, Deploying: deploying, At: at})
		return tx.Set(c.doc(), map[string]any{cdEntriesField: doc.Entries})
	})
	if err != nil {
		return fmt.Errorf("recording %s's CD flag: %w", repo, err)
	}
	return nil
}

// setCD replaces repo's entry, keeping the entries in the order they were first
// recorded. A repository is matched the way GitHub names one, case-insensitively,
// so a later recording under another case replaces the entry rather than adding a
// second the condition would have to choose between.
func setCD(entries []CDEntry, entry CDEntry) []CDEntry {
	out := make([]CDEntry, 0, len(entries)+1)
	replaced := false
	for _, e := range entries {
		if !strings.EqualFold(e.Repo, entry.Repo) {
			out = append(out, e)
			continue
		}
		if replaced {
			continue
		}
		replaced = true
		out = append(out, entry)
	}
	if !replaced {
		out = append(out, entry)
	}
	return out
}
