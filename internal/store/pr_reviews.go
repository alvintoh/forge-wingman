package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

const prReviewsCollection = "pr_reviews"

// ReviewRoleGeneral is the role of the general review's calls; a specialist's
// calls carry its review area's name.
const ReviewRoleGeneral = "general"

// ReviewCall is one model call of a pull request review: who it was for, the
// model, how it ended, and its tokens and dollar figure. Only the call that
// answered has tokens.
type ReviewCall struct {
	Role    string  `firestore:"role"`
	Model   string  `firestore:"model"`
	Outcome string  `firestore:"outcome"`
	Input   int64   `firestore:"input"`
	Output  int64   `firestore:"output"`
	Cost    float64 `firestore:"cost"`
}

// ReviewRecord is what one review of a pull request head cost: every model call
// it made, their count and their total in dollars, the risk level and the
// specialists the review asked for. It holds no finding text.
type ReviewRecord struct {
	PR      int    `firestore:"pr"`
	HeadSHA string `firestore:"head_sha"`
	Risk    string `firestore:"risk"`
	// Specialists are the review areas the review asked for, in configuration order.
	Specialists []string     `firestore:"specialists"`
	Calls       []ReviewCall `firestore:"calls"`
	// CallCount is len(Calls); Write sets it, so it can never disagree.
	CallCount int     `firestore:"call_count"`
	Total     float64 `firestore:"total_cost"`
	// RecordedAt is the server's time of the latest write.
	RecordedAt time.Time `firestore:"recorded_at,serverTimestamp"`
}

// PRReviews keeps one pr_reviews document per reviewed pull request head.
type PRReviews struct {
	client *firestore.Client
}

// NewPRReviews returns a PRReviews backed by client.
func NewPRReviews(client *firestore.Client) *PRReviews {
	return &PRReviews{client: client}
}

// reviewDocID names the document of one pull request head, built only from
// validated parts.
func reviewDocID(pr int, sha string) string {
	return strconv.Itoa(pr) + "-" + sha
}

// Write stores rec under its pull request and head, replacing a document a
// review of the same head wrote before: a second post of one review is the same
// document, and a new head is a new one.
func (p *PRReviews) Write(ctx context.Context, rec ReviewRecord) error {
	if err := rec.validate(); err != nil {
		return fmt.Errorf("recording the review: %w", err)
	}
	rec = rec.stored()
	if _, err := p.client.Collection(prReviewsCollection).Doc(reviewDocID(rec.PR, rec.HeadSHA)).Set(ctx, rec); err != nil {
		return fmt.Errorf("recording the review: %w", err)
	}
	return nil
}

// stored is r as the document holds it: the count of its calls, and an empty
// list where there is no specialist, which Firestore would otherwise store as null.
func (r ReviewRecord) stored() ReviewRecord {
	r.CallCount = len(r.Calls)
	if r.Specialists == nil {
		r.Specialists = []string{}
	}
	return r
}

// validate refuses a record whose id could be forged or whose lists the review's
// own reader would never produce.
func (r ReviewRecord) validate() error {
	switch {
	case r.PR <= 0:
		return errors.New("the pull request number is not positive")
	case !runner.ValidCommitSHA(r.HeadSHA):
		return errors.New("the head is not a commit sha")
	case r.Risk != runner.RiskLow && r.Risk != runner.RiskHigh:
		return errors.New("the risk is not low or high")
	case len(r.Calls) > runner.MaxPRReviewCalls():
		return fmt.Errorf("%d calls is over the %d a review can make", len(r.Calls), runner.MaxPRReviewCalls())
	case !runner.ValidTotalCost(r.Total):
		return errors.New("the total is not a dollar figure")
	case slices.ContainsFunc(r.Specialists, func(name string) bool { return !validReviewRole(name) || name == ReviewRoleGeneral }):
		return errors.New("a specialist is not a configured review area")
	case slices.ContainsFunc(r.Calls, func(c ReviewCall) bool {
		return !validReviewRole(c.Role) || !runner.ValidModel(c.Model) || !runner.ValidAttemptOutcome(c.Outcome)
	}):
		return errors.New("a call names a role, model or outcome outside the review")
	case slices.ContainsFunc(r.Calls, func(c ReviewCall) bool { return c.Input < 0 || c.Output < 0 || !runner.ValidCost(c.Cost) }):
		return errors.New("a call has a token count or cost out of range")
	}
	return nil
}

// validReviewRole reports whether role is the general review or a configured
// review area.
func validReviewRole(role string) bool {
	return role == ReviewRoleGeneral || slices.ContainsFunc(runner.ReviewAreas(), func(a runner.ReviewArea) bool { return a.Name == role })
}
