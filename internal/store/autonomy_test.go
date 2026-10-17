package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// autonomiesOver returns Autonomies over the emulator with no document, and
// removes the document afterwards.
func autonomiesOver(t *testing.T) (*Autonomies, *firestore.Client) {
	t.Helper()
	_, client := queue(t)
	doc := client.Collection(dispatchCollection).Doc(autonomyDocID)
	_, _ = doc.Delete(context.Background())
	t.Cleanup(func() { _, _ = doc.Delete(context.Background()) })
	return NewAutonomies(client), client
}

func TestAutonomyIsZeroWhileNoneIsSet(t *testing.T) {
	a, _ := autonomiesOver(t)
	if level, err := a.Level(context.Background()); err != nil || level != 0 {
		t.Fatalf("level = %d, %v, want 0 with no document", level, err)
	}
}

func TestAutonomySetIsReadBackOnTheNextRead(t *testing.T) {
	a, client := autonomiesOver(t)
	ctx := context.Background()
	if err := a.Set(ctx, 3, noticeAt); err != nil {
		t.Fatal(err)
	}
	if level, err := a.Level(ctx); err != nil || level != 3 {
		t.Fatalf("level = %d, %v, want 3", level, err)
	}
	snap, err := client.Collection(dispatchCollection).Doc(autonomyDocID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Data(); got[levelField] != int64(3) || !got[setAtField].(time.Time).Equal(noticeAt) {
		t.Fatalf("stored = %v, want level 3 set at %v", got, noticeAt)
	}
}

func TestAutonomySetRefusesALevelTheOwnerMayNotSet(t *testing.T) {
	a, client := autonomiesOver(t)
	ctx := context.Background()
	for level, want := range map[dispatcher.Autonomy]error{
		4: dispatcher.ErrAutonomyNotEarned, 5: dispatcher.ErrAutonomyNotEarned, 6: dispatcher.ErrUnknownAutonomy, -1: dispatcher.ErrUnknownAutonomy,
	} {
		if err := a.Set(ctx, level, noticeAt); !errors.Is(err, want) {
			t.Errorf("Set(%d) err = %v, want %v", level, err, want)
		}
	}
	if _, err := client.Collection(dispatchCollection).Doc(autonomyDocID).Get(ctx); err == nil {
		t.Fatal("a refused Set wrote the document")
	}
}

func TestAutonomyRefusesAStoredValueNamingNoLevel(t *testing.T) {
	a, client := autonomiesOver(t)
	ctx := context.Background()
	for name, doc := range map[string]map[string]any{
		"a level out of range": {levelField: int64(9)},
		"a level as a string":  {levelField: "4"},
		"no level field":       {setAtField: noticeAt},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := client.Collection(dispatchCollection).Doc(autonomyDocID).Set(ctx, doc); err != nil {
				t.Fatal(err)
			}
			if level, err := a.Level(ctx); !errors.Is(err, dispatcher.ErrUnknownAutonomy) || level != 0 {
				t.Fatalf("Level = %d, %v, want ErrUnknownAutonomy so the dispatcher counts it as 0", level, err)
			}
		})
	}
}

