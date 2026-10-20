package store

import (
	"context"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

func TestRecordReachWritesOneDocumentPerRepositoryAndRewritesIt(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	repo := "Octo/" + fresh("Widget")
	doc := client.Collection(reachCollection).Doc(projectDocID(repo))
	t.Cleanup(func() { _, _ = doc.Delete(context.Background()) })
	r := NewReaches(client)
	at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

	if err := r.RecordReach(ctx, []dispatcher.RepoReach{{Repository: repo, Reason: "forbidden", Status: 403, CheckedAt: at}}); err != nil {
		t.Fatal(err)
	}
	snap, err := doc.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data := snap.Data()
	if data["reachable"] != false || data["reason"] != "forbidden" || data["status"] != int64(403) || !data["checked_at"].(time.Time).Equal(at) {
		t.Fatalf("stored %v, want a forbidden 403 read at %v", data, at)
	}

	later := at.Add(time.Hour)
	if err := r.RecordReach(ctx, []dispatcher.RepoReach{{Repository: repo, Reachable: true, CheckedAt: later}}); err != nil {
		t.Fatal(err)
	}
	snap, err = doc.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data = snap.Data()
	if data["reachable"] != true || data["reason"] != "" || data["status"] != int64(0) || !data["checked_at"].(time.Time).Equal(later) {
		t.Fatalf("stored %v, want the later read to replace the first", data)
	}
}
