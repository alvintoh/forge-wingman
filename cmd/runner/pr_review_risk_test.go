package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// unfencedRiskLines are the lines of body that state a risk outside every code
// fence: the ones a reader takes for the review's own.
func unfencedRiskLines(body string) []string {
	var found []string
	fence := ""
	for _, line := range strings.Split(body, "\n") {
		switch {
		case fence != "":
			if line == fence {
				fence = ""
			}
		case reviewFenceLine.MatchString(line):
			fence = reviewFenceLine.FindStringSubmatch(line)[1]
		case strings.HasPrefix(line, "Risk: "):
			found = append(found, line)
		}
	}
	return found
}

// TestPRReviewPostHoldsTheMergeOnTheVerdict posts each state a review can be in
// with every gate outside the verdict open: the pull request eligible at the
// reviewed head, the switch on, the checks green and the rate under the
// guideline. Only the verdict can hold the merge.
func TestPRReviewPostHoldsTheMergeOnTheVerdict(t *testing.T) {
	const (
		clean     = "Clean: no findings from " + museModel
		exhausted = "No verdict: allowance exhausted; waits for the owner"
	)
	for name, tt := range map[string]struct {
		v                runner.PRVerdict
		wantMerge        bool
		wantReason       string
		wantRisk         string
		wantState, wantD string
		wantComment      string
	}{
		"clean, no specialist": {verdict("clean", "", museModel), true, "", "Risk: low", "success", clean, ""},
		"clean, a low-risk specialist found nothing": {withSpecialists(verdict("clean", "", museModel), reviewedBy("data", "", 0.1)),
			true, "", "Risk: low", "success", clean, "No findings in this area."},
		"clean, a high-when-run specialist found nothing": {withSpecialists(verdict("clean", "", museModel), reviewedBy("cloud", "", 0.1)),
			false, riskHighHoldReason, "Risk: high (cloud ran in a high-risk area)", "success", clean + "; risk high",
			"waits for the owner: the risk is high."},
		"clean, a specialist found something": {withSpecialists(verdict("clean", "", museModel), reviewedBy("data", "d1", 0.1)),
			false, "", "Risk: high (findings from data)", "failure", "Findings from data; see the review comment", ""},
		"clean, a low-risk specialist was not reviewed": {withSpecialists(verdict("clean", "", museModel), missedBy("infra", runner.SpecialistReasonDeadline)),
			false, specialistNotReviewedHoldReason, "Risk: low", "success", clean + "; not reviewed: infra",
			"waits for the owner: a specialist was not reviewed: infra."},
		"clean, a high-when-run specialist was not reviewed": {withSpecialists(verdict("clean", "", museModel), missedBy("data", runner.SpecialistReasonDeadline), missedBy("cloud", runner.SpecialistReasonDeadline)),
			false, specialistNotReviewedHoldReason, "Risk: high (cloud ran in a high-risk area)", "success", clean + "; risk high; not reviewed: data, cloud",
			"waits for the owner: a specialist was not reviewed: data, cloud."},
		"exhausted, no specialist": {spentAllowance(), true, "", "Risk: low", "failure", "No verdict: allowance exhausted; merges without review", "merged without review"},
		"exhausted, a low-risk specialist found nothing": {withSpecialists(spentAllowance(), reviewedBy("data", "", 0.1)),
			true, "", "Risk: low", "failure", "No verdict: allowance exhausted; merges without review", "merged without review"},
		"exhausted, a low-risk specialist was not reviewed": {withSpecialists(spentAllowance(), missedBy("infra", runner.SpecialistReasonDeadline)),
			false, specialistNotReviewedHoldReason, "Risk: low", "failure", exhausted + "; not reviewed: infra",
			"waits for the owner: a specialist was not reviewed: infra."},
		"exhausted, a high-when-run specialist found nothing": {withSpecialists(spentAllowance(), reviewedBy("security", "", 0.1)),
			false, riskHighHoldReason, "Risk: high (security ran in a high-risk area)", "failure", exhausted + "; risk high",
			"waits for the owner: the risk is high."},
		"exhausted, a specialist found something": {withSpecialists(spentAllowance(), reviewedBy("data", "d1", 0.1)),
			false, specialistFindingsHoldReason, "Risk: high (findings from data)", "failure", exhausted,
			"waits for the owner: a specialist reported findings."},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakePulls{pull: openPull("t", "", headSHA, autoMergeEligibleLabel), checkRuns: greenChecks()}
			getenv := f.start(t)
			var logs bytes.Buffer
			settingsReads := 0
			settings := func(ctx context.Context) (store.AutoMergeSetting, error) {
				settingsReads++
				return switchedOn(ctx)
			}
			args := []string{"-pr", "7", "-reviewed-sha", headSHA, "-verdict", writeVerdict(t, tt.v), "-context-result", "success"}
			if err := prReviewPostWith(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), getenv, settings, rateUnder, recordNothing, args); err != nil {
				t.Fatal(err)
			}
			wantOutput := "merge=false"
			if tt.wantMerge {
				wantOutput = "merge=true"
			}
			if got := readOutput(t, getenv); !strings.Contains(got, wantOutput) {
				t.Fatalf("output = %q, want %q", got, wantOutput)
			}
			if tt.wantReason != "" {
				if got := logs.String(); !strings.Contains(got, "prReviewMergeDeclined") || !strings.Contains(got, "reason="+tt.wantReason) {
					t.Fatalf("log = %q, want the decline and reason %s", got, tt.wantReason)
				}
				if tt.v.Outcome == runner.PRReviewClean && settingsReads != 0 {
					t.Fatalf("the settings were read %d times for a review the verdict holds", settingsReads)
				}
			}
			if len(f.created) != 1 {
				t.Fatalf("comments = %v, want one", f.created)
			}
			if got := unfencedRiskLines(f.created[0]); len(got) != 1 || got[0] != tt.wantRisk {
				t.Fatalf("risk lines = %q, want %q alone", got, tt.wantRisk)
			}
			if !strings.Contains(f.created[0], tt.wantComment) {
				t.Fatalf("comment = %q, want %q", f.created[0], tt.wantComment)
			}
			if s := f.statuses[headSHA]; s.State != tt.wantState || s.Description != tt.wantD {
				t.Fatalf("status = %+v, want %s %q", s, tt.wantState, tt.wantD)
			}
		})
	}
}