func TestAutonomySetWritesALevelWhoseConditionsTheMeasuredRowsHold(t *testing.T) {
	a, client := autonomiesOver(t)
	ctx := context.Background()
	// Ten merged PRs the owner rewrote a line of each, and the CD flag for the
	// repository they merged into: the trailing rate is 1%.
	cdDoc := client.Collection(dispatchCollection).Doc(cdDocID)
	_, _ = cdDoc.Delete(ctx)
	t.Cleanup(func() { _, _ = cdDoc.Delete(context.Background()) })
	if err := NewCDs(client).Set(ctx, "octo/scratch", true, noticeAt); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	for i := range 10 {
		id := fresh("autonomy-measured")
		forget(t, client, id)
		if _, err := client.Collection(runsCollection).Doc(id).Set(ctx, map[string]any{
			repoField: "octo/scratch", autoMergeField: true,
		}); err != nil {
			t.Fatal(err)
		}
		measured := dispatcher.Measured{
			Rewrite:  dispatcher.Rewrite{AgentLines: 100, OwnerLines: 1},
			MergedAt: at.Add(time.Duration(i) * time.Minute),
		}
		if err := NewRewrites(client).Record(ctx, id, measured); err != nil {
			t.Fatal(err)
		}
	}
	// Level 3 is set outright, and level 4 is earned on top of it.
	if err := a.Set(ctx, 3, noticeAt); err != nil {
		t.Fatal(err)
	}
	if err := a.Set(ctx, 4, noticeAt); err != nil {
		t.Fatalf("Set(4) = %v, want a level whose every clause holds written", err)
	}
	if level, err := a.Level(ctx); err != nil || level != 4 {
		t.Fatalf("level = %d, %v, want 4", level, err)
	}
	// The same rows without the recorded CD flag cannot carry it.
	if _, err := cdDoc.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Set(ctx, 5, noticeAt); !errors.Is(err, dispatcher.ErrAutonomyNotEarned) {
		t.Fatalf("Set(5) = %v, want a level with fewer than 20 sampled PRs refused", err)
	}
	if err := a.Set(ctx, 4, noticeAt); !errors.Is(err, dispatcher.ErrAutonomyNotEarned) || !strings.Contains(err.Error(), "octo/scratch") {
		t.Fatalf("Set(4) = %v, want the refusal to name the repository with no CD flag", err)
	}
}

// claimedAutonomy reads the level run id's row records, and whether it records one.
func claimedAutonomy(t *testing.T, client *firestore.Client, id string) (any, bool) {
	t.Helper()
	snap, err := client.Collection(runsCollection).Doc(id).Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	v, ok := snap.Data()[autonomyField]
	return v, ok
}

func TestTryClaimRecordsTheLevelOnceAndALaterClaimKeepsIt(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-autonomy"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 2)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, recorded := claimedAutonomy(t, client, run.RunID); recorded {
		t.Fatal("a queued run records a level before any claim")
	}
	at3 := openFacts
	at3.Autonomy = 3
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, at3, dispatcher.Reservation{ProviderCost: money.Dollar, Stage: dispatcher.StagePlan}); err != nil || !ok {
		t.Fatalf("plan claim: ok %v, err %v", ok, err)
	}
	rec, err := NewRecords(client).GetRecord(ctx, run.RunID)
	if err != nil || rec.Autonomy != 3 {
		t.Fatalf("record autonomy = %d, %v, want 3 as pr-meta reads it", rec.Autonomy, err)
	}
	if _, err := client.Collection(runsCollection).Doc(run.RunID).Update(ctx, []firestore.Update{{Path: stateField, Value: statePlanned}}); err != nil {
		t.Fatal(err)
	}
	at1 := openFacts
	at1.Autonomy = 1
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, at1, dispatcher.Reservation{ProviderCost: money.Dollar, Stage: dispatcher.StageBuild}); err != nil || !ok {
		t.Fatalf("build claim: ok %v, err %v", ok, err)
	}
	if got, _ := claimedAutonomy(t, client, run.RunID); got != int64(3) {
		t.Fatalf("autonomy = %v after the build claim, want the plan claim's 3 kept", got)
	}
}

func TestRequeueClearsTheLevelSoTheNextAttemptRecordsItsOwn(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-autonomy-requeue"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 1)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	at2 := openFacts
	at2.Autonomy = 2
	res := dispatcher.Reservation{ProviderCost: money.Dollar, Stage: dispatcher.StagePlan}
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, at2, res); err != nil || !ok {
		t.Fatalf("claim: ok %v, err %v", ok, err)
	}
	if err := q.Stop(ctx, run.RunID, runner.StopPlanStageFailed, "the plan stage failure", queueAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Requeue(ctx, run.RunID, queueAt.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, recorded := claimedAutonomy(t, client, run.RunID); recorded {
		t.Fatalf("a requeued run still records level %v", got)
	}
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt.Add(3*time.Minute), generousBudget, openFacts, res); err != nil || !ok {
		t.Fatalf("second claim: ok %v, err %v", ok, err)
	}
	if got, _ := claimedAutonomy(t, client, run.RunID); got != int64(0) {
		t.Fatalf("autonomy = %v, want the second attempt's 0", got)
	}
}

