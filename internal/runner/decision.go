package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The bounds a decision is held to. The plan stage's own re-check enforces
// them, since a decision travels out of the untrusted model job: one question
// per decision, at most decisionContextLines lines of context, and
// decisionOptionMin..decisionOptionMax options, each naming what the owner
// reads, the value a reply is matched against, and what the option costs.
const (
	decisionQuestionLimit = 300
	decisionContextLimit  = 200
	decisionLabelLimit    = 120
	decisionValueLimit    = 80
	decisionCostLimit     = 80
	decisionContextLines  = 3
	decisionOptionMin     = 2
	decisionOptionMax     = 4
)

// errDecisionInvalid reports a decision that breaks a bound the plan stage's
// re-check enforces.
var errDecisionInvalid = errors.New("the plan's decision is not usable")

// Decision is a question a guided plan raises for the owner before the run
// continues: the question, a few lines of context, and the options the owner
// may choose between. The first option is the recommendation.
type Decision struct {
	Question string           `json:"question" firestore:"question"`
	Context  []string         `json:"context,omitempty" firestore:"context,omitempty"`
	Options  []DecisionOption `json:"options" firestore:"options"`
}

// DecisionOption is one choice a decision offers: the label the owner reads,
// the value a free-text reply is matched against, and what the choice costs.
type DecisionOption struct {
	Label string `json:"label" firestore:"label"`
	Value string `json:"value" firestore:"value"`
	Cost  string `json:"cost" firestore:"cost"`
}

// Answer is a decision the owner settled and the option they chose, carried to
// the resumed plan's prompt.
type Answer struct {
	Question string `json:"question" firestore:"question"`
	Choice   string `json:"choice" firestore:"choice"`
}

// Check reports whether d is a usable decision: one non-empty question, at most
// decisionContextLines lines of context, and decisionOptionMin to
// decisionOptionMax options, each with a label, a value and a cost, none of
// them longer than its limit or more than one line. The model job is untrusted,
// so the trusted record job re-checks a decision here before it is posted.
func (d Decision) Check() error {
	if err := checkDecisionText("question", d.Question, decisionQuestionLimit); err != nil {
		return err
	}
	if len(d.Context) > decisionContextLines {
		return fmt.Errorf("%w: context is over %d lines", errDecisionInvalid, decisionContextLines)
	}
	for i, line := range d.Context {
		if err := checkDecisionText(fmt.Sprintf("context line %d", i+1), line, decisionContextLimit); err != nil {
			return err
		}
	}
	if len(d.Options) < decisionOptionMin || len(d.Options) > decisionOptionMax {
		return fmt.Errorf("%w: %d options, want %d to %d", errDecisionInvalid, len(d.Options), decisionOptionMin, decisionOptionMax)
	}
	for i, o := range d.Options {
		if err := checkDecisionText(fmt.Sprintf("option %d label", i+1), o.Label, decisionLabelLimit); err != nil {
			return err
		}
		if err := checkDecisionText(fmt.Sprintf("option %d value", i+1), o.Value, decisionValueLimit); err != nil {
			return err
		}
		if err := checkDecisionText(fmt.Sprintf("option %d cost", i+1), o.Cost, decisionCostLimit); err != nil {
			return err
		}
	}
	return nil
}

// Match returns the option a free-text reply names: the first whose value or
// label equals the reply, trimmed and case-folded. A reply matching none is no
// match — an owner's words are never guessed at.
func (d Decision) Match(reply string) (DecisionOption, bool) {
	want := strings.ToLower(strings.TrimSpace(reply))
	if want == "" {
		return DecisionOption{}, false
	}
	for _, o := range d.Options {
		if strings.ToLower(strings.TrimSpace(o.Value)) == want ||
			strings.ToLower(strings.TrimSpace(o.Label)) == want {
			return o, true
		}
	}
	return DecisionOption{}, false
}

// checkDecisionText reports s unusable when it is blank, over limit bytes, or
// not one printable line — the same shape every free-text field of a decision
// is held to, so one over-long question cannot smuggle a second one past the
// re-check.
func checkDecisionText(name, s string, limit int) error {
	switch {
	case strings.TrimSpace(s) == "":
		return fmt.Errorf("%w: %s is empty", errDecisionInvalid, name)
	case len(s) > limit:
		return fmt.Errorf("%w: %s is over %d bytes", errDecisionInvalid, name, limit)
	case strings.ContainsAny(s, "\r\n") || !printable(s):
		return fmt.Errorf("%w: %s is not one printable line", errDecisionInvalid, name)
	}
	return nil
}

// checkAnswer reports a settled decision unusable by the same bounds its
// question and chosen option were held to when they were asked.
func checkAnswer(a Answer) error {
	if err := checkDecisionText("answer question", a.Question, decisionQuestionLimit); err != nil {
		return err
	}
	return checkDecisionText("answer choice", a.Choice, decisionLabelLimit)
}

// parseDecision decodes a plan-decision block's JSON content and checks it.
func parseDecision(raw string) (*Decision, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var d Decision
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("%w: decoding: %w", errDecisionInvalid, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("%w: trailing data after the decision", errDecisionInvalid)
	}
	if err := d.Check(); err != nil {
		return nil, err
	}
	return &d, nil
}
