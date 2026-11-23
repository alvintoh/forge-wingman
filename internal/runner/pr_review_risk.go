package runner

import "strings"

// The risk levels of a pull request review.
const (
	RiskLow  = "low"
	RiskHigh = "high"
)

// ReviewRisk is how risky a reviewed change reads as. It is derived from the
// verdict and the review areas' configuration and never stored, so a verdict
// cannot state its own risk.
type ReviewRisk struct {
	Level string
	// Reasons say what raised the level, one phrase each, empty when it is low.
	Reasons []string
}

// High reports whether the risk is high.
func (r ReviewRisk) High() bool { return r.Level == RiskHigh }

// HasFindings reports whether the specialist reviewed and found something.
func (s SpecialistVerdict) HasFindings() bool {
	return s.Outcome == SpecialistReviewed && strings.TrimSpace(s.Findings) != ""
}

// Risk is high when any specialist reported findings, or when a specialist the
// configuration marks high_when_run ran at all, whether or not a model answered
// it; otherwise low, and always low for a review with no specialist. A
// specialist the configuration does not name raises the risk too: it cannot be
// checked against the flag, so it fails safe.
func (v PRVerdict) Risk() ReviewRisk {
	return riskOf(embeddedReviewAreas, v.Specialists)
}

func riskOf(areas []ReviewArea, specialists []SpecialistVerdict) ReviewRisk {
	var reasons []string
	configured := map[string]bool{}
	for _, a := range areas {
		configured[a.Name] = true
		for _, s := range specialists {
			switch {
			case s.Name != a.Name:
			case s.HasFindings():
				reasons = append(reasons, "findings from "+s.Name)
			case a.HighWhenRun:
				reasons = append(reasons, s.Name+" ran in a high-risk area")
			}
		}
	}
	for _, s := range specialists {
		if !configured[s.Name] {
			reasons = append(reasons, "a specialist outside the review configuration ran")
		}
	}
	if len(reasons) == 0 {
		return ReviewRisk{Level: RiskLow}
	}
	return ReviewRisk{Level: RiskHigh, Reasons: reasons}
}
