package dispatcher

import (
	"context"
	"net"
	"strings"
	"testing"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// secretServer answers every access with the one payload, and remembers the
// resource name it was asked for.
type secretServer struct {
	secretmanagerpb.UnimplementedSecretManagerServiceServer
	payload string
	err     error
	asked   string
}

func (s *secretServer) AccessSecretVersion(_ context.Context, req *secretmanagerpb.AccessSecretVersionRequest) (*secretmanagerpb.AccessSecretVersionResponse, error) {
	s.asked = req.GetName()
	if s.err != nil {
		return nil, s.err
	}
	return &secretmanagerpb.AccessSecretVersionResponse{
		Name:    req.GetName(),
		Payload: &secretmanagerpb.SecretPayload{Data: []byte(s.payload)},
	}, nil
}

// serve answers the secret server over an in-memory connection, so the client
// under test is the same one a run builds from the environment.
func serve(t *testing.T, srv *secretServer) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	secretmanagerpb.RegisterSecretManagerServiceServer(server, srv)
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///secretmanager",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestSecretsTokenReadsTheLatestVersion(t *testing.T) {
	srv := &secretServer{payload: "linear-token-value"}
	ctx := context.Background()
	client, err := secretmanager.NewClient(ctx, option.WithGRPCConn(serve(t, srv)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	got, err := NewSecrets("forge-wingman", client).Token(ctx, "linear-token")
	if err != nil {
		t.Fatal(err)
	}
	if got != "linear-token-value" {
		t.Fatalf("Token = %q", got)
	}
	if want := "projects/forge-wingman/secrets/linear-token/versions/latest"; srv.asked != want {
		t.Fatalf("asked for %q, want %q", srv.asked, want)
	}
}

func TestSecretsTokenNamesTheSecretItCannotRead(t *testing.T) {
	srv := &secretServer{err: status.Error(codes.NotFound, "no such secret")}
	ctx := context.Background()
	client, err := secretmanager.NewClient(ctx, option.WithGRPCConn(serve(t, srv)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	_, err = NewSecrets("forge-wingman", client).Token(ctx, "github-token")
	if err == nil || !strings.Contains(err.Error(), "github-token") {
		t.Fatalf("err = %v, want it to name the secret", err)
	}
}
