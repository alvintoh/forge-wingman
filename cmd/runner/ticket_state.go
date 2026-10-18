package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/linear"
)

// ticketStateTimeout bounds the Linear write that puts a requeued ticket back,
// so a hung connection cannot hold the command open.
const ticketStateTimeout = 30 * time.Second

// restoreTicketState puts a ticket back to the state it held before its claim,
// as an operator requeue of its run does. The queue reports that state from the
// run's row; an empty one says the claim moved nothing, and there is nothing to
// put back.
//
// A write that fails is logged and never fails the command: the run is back in
// the queue either way, and its ticket shows In Progress until the claim that
// follows claims it again.
func restoreTicketState(ctx context.Context, logger *slog.Logger, project, ticketID, stateID string) {
	if stateID == "" || ticketID == "" {
		return
	}
	secretsClient, err := secretmanager.NewClient(ctx)
	if err != nil {
		logger.Warn("ticketStateNotRestored", "ticket", ticketID, "err", err.Error())
		return
	}
	defer func() { _ = secretsClient.Close() }()
	client := &http.Client{Timeout: ticketStateTimeout}
	creds, err := linear.ReadCredentials(ctx, linear.DispatchApp, dispatcher.NewSecrets(project, secretsClient).Token, client)
	if err != nil {
		logger.Warn("ticketStateNotRestored", "ticket", ticketID, "err", err.Error())
		return
	}
	tokens := linear.NewPollSource(creds)
	defer linear.RevokeAll(ctx, tokens, logger)
	if err := (dispatcher.Linear{Tokens: tokens, Client: client}).RestoreTicket(ctx, ticketID, stateID); err != nil {
		logger.Warn("ticketStateNotRestored", "ticket", ticketID, "err", err.Error())
	}
}
