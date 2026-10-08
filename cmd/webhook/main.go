// Command webhook is the public Cloud Run service Linear delivers agent-session
// events to. A delegation marks the session, acknowledges it and wakes
// the dispatcher job; the poll that job runs does the admitting.
//
// Linear's signature is the only gate. With no signing secret the service
// still serves, refusing every delivery and re-reading the secret now and
// then, rather than failing to start and restarting forever.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"cloud.google.com/go/firestore"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"golang.org/x/oauth2/google"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/linear"
	"github.com/alvintoh/forge-wingman/internal/store"
	"github.com/alvintoh/forge-wingman/internal/webhook"
)

const (
	webhookSecret      = "linear-webhook-secret"
	linearClientID     = "linear-client-id"
	linearClientSecret = "linear-client-secret"
	cloudPlatform      = "https://www.googleapis.com/auth/cloud-platform"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, logger, os.Getenv)
	stop()
	if err != nil {
		logger.Error("webhookExited", "err", err)
		os.Exit(1)
	}
}

// config is what the service needs from its environment.
type config struct {
	project string
	runURI  string
	port    string
}

func loadConfig(getenv func(string) string) (config, error) {
	c := config{
		project: getenv("GOOGLE_CLOUD_PROJECT"),
		runURI:  getenv("DISPATCHER_RUN_URI"),
		port:    getenv("PORT"),
	}
	var missing []string
	for _, kv := range [][2]string{
		{"GOOGLE_CLOUD_PROJECT", c.project},
		{"DISPATCHER_RUN_URI", c.runURI},
	} {
		if kv[1] == "" {
			missing = append(missing, kv[0])
		}
	}
	if len(missing) > 0 {
		return config{}, fmt.Errorf("missing environment: %v", missing)
	}
	if c.port == "" {
		c.port = "8080"
	}
	return c, nil
}

// tokenReader reads a secret's latest value.
type tokenReader interface {
	Token(ctx context.Context, name string) (string, error)
}

// optionalSecret is the named secret's value, or empty when it has no enabled
// version yet. Any other failure is returned, so a transient one restarts the
// service instead of leaving it refusing every delivery.
func optionalSecret(ctx context.Context, secrets tokenReader, name string, logger *slog.Logger) (string, error) {
	value, err := secrets.Token(ctx, name)
	if err == nil {
		return value, nil
	}
	if code := status.Code(err); code == codes.NotFound || code == codes.FailedPrecondition {
		logger.Error("webhookSecretMissing", "secret", name, "err", err)
		return "", nil
	}
	return "", err
}

// run serves until the process is signalled.
func run(ctx context.Context, logger *slog.Logger, getenv func(string) string) error {
	c, err := loadConfig(getenv)
	if err != nil {
		return err
	}
	secretsClient, err := secretmanager.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("secret manager client: %w", err)
	}
	defer func() { _ = secretsClient.Close() }()
	secrets := dispatcher.NewSecrets(c.project, secretsClient)
	signing, err := optionalSecret(ctx, secrets, webhookSecret, logger)
	if err != nil {
		return err
	}
	clientID, err := optionalSecret(ctx, secrets, linearClientID, logger)
	if err != nil {
		return err
	}
	clientSecret, err := optionalSecret(ctx, secrets, linearClientSecret, logger)
	if err != nil {
		return err
	}
	fsc, err := firestore.NewClient(ctx, c.project)
	if err != nil {
		return fmt.Errorf("firestore client: %w", err)
	}
	defer func() { _ = fsc.Close() }()
	gcp, err := google.DefaultClient(ctx, cloudPlatform)
	if err != nil {
		return fmt.Errorf("google credentials: %w", err)
	}
	tokens := linear.NewCachedSource(linear.Credentials{
		ClientID:     strings.TrimSpace(clientID),
		ClientSecret: strings.TrimSpace(clientSecret),
	}, time.Now)
	defer linear.RevokeAll(ctx, tokens, logger)
	sessions := webhook.Linear{Tokens: tokens}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("POST /linear", webhook.Handler{
		Key: webhook.NewSecret(webhookSecret, signing, func(ctx context.Context) (string, error) {
			return secrets.Token(ctx, webhookSecret)
		}, time.Now, logger),
		Markers:  store.NewMarkers(fsc),
		Sessions: sessions,
		Poll:     webhook.Job{RunURI: c.runURI, Client: gcp},
		Logger:   logger,
		Now:      time.Now,
	})
	srv := &http.Server{
		Addr:              ":" + c.port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", srv.Addr)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