// TestAutonomyViewReadsTheLevelTheSnapshotAndTheHistoryNewestFirst asserts the
// surface's whole read in one call: the level the owner set, the snapshot the
// measured PRs last earned with the numbers behind it, and the changes
// recorded, newest first (AC1, AC2).
func TestAutonomyViewReadsTheLevelTheSnapshotAndTheHistoryNewestFirst(t *testing.T) {
	a, client := autonomiesOver(t)
	ctx := context.Background()
	h := NewAutonomyHistory(client)
	clearHistory(t, h)
	if err := a.Set(ctx, 3, noticeAt); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	if err := a.SetEarned(ctx, dispatcher.Earned{Level: 4, ComputedAt: at, Numbers: dispatcher.Numbers{
		PRs: []int{9, 8}, Rate: 0.02, AgentLines: 100, OwnerLines: 2,
		ReviewFoundLines: 1, OwnerChangeLines: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []dispatcher.Change{
		{From: 3, To: 4, Cause: dispatcher.ChangeAdvance, Numbers: dispatcher.Numbers{PRs: []int{8}, AgentLines: 100}, At: at},
		{From: 4, To: 3, Cause: string(dispatcher.RollbackSampledRate),
			Numbers: dispatcher.Numbers{PRs: []int{9}, Rate: 0.2, AgentLines: 100, OwnerLines: 20}, At: at.Add(time.Hour)},
	} {
		if err := h.Append(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	view, err := a.AutonomyView(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if view.Level != 3 || !view.HasEarned || view.Earned.Level != 4 || !view.Earned.ComputedAt.Equal(at) ||
		view.Earned.Numbers.Rate != 0.02 || len(view.Earned.Numbers.PRs) != 2 || view.Earned.Numbers.ReviewFoundLines != 1 {
		t.Fatalf("view = %+v, want level 3 with the earned snapshot read back whole", view)
	}
	if len(view.History) != 2 || view.History[0].Cause != string(dispatcher.RollbackSampledRate) ||
		view.History[1].Cause != dispatcher.ChangeAdvance {
		t.Fatalf("history = %+v, want the drop newest first", view.History)
	}
	if view.History[0].From != 4 || view.History[0].To != 3 || view.History[0].Numbers.OwnerLines != 20 ||
		!view.History[0].At.Equal(at.Add(time.Hour)) {
		t.Fatalf("newest change = %+v, want its levels, numbers and time read back", view.History[0])
	}
	if one, err := a.AutonomyView(ctx, 1); err != nil || len(one.History) != 1 ||
		one.History[0].Cause != string(dispatcher.RollbackSampledRate) {
		t.Fatalf("history = %+v, %v, want the limit to keep the newest change alone", one.History, err)
	}
}

// TestAutonomyViewReadsTheMeasuredMergesNewestFirst asserts the surface's read
// carries the merged PRs a page draws its rewrite figures from, newest merge
// first, each naming the run it was measured on, its repository and its size
// (AC3, AC5).
func TestAutonomyViewReadsTheMeasuredMergesNewestFirst(t *testing.T) {
	a, client := autonomiesOver(t)
	ctx := context.Background()
	oldest, newest := fresh("view-measured-oldest"), fresh("view-measured-newest")
	forget(t, client, oldest, newest)
	r := NewRewrites(client)
	// The merges are this run's own, so rows an earlier run left in the shared
	// emulator sort behind them.
	at := time.Now().UTC()
	for i, id := range []string{oldest, newest} {
		if err := r.Record(ctx, id, dispatcher.Measured{
			Rewrite:  dispatcher.Rewrite{AgentLines: int64(100 + i), OwnerLines: 1},
			MergedAt: at.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// The older row carries the fields the sample reads beside the measurement.
	if _, err := client.Collection(runsCollection).Doc(oldest).Set(ctx, map[string]any{
		repoField: "octo/scratch", sizeField: "M",
	}, firestore.MergeAll); err != nil {
		t.Fatal(err)
	}

	view, err := a.AutonomyView(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Measured) < 2 {
		t.Fatalf("Measured = %+v, want the two merges just recorded", view.Measured)
	}
	newestRow, oldestRow := view.Measured[0], view.Measured[1]
	if newestRow.Run != newest || oldestRow.Run != oldest {
		t.Fatalf("Measured = %+v, want the newest merge first", view.Measured)
	}
	if oldestRow.Repo != "octo/scratch" || oldestRow.Size != "M" || oldestRow.AgentLines != 100 {
		t.Fatalf("oldest = %+v, want its repository, size and measurement read with it", oldestRow)
	}
	if newestRow.Repo != "" || newestRow.Size != "" || newestRow.AgentLines != 101 {
		t.Fatalf("newest = %+v, want only the fields that row carries", newestRow)
	}
}

// TestAutonomyViewReportsAMeasuredReadItCannotMake asserts a view whose measured
// merges cannot be read is an error, rather than a view of empty figures a page
// would draw as nothing measured (AC3).
func TestAutonomyViewReportsAMeasuredReadItCannotMake(t *testing.T) {
	_, err := measuredUnreadable(t).AutonomyView(context.Background(), 1)
	if err == nil || !strings.Contains(err.Error(), "listing the measured runs") {
		t.Fatalf("AutonomyView err = %v, want the measured read reported", err)
	}
}

// measuredUnreadable returns Autonomies over a client that refuses the query the
// measured merges are read with, while its point reads and the history query go
// through, so a test drives the view's own handling of that read alone.
func measuredUnreadable(t *testing.T) *Autonomies {
	t.Helper()
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	intercept := func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		stream, err := streamer(metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer owner"), desc, cc, method, opts...)
		if err != nil || !strings.HasSuffix(method, "/RunQuery") {
			return stream, err
		}
		return &refusingRuns{ClientStream: stream}, nil
	}
	conn, err := grpc.NewClient(os.Getenv("FIRESTORE_EMULATOR_HOST"),
		grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithChainStreamInterceptor(intercept))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client, err := firestore.NewClient(context.Background(), "forge-wingman-test", option.WithGRPCConn(conn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return NewAutonomies(client)
}

// refusingRuns refuses each query over the runs, sending every other query on.
type refusingRuns struct {
	grpc.ClientStream
}

func (s *refusingRuns) SendMsg(m any) error {
	if req, ok := m.(*firestorepb.RunQueryRequest); ok {
		if from := req.GetStructuredQuery().GetFrom(); len(from) > 0 && from[0].GetCollectionId() == runsCollection {
			return status.Error(codes.PermissionDenied, "the runs are unreadable")
		}
	}
	return s.ClientStream.SendMsg(m)
}

// TestAutonomyViewWithoutADocumentOrHistoryReadsEmpty covers the surface's first
// load: no level set, no snapshot stored, no change recorded and no merge
// measured — a measured row left behind reads here rather than passing unnoticed
// (AC6).
func TestAutonomyViewWithoutADocumentOrHistoryReadsEmpty(t *testing.T) {
	a, client := autonomiesOver(t)
	clearHistory(t, NewAutonomyHistory(client))
	view, err := a.AutonomyView(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if view.Level != 0 || view.HasEarned || len(view.History) != 0 || len(view.Measured) != 0 {
		t.Fatalf("view = %+v, want no level, no snapshot, no change and no measured merge", view)
	}
}
