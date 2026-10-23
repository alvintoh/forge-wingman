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
