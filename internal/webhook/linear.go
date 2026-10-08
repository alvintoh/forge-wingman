package webhook

import (
	"context"
	"fmt"

	"github.com/alvintoh/forge-wingman/internal/linear"
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
