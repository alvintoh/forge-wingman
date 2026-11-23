package runner

// MaxPRReviewCalls is the most model calls one verdict can carry: the verdict
// reader's cap on the general review's attempts and on each configured
// specialist's. A record of a review's calls is written within it.
func MaxPRReviewCalls() int {
	return maxPRReviewAttempts * (1 + len(embeddedReviewAreas))
}

// ValidCommitSHA reports whether sha is a full lowercase commit sha.
func ValidCommitSHA(sha string) bool {
	return shaPattern.MatchString(sha)
}

// ValidCost reports whether cost is a dollar figure the review artifacts accept:
// finite, not negative and within the cap, so NaN and the infinities fail.
func ValidCost(cost float64) bool {
	return cost >= 0 && cost <= maxCost
}

// ValidAttemptOutcome reports whether outcome is one a model attempt ends in.
func ValidAttemptOutcome(outcome string) bool {
	return attemptOutcomes[outcome]
}

// ValidTotalCost reports whether total is a dollar figure a review's calls can
// sum to: finite, not negative and within the cap on every call a review can make.
func ValidTotalCost(total float64) bool {
	return total >= 0 && total <= maxCost*float64(MaxPRReviewCalls())
}
