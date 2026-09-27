package dispatcher

import (
	"context"
	"fmt"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
)

// Secrets reads the API tokens the dispatcher polls with out of Secret Manager
// as each run starts, so a rotated token is the next run's business rather than
// a redeploy's, and no token is ever an environment variable or a command line.
type Secrets struct {
	project string
	client  *secretmanager.Client
}

// NewSecrets reads secrets held in project.
func NewSecrets(project string, client *secretmanager.Client) Secrets {
	return Secrets{project: project, client: client}
}

// Token returns the value of the named secret's latest version.
func (s Secrets) Token(ctx context.Context, name string) (string, error) {
	res, err := s.client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{
		Name: fmt.Sprintf("projects/%s/secrets/%s/versions/latest", s.project, name),
	})
	if err != nil {
		return "", fmt.Errorf("reading secret %s: %w", name, err)
	}
	return string(res.GetPayload().GetData()), nil
}