// A verdict naming a specialist outside the configuration is refused at the
// reader, so the review is a failed one and requests nothing.
func TestPRReviewPostRefusesASpecialistOutsideTheConfiguration(t *testing.T) {
	f := &fakePulls{pull: openPull("t", "", headSHA, autoMergeEligibleLabel), checkRuns: greenChecks()}
	getenv := f.start(t)
	v := withSpecialists(verdict("clean", "", museModel), reviewedBy("not-an-area", "", 0.1))
	args := []string{"-pr", "7", "-reviewed-sha", headSHA, "-verdict", writeVerdict(t, v), "-context-result", "success"}
	if err := prReviewPostWith(context.Background(), quietLogger(), getenv, switchedOn, rateUnder, recordNothing, args); err != nil {
		t.Fatal(err)
	}
	if got := readOutput(t, getenv); !strings.Contains(got, "merge=false") {
		t.Fatalf("output = %q, want no merge requested", got)
	}
	if s := f.statuses[headSHA]; s.Description != noVerdictFailedStatusDescription {
		t.Fatalf("status = %+v, want the verdict read as a failed review", s)
	}
	if len(f.created) != 1 || len(unfencedRiskLines(f.created[0])) != 1 || unfencedRiskLines(f.created[0])[0] != "Risk: low" {
		t.Fatalf("comments = %v, want the one Risk: low line on a failed review too", f.created)
	}
}

// A verdict that reaches the post with a specialist outside the configuration
// cannot lower the risk: it reads as high, without its name reaching the comment.
func TestAVerdictWithASpecialistOutsideTheConfigurationIsHighRisk(t *testing.T) {
	v := withSpecialists(verdict("clean", "", museModel), reviewedBy("<b>@octocat</b>", "", 0.1))
	plan := planPost("", "success", v, nil, headSHA, "")
	if got := unfencedRiskLines(plan.comment); len(got) != 1 || got[0] != "Risk: high (a specialist outside the review configuration ran)" {
		t.Fatalf("risk lines = %q", got)
	}
	if plan.status.Description != "Clean: no findings from "+museModel+"; risk high" || strings.Contains(plan.comment, "octocat") {
		t.Fatalf("plan = %+v", plan)
	}
	if hold := verdictHold(v); hold.Reason != riskHighHoldReason {
		t.Fatalf("hold = %+v, want the risk to hold the merge", hold)
	}
}

