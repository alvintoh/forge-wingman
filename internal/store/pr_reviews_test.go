package store

import (
	"context"
	"math"
	"strings"
	"testing"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

const (
	reviewSHA      = "0123456789abcdef0123456789abcdef01234567"
	otherReviewSHA = "89abcdef0123456789abcdef0123456789abcdef"
	reviewModel    = "opencode-go/muse-spark-1.3-contributor"
)

func goodReview() ReviewRecord {
	return ReviewRecord{PR: 7, HeadSHA: reviewSHA, Risk: runner.RiskLow, Specialists: []string{"data"}, Total: 0.3, Calls: []ReviewCall{
		{Role: ReviewRoleGeneral, Model: reviewModel, Outcome: runner.AttemptReviewed, Input: 1200, Output: 80, Cost: 0.1},
		{Role: "data", Model: reviewModel, Outcome: runner.AttemptTimedOut, Cost: 0.2},
	}}
}

func TestReviewRecordRefusesWhatItsReaderWouldNotProduce(t *testing.T) {
	over := make([]ReviewCall, runner.MaxPRReviewCalls()+1)
	for i := range over {
		over[i] = ReviewCall{Role: ReviewRoleGeneral, Model: reviewModel, Outcome: runner.AttemptTimedOut}
	}
	for name, tt := range map[string]struct {
		edit func(*ReviewRecord)
		want string
	}{
		"a pull request of zero":                 {func(r *ReviewRecord) { r.PR = 0 }, "number"},
		"a negative pull request":                {func(r *ReviewRecord) { r.PR = -7 }, "number"},
		"a head that is not hex":                 {func(r *ReviewRecord) { r.HeadSHA = strings.Repeat("g", 40) }, "head"},
		"a head carrying a path":                 {func(r *ReviewRecord) { r.HeadSHA = "../" + reviewSHA }, "head"},
		"a risk of neither level":                {func(r *ReviewRecord) { r.Risk = "medium" }, "risk"},
		"more calls than can be made":            {func(r *ReviewRecord) { r.Calls = over }, "calls"},
		"the general review as a specialist":     {func(r *ReviewRecord) { r.Specialists = []string{ReviewRoleGeneral} }, "specialist"},
		"a specialist outside the configuration": {func(r *ReviewRecord) { r.Specialists = []string{"made-up"} }, "specialist"},
		"a call for an unknown role":             {func(r *ReviewRecord) { r.Calls[0].Role = "made-up" }, "role, model or outcome"},
		"a call on an invalid model":             {func(r *ReviewRecord) { r.Calls[0].Model = "no slash" }, "role, model or outcome"},
		"a negative token count":                 {func(r *ReviewRecord) { r.Calls[0].Input = -1 }, "out of range"},
		"a call on an unknown outcome":           {func(r *ReviewRecord) { r.Calls[0].Outcome = "made-up" }, "role, model or outcome"},
		"an empty outcome":                       {func(r *ReviewRecord) { r.Calls[0].Outcome = "" }, "role, model or outcome"},
		"a negative output count":                {func(r *ReviewRecord) { r.Calls[0].Output = -1 }, "out of range"},
		"a NaN cost":                             {func(r *ReviewRecord) { r.Calls[0].Cost = math.NaN() }, "out of range"},
		"a positive infinite cost":               {func(r *ReviewRecord) { r.Calls[0].Cost = math.Inf(1) }, "out of range"},
		"a negative infinite cost":               {func(r *ReviewRecord) { r.Calls[0].Cost = math.Inf(-1) }, "out of range"},
		"a negative total":                       {func(r *ReviewRecord) { r.Total = -0.1 }, "total"},
		"a NaN total":                            {func(r *ReviewRecord) { r.Total = math.NaN() }, "total"},
		"an infinite total":                      {func(r *ReviewRecord) { r.Total = math.Inf(1) }, "total"},
		"a total over the cap":                   {func(r *ReviewRecord) { r.Total = 1e6*float64(runner.MaxPRReviewCalls()) + 1 }, "total"},
		"a negative cost":                        {func(r *ReviewRecord) { r.Calls[1].Cost = -0.1 }, "out of range"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := goodReview()
			tt.edit(&rec)
			// No client: a refusal must come before any write.
			err := NewPRReviews(nil).Write(context.Background(), rec)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Write = %v, want a refusal naming %q", err, tt.want)
			}
		})
	}
}

// reviewsOver returns PRReviews over the emulator and removes the documents the
// test's pull request numbers wrote.
func reviewsOver(t *testing.T, prs ...int) (*PRReviews, *firestore.Client) {
	t.Helper()
	_, client := queue(t)
	clear := func() {
		ctx := context.Background()
		for _, pr := range prs {
			for _, sha := range []string{reviewSHA, otherReviewSHA} {
				_, _ = client.Collection(prReviewsCollection).Doc(reviewDocID(pr, sha)).Delete(ctx)
			}
		}
	}
	clear()
	t.Cleanup(clear)
	return NewPRReviews(client), client
}

