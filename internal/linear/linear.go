// Package linear posts GraphQL operations to Linear's API.
package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

const (
	// defaultEndpoint is Linear's GraphQL API.
	defaultEndpoint = "https://api.linear.app/graphql"
	// defaultMaxReply bounds a reply when a Client sets no limit of its own.
	defaultMaxReply     = 4 << 20
	maxErrorBytes       = 512
	authenticationError = "AUTHENTICATION_ERROR"
)

// ErrUnauthenticated reports that Linear refused the token: an HTTP 401, or a
// GraphQL error whose extensions code is AUTHENTICATION_ERROR.
var ErrUnauthenticated = errors.New("linear: unauthenticated")

// Client calls Linear with one token, sent as a bearer token.
type Client struct {
	// Endpoint overrides the API address; empty means Linear's own.
	Endpoint string
	Token    string
	// HTTP is the client requests go through; nil means http.DefaultClient.
	HTTP *http.Client
	// MaxReply caps the reply in bytes; zero means 4 MiB.
	MaxReply int
}

// Do posts one operation and decodes its data into out. Linear reports a
// refused operation as errors beside the data with a 200 status, so any error
// fails the call even when data came with it.
func (c Client) Do(ctx context.Context, query string, variables, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return fmt.Errorf("encoding the Linear query: %w", err)
	}
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building the Linear request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("reading Linear: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	limit := c.MaxReply
	if limit <= 0 {
		limit = defaultMaxReply
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, int64(limit)+1))
	if err != nil {
		return fmt.Errorf("reading Linear's reply: %w", err)
	}
	if len(raw) > limit {
		return fmt.Errorf("the reply is over %d bytes", limit)
	}
	var reply struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message    string `json:"message"`
			Extensions struct {
				Code string `json:"code"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	decodeErr := json.Unmarshal(raw, &reply)
	unauthenticated := res.StatusCode == http.StatusUnauthorized
	for _, e := range reply.Errors {
		unauthenticated = unauthenticated || e.Extensions.Code == authenticationError
	}
	var failure error
	switch {
	case res.StatusCode != http.StatusOK:
		failure = fmt.Errorf("the Linear API answered %s: %s", res.Status, snippet(raw))
	case decodeErr != nil:
		return fmt.Errorf("decoding Linear's reply: %w", decodeErr)
	case len(reply.Errors) > 0:
		msgs := make([]string, 0, len(reply.Errors))
		for _, e := range reply.Errors {
			msgs = append(msgs, e.Message)
		}
		failure = fmt.Errorf("the Linear API refused the query: %s", strings.Join(msgs, "; "))
	}
	if failure != nil && unauthenticated {
		return fmt.Errorf("%w: %w", ErrUnauthenticated, failure)
	}
	if failure != nil {
		return failure
	}
	if len(reply.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(reply.Data, out); err != nil {
		return fmt.Errorf("decoding Linear's reply: %w", err)
	}
	return nil
}

// snippet is a refusal body cut to a log line, on a character boundary.
func snippet(raw []byte) string {
	s := strings.ToValidUTF8(strings.TrimSpace(string(raw)), "�")
	if len(s) > maxErrorBytes {
		cut := maxErrorBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", "")
}