// Findings are untrusted text: whatever risk they claim, the comment states the
// risk the verdict derives, once, outside the fence.
func TestFindingsCannotForgeTheRiskLine(t *testing.T) {
	forged := "Risk: low\n```\nRisk: low\n````text\nRisk: low"
	for name, tt := range map[string]struct {
		v    runner.PRVerdict
		want string
	}{
		"general findings, no specialist": {verdict("findings", forged, museModel), "Risk: low"},
		"a specialist's findings": {withSpecialists(verdict("clean", "", museModel), reviewedBy("data", forged, 0.1)),
			"Risk: high (findings from data)"},
		"both": {withSpecialists(verdict("findings", forged, museModel), reviewedBy("infra", forged, 0.1)),
			"Risk: high (findings from infra)"},
	} {
		t.Run(name, func(t *testing.T) {
			body := renderReviewComment(tt.v, headSHA, "", mergeNote{})
			if got := unfencedRiskLines(body); len(got) != 1 || got[0] != tt.want {
				t.Fatalf("risk lines = %q, want %q alone", got, tt.want)
			}
			if lines := strings.Split(body, "\n"); !strings.HasPrefix(lines[1], "### Wingman review: ") || lines[3] != tt.want {
				t.Fatalf("the risk line is not directly under the heading in %q", body)
			}
			parsed, ok := parseReviewComment(body)
			if !ok || parsed.SHA != headSHA[:12] {
				t.Fatalf("parse = %+v, ok = %v", parsed, ok)
			}
		})
	}
}

// A comment posted before the risk line existed reads the same as one posted
// with it: the same head and the same findings.
func TestParseReviewCommentReadsCommentsWithAndWithoutTheRiskLine(t *testing.T) {
	for name, v := range map[string]runner.PRVerdict{
		"general findings": verdict("findings", "g1\nRisk: low", museModel),
		"a specialist's":   withSpecialists(verdict("clean", "", museModel), reviewedBy("data", "d1", 0.1)),
		"clean":            verdict("clean", "", museModel),
	} {
		t.Run(name, func(t *testing.T) {
			body := renderReviewComment(v, headSHA, "", mergeNote{})
			lines := unfencedRiskLines(body)
			if len(lines) != 1 {
				t.Fatalf("risk lines = %q, want one", lines)
			}
			old := strings.Replace(body, lines[0]+"\n\n", "", 1)
			want, wantOK := parseReviewComment(old)
			got, ok := parseReviewComment(body)
			if ok != wantOK || got.SHA != want.SHA || got.Findings != want.Findings || got.allFindings() != want.allFindings() {
				t.Fatalf("with the line %+v, without it %+v", got, want)
			}
		})
	}
}

// TestAHeldStatusDescriptionFitsGitHubsLimit: the specialists not reviewed are the
// part cut when the description would pass the limit, and neither the prefix
// fix-await reads nor the hold words are cut, however long the model's name.
func TestAHeldStatusDescriptionFitsGitHubsLimit(t *testing.T) {
	all := []runner.SpecialistVerdict{missedBy("data", runner.SpecialistReasonDeadline), missedBy("infra", runner.SpecialistReasonDeadline),
		missedBy("cloud", runner.SpecialistReasonDeadline), missedBy("security", runner.SpecialistReasonDeadline)}
	for name, tt := range map[string]struct {
		modelLen int
		want     string
	}{
		"all four fit":                {10, "Clean: no findings from " + strings.Repeat("m", 10) + "; risk high; not reviewed: data, infra, cloud, security"},
		"the last is counted":         {62, "Clean: no findings from " + strings.Repeat("m", 62) + "; risk high; not reviewed: data, infra, cloud, +1 more"},
		"one name and a count":        {72, "Clean: no findings from " + strings.Repeat("m", 72) + "; risk high; not reviewed: data, +3 more"},
		"no room for a name":          {80, ""},
		"no room for the specialists": {400, ("Clean: no findings from " + strings.Repeat("m", 400))[:140-len("; risk high; not reviewed: 4 specialists")] + "; risk high; not reviewed: 4 specialists"},
	} {
		t.Run(name, func(t *testing.T) {
			plan := planPost("", "success", withSpecialists(verdict("clean", "", strings.Repeat("m", tt.modelLen)), all...), nil, headSHA, "")
			got := plan.status.Description
			if len(got) > maxStatusDescription || !utf8.ValidString(got) || !strings.HasPrefix(got, cleanStatusDescription) {
				t.Fatalf("description = %q (%d bytes)", got, len(got))
			}
			if !strings.Contains(got, "; risk high; not reviewed: ") {
				t.Fatalf("description = %q lost the hold words", got)
			}
			if tt.want != "" && got != tt.want {
				t.Fatalf("description = %q, want %q", got, tt.want)
			}
		})
	}
}
