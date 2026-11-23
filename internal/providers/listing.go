package providers

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/alvintoh/forge-wingman/internal/money"
)

const (
	MinMonthlyPrice = 10 * money.Dollar
	MaxMonthlyPrice = 20 * money.Dollar
)

// Flag is something about a plan the owner should look at before relying on it.
type Flag string

const (
	FlagCanSpendPast     Flag = "can-spend-past"
	FlagOutsidePriceBand Flag = "outside-price-band"
)

// Flags lists what d warrants the owner's attention for.
func Flags(d Definition) []Flag {
	var flags []Flag
	if d.LimitBehaviour == LimitCanSpendPast {
		flags = append(flags, FlagCanSpendPast)
	}
	if d.MonthlyPrice < MinMonthlyPrice || d.MonthlyPrice > MaxMonthlyPrice {
		flags = append(flags, FlagOutsidePriceBand)
	}
	return flags
}

// HarnessLine is one plan-and-harness pair as the side-by-side view shows it:
// the harness, the model it serves, and the licence the harness's adapter
// records, empty for a harness this build does not register.
type HarnessLine struct {
	Harness string
	Model   string
	Licence string
}

// Listing is one plan as plan-list shows it. FactsDate is the date of the
// latest refreshed facts, empty when none have been read. Harnesses are the
// plan's pairs as the view lists them — open-source first.
type Listing struct {
	Provider  string
	Plan      Plan
	FactsDate string
	Harnesses []HarnessLine
	// BuildCache and PlanCache are each phase's own figures over the plan's
	// trailing runs: the share of its first request and of its whole input, or
	// the word that stands in for a share none of those runs recorded.
	BuildCache string
	PlanCache  string
}

// WriteTable writes the plans side by side, one row each.
func WriteTable(w io.Writer, rows []Listing) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "PROVIDER\tPRICE\tLIMIT\tBILLING\tOPTED-IN\tVERDICT\tFACTS\tFLAGS\tHARNESSES\tBUILD-CACHE\tPLAN-CACHE"); err != nil {
		return err
	}
	for _, r := range rows {
		verdict := string(r.Plan.Verdict)
		if verdict == "" {
			verdict = string(VerdictUnconfirmed)
		}
		var flags []string
		for _, f := range Flags(r.Plan.Definition) {
			flags = append(flags, string(f))
		}
		if _, err := fmt.Fprintf(tw, "%s\t$%.2f\t%s\t%s\t%t\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Provider, r.Plan.Definition.MonthlyPrice.USD(),
			r.Plan.Definition.LimitBehaviour, orDash(string(r.Plan.Definition.Billing)), r.Plan.OptedIn, verdict, orDash(r.FactsDate), orDash(strings.Join(flags, ",")), orDash(harnessCell(r.Harnesses)), orDash(r.BuildCache), orDash(r.PlanCache)); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// harnessCell renders a plan's pairs as the view shows them: each as
// harness:model, with the licence its adapter records in parentheses.
func harnessCell(lines []HarnessLine) string {
	cells := make([]string, 0, len(lines))
	for _, l := range lines {
		cell := l.Harness + ":" + l.Model
		if l.Licence != "" {
			cell += " (" + l.Licence + ")"
		}
		cells = append(cells, cell)
	}
	return strings.Join(cells, ",")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// RankingLines is how plan-list reports a plan's latest ranking against its
// escalation ladder. The ladder is one hand-picked list, so the ranking is read
// against it in two ways: rung 1, the cheapest ladder model, is compared with the
// top ranked model for each size, which is the model a claim would choose, and a
// line is printed only for a size where they differ; every other rung gets one
// line saying where that ladder model stands in each size's ranking, with no
// verdict. A size where no model cleared the bar is named once, and when no size
// has a ranked model one line says so. A plan with no ladder, no recorded ranking
// or a top ranked model equal to rung 1 for every size gets one line. The date is
// when the ranking was last read, so a ranking carried forward shows its week.
func RankingLines(provider string, ladder []string, ranking RankingFacts) []string {
	switch {
	case len(ladder) == 0:
		return []string{provider + ": no escalation ladder to compare"}
	case len(ranking.Sizes) == 0:
		return []string{provider + ": no ranking recorded"}
	}
	on := "ranking (ranked on " + ranking.Source.LastGoodReadOn
	if ranking.Source.State == ReadCouldNotRead {
		on += ", carried forward"
	}
	on += ")"
	var lines []string
	var empty []string
	for _, size := range ranking.Sizes {
		switch {
		case len(size.Ranked) == 0:
			empty = append(empty, size.Size)
			lines = append(lines, "  size "+size.Size+": no model cleared the bar")
		case size.Ranked[0].Model != ladder[0]:
			lines = append(lines, fmt.Sprintf("  size %s rung 1: configured %s, ranked top %s", size.Size, ladder[0], size.Ranked[0].Model))
		}
	}
	switch {
	case len(empty) == len(ranking.Sizes):
		return []string{provider + ": " + on + ": no model has cleared the bar yet"}
	case len(lines) == 0:
		return []string{provider + ": " + on + " matches the ladder at rung 1; rungs 2 and up are positions only"}
	}
	for i, model := range ladder[1:] {
		stands := make([]string, len(ranking.Sizes))
		for j, size := range ranking.Sizes {
			stands[j] = standing(size, model) + " for " + size.Size
		}
		lines = append(lines, fmt.Sprintf("  rung %d %s: %s", i+2, model, strings.Join(stands, ", ")))
	}
	return append([]string{provider + ": " + on + " differs from the ladder"}, lines...)
}

// standing is where model stands in one size's ranking: its place among the
// models that cleared the bar, below the bar, or not ranked at all.
func standing(size SizeRanking, model string) string {
	if i := slices.IndexFunc(size.Ranked, func(m RankedModel) bool { return m.Model == model }); i >= 0 {
		return "ranked " + ordinal(i+1)
	}
	if slices.ContainsFunc(size.BelowBar, func(m RankedModel) bool { return m.Model == model }) {
		return "below the bar"
	}
	return "not ranked"
}

// ordinal writes n as 1st, 2nd, 3rd, 4th, 11th.
func ordinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return strconv.Itoa(n) + suffix
}
