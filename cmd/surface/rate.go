package main

import (
	"fmt"
	"math"
)

// rateNoAgentLines is what a rate over none of the agent's lines prints as.
const rateNoAgentLines = "no agent lines"

// linesRateText is rateText for a rate pooled over agentLines of the agent's:
// with none there is nothing measured, whatever the rate reads.
func linesRateText(agentLines int64, rate float64) string {
	if agentLines <= 0 {
		return rateNoAgentLines
	}
	return rateText(rate)
}

// rateText is the rewrite rate as the endpoint prints it: a percentage, or the
// words for a rate measured over none of the agent's lines at all, which is
// +Inf and has no percentage to print.
func rateText(rate float64) string {
	if math.IsInf(rate, 1) {
		return rateNoAgentLines
	}
	return fmt.Sprintf("%.1f%%", rate*100)
}

// linesRateNumber is linesRateText as the fraction a chart plots, null where the
// text is the words for no agent lines.
func linesRateNumber(agentLines int64, rate float64) *float64 {
	if agentLines <= 0 {
		return nil
	}
	return rateNumber(rate)
}

// rateNumber is rateText as the fraction a chart plots (0.582 for "58.2%"), null
// for the rate over none of the agent's lines.
func rateNumber(rate float64) *float64 {
	if math.IsInf(rate, 1) {
		return nil
	}
	return &rate
}
