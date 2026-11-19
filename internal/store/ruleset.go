package store

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	rulesetMarkPrefix = "ruleset-required-"
	markedAtField     = "marked_at"
)

// RulesetMarks keeps one dispatch/ruleset-required-<repository> document per
// repository whose branch rules a poll has seen require the merge checks. A mark
// is never removed, so a repository that drops them stays in scope.
type RulesetMarks struct {
	client *firestore.Client
}

// NewRulesetMarks returns a RulesetMarks backed by client.
func NewRulesetMarks(client *firestore.Client) *RulesetMarks {
	return &RulesetMarks{client: client}
}

func (m *RulesetMarks) doc(repo string) *firestore.DocumentRef {
	return m.client.Collection(dispatchCollection).Doc(rulesetMarkPrefix + projectDocID(repo))
}

// MarkRequired creates repo's mark; one that exists keeps its first time.
func (m *RulesetMarks) MarkRequired(ctx context.Context, repo string, at time.Time) error {
	_, err := m.doc(repo).Create(ctx, map[string]any{markedAtField: at})
	if err != nil && status.Code(err) != codes.AlreadyExists {
		return fmt.Errorf("marking the rules of %s: %w", repo, err)
	}
	return nil
}

// SeenRequired reports whether repo has a mark.
func (m *RulesetMarks) SeenRequired(ctx context.Context, repo string) (bool, error) {
	_, err := m.doc(repo).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading the rules mark of %s: %w", repo, err)
	}
	return true, nil
}