// reviewDocs counts the stored documents of pull request pr.
func reviewDocs(t *testing.T, client *firestore.Client, pr int) int {
	t.Helper()
	iter := client.Collection(prReviewsCollection).Where("pr", "==", pr).Documents(context.Background())
	defer iter.Stop()
	n := 0
	for {
		if _, err := iter.Next(); err == iterator.Done {
			return n
		} else if err != nil {
			t.Fatal(err)
		}
		n++
	}
}

func TestReviewRecordIsStoredUnderItsPullRequestAndHeadWithItsCountAndTotal(t *testing.T) {
	p, client := reviewsOver(t, 7001)
	rec := goodReview()
	rec.PR = 7001
	if err := p.Write(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	snap, err := client.Collection(prReviewsCollection).Doc("7001-" + reviewSHA).Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	row := snap.Data()
	if row["call_count"] != int64(2) || row["total_cost"] != 0.3 || row["risk"] != "low" || row["head_sha"] != reviewSHA {
		t.Fatalf("stored %v, want a count of 2, a total of 0.3, risk low and the head", row)
	}
	if specialists := row["specialists"].([]any); len(specialists) != 1 || specialists[0] != "data" {
		t.Fatalf("specialists = %v, want [data]", specialists)
	}
	calls := row["calls"].([]any)
	first, second := calls[0].(map[string]any), calls[1].(map[string]any)
	if len(calls) != 2 || first["role"] != "general" || first["input"] != int64(1200) || first["output"] != int64(80) || first["cost"] != 0.1 ||
		second["role"] != "data" || second["outcome"] != runner.AttemptTimedOut || second["input"] != int64(0) {
		t.Fatalf("calls = %v, want the general call with its tokens and the specialist's without", calls)
	}
	if recorded, _ := row["recorded_at"].(interface{ IsZero() bool }); recorded == nil || recorded.IsZero() {
		t.Fatalf("recorded_at = %v, want the server's time", row["recorded_at"])
	}
}

func TestReviewRecordWithNoSpecialistStoresAnEmptyList(t *testing.T) {
	p, client := reviewsOver(t, 7002)
	rec := goodReview()
	rec.PR, rec.Specialists, rec.Calls = 7002, nil, rec.Calls[:1]
	if err := p.Write(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	snap, err := client.Collection(prReviewsCollection).Doc("7002-" + reviewSHA).Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if specialists, ok := snap.Data()["specialists"].([]any); !ok || len(specialists) != 0 {
		t.Fatalf("specialists = %#v, want an empty list and not null", snap.Data()["specialists"])
	}
}

func TestASecondWriteOfTheSameHeadReplacesTheDocumentAndANewHeadAddsOne(t *testing.T) {
	p, client := reviewsOver(t, 7003)
	ctx := context.Background()
	first := goodReview()
	first.PR = 7003
	if err := p.Write(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Calls, second.Specialists, second.Total = first.Calls[:1], nil, 0.1
	if err := p.Write(ctx, second); err != nil {
		t.Fatal(err)
	}
	if n := reviewDocs(t, client, 7003); n != 1 {
		t.Fatalf("%d documents after a second write of one head, want 1", n)
	}
	snap, err := client.Collection(prReviewsCollection).Doc("7003-" + reviewSHA).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if row := snap.Data(); row["call_count"] != int64(1) || row["total_cost"] != 0.1 || len(row["calls"].([]any)) != 1 || len(row["specialists"].([]any)) != 0 {
		t.Fatalf("stored %v, want the second write replacing the first whole", row)
	}
	moved := second
	moved.HeadSHA = otherReviewSHA
	if err := p.Write(ctx, moved); err != nil {
		t.Fatal(err)
	}
	if n := reviewDocs(t, client, 7003); n != 2 {
		t.Fatalf("%d documents after a new head, want 2", n)
	}
}

// A review at every cap the verdict reader allows is stored; one call more is not.
func TestReviewRecordTakesTheMostCallsAReviewCanMake(t *testing.T) {
	p, _ := reviewsOver(t, 7004)
	rec := goodReview()
	rec.PR, rec.Specialists = 7004, nil
	rec.Calls = make([]ReviewCall, runner.MaxPRReviewCalls())
	for i := range rec.Calls {
		rec.Calls[i] = ReviewCall{Role: ReviewRoleGeneral, Model: reviewModel, Outcome: runner.AttemptTimedOut}
	}
	if err := p.Write(context.Background(), rec); err != nil {
		t.Fatalf("Write of %d calls = %v, want it stored", len(rec.Calls), err)
	}
}

// stored is what Write puts in the document, checked without the emulator.
func TestReviewRecordIsStoredWithItsCallCountAndAnEmptySpecialistList(t *testing.T) {
	rec := goodReview()
	rec.CallCount, rec.Specialists = 99, nil
	got := rec.stored()
	if got.CallCount != 2 || got.Specialists == nil || len(got.Specialists) != 0 {
		t.Fatalf("stored = count %d, specialists %#v, want 2 and an empty list", got.CallCount, got.Specialists)
	}
}

func TestReviewRecordAcceptsTheLargestTotalItsCallsCanSum(t *testing.T) {
	rec := goodReview()
	rec.Total = 1e6 * float64(runner.MaxPRReviewCalls())
	if err := rec.validate(); err != nil {
		t.Fatalf("validate = %v, want the largest possible total accepted", err)
	}
}
