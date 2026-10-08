package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type secretsOf struct {
	value string
	err   error
}

func (s secretsOf) Token(context.Context, string) (string, error) {
	return s.value, s.err
}

func TestOptionalSecret(t *testing.T) {
	for _, tc := range []struct {
		name    string
		secrets secretsOf
		want    string
		wantErr bool
	}{
		{name: "present", secrets: secretsOf{value: "whsec"}, want: "whsec"},
		{name: "no version", secrets: secretsOf{err: fmt.Errorf("reading secret: %w", status.Error(codes.NotFound, "no version"))}},
		{name: "disabled", secrets: secretsOf{err: status.Error(codes.FailedPrecondition, "disabled")}},
		{name: "unavailable", secrets: secretsOf{err: status.Error(codes.Unavailable, "try again")}, wantErr: true},
		{name: "not grpc", secrets: secretsOf{err: errors.New("dial")}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := optionalSecret(context.Background(), tc.secrets, "linear-webhook-secret", slog.New(slog.DiscardHandler))
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestLoadConfigNamesWhatIsMissing(t *testing.T) {
	_, err := loadConfig(func(string) string { return "" })
	if err == nil || err.Error() != "missing environment: [GOOGLE_CLOUD_PROJECT DISPATCHER_RUN_URI]" {
		t.Fatalf("err = %v", err)
	}
	c, err := loadConfig(func(k string) string {
		return map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "DISPATCHER_RUN_URI": "https://run"}[k]
	})
	if err != nil || c.port != "8080" {
		t.Fatalf("config = %+v, %v", c, err)
	}
}
