package webhook

import (
	"context"
	"errors"
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
	Token    *Secret
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
	if err := l.do(ctx, activityMutation, map[string]any{"input": input}, &data); err != nil {
		return fmt.Errorf("acknowledging session %s: %w", sessionID, err)
	}
	if !data.AgentActivityCreate.Success {
		return fmt.Errorf("acknowledging session %s: Linear reported no success", sessionID)
	}
	return nil
}

// do calls Linear once, and once more if Linear refused the token and the
// token held after a re-read differs: the token rotates while the service runs.
func (l Linear) do(ctx context.Context, query string, variables, out any) error {
	token := l.Token.Value()
	err := l.client(token).Do(ctx, query, variables, out)
	if !errors.Is(err, linear.ErrUnauthenticated) {
		return err
	}
	rotated := l.Token.Reload(ctx)
	if rotated == token {
		return err
	}
	return l.client(rotated).Do(ctx, query, variables, out)
}

func (l Linear) client(token string) linear.Client {
	return linear.Client{Endpoint: l.Endpoint, Token: token}
}
