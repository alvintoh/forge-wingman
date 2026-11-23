package runner

import (
	"slices"
	"strings"
	"testing"
)

func specialist(name, outcome, findings string) SpecialistVerdict {
	return SpecialistVerdict{Name: name, Outcome: outcome, Findings: findings}
}

func TestRiskOf(t *testing.T) {
	areas := []ReviewArea{{Name: "calm"}, {Name: "hot", HighWhenRun: true}, {Name: "other"}}
	for name, tt := range map[string]struct {
		specialists []SpecialistVerdict
		want        string
		reasons     []string
	}{
		"no specialist ran":                                {nil, RiskLow, nil},
		"an empty list":                                    {[]SpecialistVerdict{}, RiskLow, nil},
		"a calm area, no findings":                         {[]SpecialistVerdict{specialist("calm", SpecialistReviewed, "")}, RiskLow, nil},
		"whitespace is no finding":                         {[]SpecialistVerdict{specialist("calm", SpecialistReviewed, " \n\t")}, RiskLow, nil},
		"a calm area that was not reviewed":                {[]SpecialistVerdict{specialist("calm", SpecialistNotReviewed, "")}, RiskLow, nil},
		"a calm area with findings":                        {[]SpecialistVerdict{specialist("calm", SpecialistReviewed, "x")}, RiskHigh, []string{"findings from calm"}},
		"a hot area, no findings":                          {[]SpecialistVerdict{specialist("hot", SpecialistReviewed, "")}, RiskHigh, []string{"hot ran in a high-risk area"}},
		"a hot area that was not reviewed":                 {[]SpecialistVerdict{specialist("hot", SpecialistNotReviewed, "")}, RiskHigh, []string{"hot ran in a high-risk area"}},
		"a hot area with findings names the findings once": {[]SpecialistVerdict{specialist("hot", SpecialistReviewed, "x")}, RiskHigh, []string{"findings from hot"}},
		"an area outside the configuration": {[]SpecialistVerdict{specialist("<b>@octocat</b>", SpecialistReviewed, "")}, RiskHigh,
			[]string{"a specialist outside the review configuration ran"}},
		"reasons follow the configuration, the unknown last": {
			[]SpecialistVerdict{specialist("stray", SpecialistReviewed, ""), specialist("other", SpecialistReviewed, "y"), specialist("hot", SpecialistNotReviewed, "")},
			RiskHigh, []string{"hot ran in a high-risk area", "findings from other", "a specialist outside the review configuration ran"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := riskOf(areas, tt.specialists)
			if got.Level != tt.want || !slices.Equal(got.Reasons, tt.reasons) || got.High() != (tt.want == RiskHigh) {
				t.Fatalf("risk = %+v, want %s %q", got, tt.want, tt.reasons)
			}
			for _, r := range got.Reasons {
				if strings.Contains(r, "octocat") {
					t.Fatalf("reason %q carries an unconfigured name", r)
				}
			}
		})
	}
}

// TestVerdictRiskReadsTheConfiguredFlag holds the shipped configuration to the
// flags its areas carry: the verdict takes them from the file, not from itself.
func TestVerdictRiskReadsTheConfiguredFlag(t *testing.T) {
	for _, a := range ReviewAreas() {
		v := PRVerdict{Specialists: []SpecialistVerdict{specialist(a.Name, SpecialistReviewed, "")}}
		if got := v.Risk().High(); got != a.HighWhenRun {
			t.Errorf("%s: high = %v, want its high_when_run %v", a.Name, got, a.HighWhenRun)
		}
	}
	if (PRVerdict{}).Risk().Level != RiskLow {
		t.Error("a verdict with no specialist is not low risk")
	}
}
