package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// The bounds a decision is held to. The plan stage's own re-check enforces
// them, since a decision travels out of the untrusted model job: one question
// per decision, at most decisionContextLines lines of context, and
// decisionOptionMin..decisionOptionMax options, each naming what the owner
// reads, the value a reply is matched against, and what the option costs. The
// plan prompt states these same limits, built from them, so a change here
// changes what the model is told.
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

// decisionBoundError reports a decision that broke the bounds: it names the
// first it broke and carries every one, so a report states the first rule while
// the plan stage's correction round can name them all. It unwraps to the first,
// so it reads as that rule wherever an invalid decision is reported.
type decisionBoundError struct{ breaches []error }

func (e decisionBoundError) Error() string { return e.breaches[0].Error() }
func (e decisionBoundError) Unwrap() error { return e.breaches[0] }

// breaksDecisionBound reports whether err is a decision that broke one of the
// bounds, which the plan stage answers with one correction round.
func breaksDecisionBound(err error) bool {
	var b decisionBoundError
	return errors.As(err, &b)
}

// decisionBreaches returns every bound err reports: all of them for a decision
// that broke several, and err alone for any other error.
func decisionBreaches(err error) []error {
	var b decisionBoundError
	if errors.As(err, &b) {
		return b.breaches
	}
	return []error{err}
}

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

// DecisionMode is how a run's plan decisions are settled.
type DecisionMode string

const (
	// DecisionGuided parks the run until the owner answers.
	DecisionGuided DecisionMode = "guided"
	// DecisionAutopilot takes the recommended option and lets the run continue.
	DecisionAutopilot DecisionMode = "autopilot"
)

// DefaultMaxAutopilotDecisions is how many answered decisions a run may hold
// before auto-pilot stops taking the recommendation and asks the owner.
const DefaultMaxAutopilotDecisions = 3

// TakesRecommendation reports whether a run in mode m, holding answered
// decisions already, settles its next decision with the recommended option.
func (m DecisionMode) TakesRecommendation(answered int) bool {
	return m == DecisionAutopilot && answered < DefaultMaxAutopilotDecisions
}

// ErrUnknownDecisionMode is a decision mode that is neither guided nor autopilot.
var ErrUnknownDecisionMode = errors.New("unknown decision mode")

// ParseDecisionMode reads a mode setting, taking guided for an empty one.
func ParseDecisionMode(raw string) (DecisionMode, error) {
	switch m := DecisionMode(strings.ToLower(strings.TrimSpace(raw))); m {
	case "", DecisionGuided:
		return DecisionGuided, nil
	case DecisionAutopilot:
		return m, nil
	}
	return "", fmt.Errorf("%w %q: want %s or %s", ErrUnknownDecisionMode, raw, DecisionGuided, DecisionAutopilot)
}

// Provisional is a decision an auto-pilot run settled with the recommended option.
type Provisional struct {
	Question  string
	Choice    string
	Cost      string
	Confirmed bool
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
// them longer than its limit or more than one line. It reports the first bound
// broken, and carries every one it found.
func (d Decision) Check() error {
	if breaches := d.boundBreaches(); len(breaches) > 0 {
		return decisionBoundError{breaches}
	}
	return nil
}

// boundBreaches reports every bound d breaks, in the order the fields are read,
// so one correction round can name them together.
func (d Decision) boundBreaches() []error {
	var breaches []error
	add := func(err error) {
		if err != nil {
			breaches = append(breaches, err)
		}
	}
	add(checkDecisionText("question", d.Question, decisionQuestionLimit))
	if len(d.Context) > decisionContextLines {
		add(fmt.Errorf("%w: context is over %d lines", errDecisionInvalid, decisionContextLines))
	}
	for i, line := range d.Context {
		add(checkDecisionText(fmt.Sprintf("context line %d", i+1), line, decisionContextLimit))
	}
	if len(d.Options) < decisionOptionMin || len(d.Options) > decisionOptionMax {
		add(fmt.Errorf("%w: %d options, want %d to %d", errDecisionInvalid, len(d.Options), decisionOptionMin, decisionOptionMax))
	}
	for i, o := range d.Options {
		add(checkDecisionText(fmt.Sprintf("option %d label", i+1), o.Label, decisionLabelLimit))
		add(checkDecisionText(fmt.Sprintf("option %d value", i+1), o.Value, decisionValueLimit))
		add(checkDecisionText(fmt.Sprintf("option %d cost", i+1), o.Cost, decisionCostLimit))
	}
	return breaches
}

// matchCollators folds case, width and diacritics when a reply is matched
// against an option, so a reply typed on a phone still names the option it
// means: strasse and straße both name STRASSE. A Collator carries scratch state
// for the comparison it is running, so one is borrowed per match rather than
// shared between goroutines.
var matchCollators = sync.Pool{New: func() any { return collate.New(language.Und, collate.Loose) }}

// Match returns the option a free-text reply names: the first whose value or
// label equals the reply, folded as above. A reply matching none is no match —
// an owner's words are never guessed at.
func (d Decision) Match(reply string) (DecisionOption, bool) {
	want := strings.TrimSpace(reply)
	if want == "" {
		return DecisionOption{}, false
	}
	c := matchCollators.Get().(*collate.Collator)
	defer matchCollators.Put(c)
	for _, o := range d.Options {
		if c.CompareString(want, strings.TrimSpace(o.Value)) == 0 ||
			c.CompareString(want, strings.TrimSpace(o.Label)) == 0 {
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
