package store

import (
	"context"
	"strings"
	"testing"
)

func TestRulesetMarkIsSeenOnceMarked(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	m := NewRulesetMarks(client)
	repo := "Octo/" + fresh("Scratch")
	t.Cleanup(func() { _, _ = m.doc(repo).Delete(context.Background()) })
	if seen, err := m.SeenRequired(ctx, repo); err != nil || seen {
		t.Fatalf("seen = %v, %v before any mark", seen, err)
	}
	for range 2 {
		if err := m.MarkRequired(ctx, repo, noticeAt); err != nil {
			t.Fatal(err)
		}
	}
	if seen, err := m.SeenRequired(ctx, strings.ToLower(repo)); err != nil || !seen {
		t.Fatalf("seen = %v, %v after a mark, read in another case", seen, err)
	}
}
