package webhook

import (
	"context"
	"fmt"
	"strings"

	"github.com/alvintoh/forge-wingman/internal/linear"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

const (
	acknowledgement  = "Received. Queuing this ticket for a run."
	activityMutation = `mutation Acknowledge($input: AgentActivityCreateInput!) { agentActivityCreate(input: $input) { success } }`
)

// Linear calls Linear's GraphQL API with the agent's token.
type Linear struct {
	Endpoint string
	Tokens   linear.TokenSource
}

// Acknowledge posts a thought activity on the session, which Linear needs
// within ten seconds of creating it or it shows the agent as unresponsive.
func (l Linear) Acknowledge(ctx context.Context, sessionID string) error {
	var data struct {
		AgentActivityCreate struct {
			Success bool `json:"success"`
		} `json:"agentActivityCreate"`
	}
	input := map[string]any{
		"agentSessionId": sessionID,
		"content":        map[string]string{"type": "thought", "body": acknowledgement},
	}
	err := linear.DoRefreshing(ctx, l.Tokens, func(token string) error {
		return linear.Client{Endpoint: l.Endpoint, Token: token}.Do(ctx, activityMutation, map[string]any{"input": input}, &data)
	})
	if err != nil {
		return fmt.Errorf("acknowledging session %s: %w", sessionID, err)
	}
	if !data.AgentActivityCreate.Success {
		return fmt.Errorf("acknowledging session %s: Linear reported no success", sessionID)
	}
	return nil
}

// Elicit asks the owner d's question in the session as one selection activity:
// the options in order, the recommended first, each carrying its cost. The
// owner may still answer in free text, which the dispatcher matches leniently.
func (l Linear) Elicit(ctx context.Context, sessionID string, d runner.Decision) error {
	var data struct {
		AgentActivityCreate struct {
			Success bool `json:"success"`
		} `json:"agentActivityCreate"`
	}
	options := make([]map[string]string, 0, len(d.Options))
	for _, o := range d.Options {
		options = append(options, map[string]string{"label": o.Label + " — " + o.Cost, "value": o.Value})
	}
	input := map[string]any{
		"agentSessionId": sessionID,
		"content": map[string]any{
			"type":           "elicitation",
			"body":           elicitationBody(d),
			"signal":         "select",
			"signalMetadata": map[string]any{"options": options},
		},
	}
	err := linear.DoRefreshing(ctx, l.Tokens, func(token string) error {
		return linear.Client{Endpoint: l.Endpoint, Token: token}.Do(ctx, activityMutation, map[string]any{"input": input}, &data)
	})
	if err != nil {
		return fmt.Errorf("asking session %s: %w", sessionID, err)
	}
	if !data.AgentActivityCreate.Success {
		return fmt.Errorf("asking session %s: Linear reported no success", sessionID)
	}
	return nil
}

// elicitationBody is the activity's text: the question, then its context lines.
func elicitationBody(d runner.Decision) string {
	if len(d.Context) == 0 {
		return d.Question
	}
	return d.Question + "\n\n" + strings.Join(d.Context, "\n")
}
