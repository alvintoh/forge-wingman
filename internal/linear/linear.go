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
	// entityNotFoundMessage opens Linear's message for an id it cannot resolve,
	// as in "Entity not found: Issue".
	entityNotFoundMessage = "Entity not found"
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
			Path       []any  `json:"path"`
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
		r := refusal{data: reply.Data}
		for _, e := range reply.Errors {
			var field string
			if len(e.Path) > 0 {
				field, _ = e.Path[0].(string)
			}
			r.messages = append(r.messages, e.Message)
			r.fields = append(r.fields, field)
		}
		failure = r
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

// refusal is a query Linear answered with GraphQL errors: each error's message
// and the top-level field its path names ("" for none), and the data beside
// them.
type refusal struct {
	messages []string
	fields   []string
	data     json.RawMessage
}

// MissingEntities reports whether err is Linear refusing a query only because
// entities it names do not exist, as when one aliased field of a batched read
// names a deleted issue. It returns the top-level field each missing entity
// was asked under and the data Linear returned for the rest. Any other error,
// or an entity-not-found error whose path names no field, reports false.
func MissingEntities(err error) (fields []string, data json.RawMessage, ok bool) {
	var r refusal
	if !errors.As(err, &r) || errors.Is(err, ErrUnauthenticated) {
		return nil, nil, false
	}
	for i, m := range r.messages {
		if !strings.HasPrefix(m, entityNotFoundMessage) || r.fields[i] == "" {
			return nil, nil, false
		}
	}
	return r.fields, r.data, true
}

func (r refusal) Error() string {
	return "the Linear API refused the query: " + strings.Join(r.messages, "; ")
}

// entityNotFound reports whether err is Linear refusing a query because an
// entity it names does not exist, or is not visible to the caller.
func entityNotFound(err error) bool {
	var r refusal
	if !errors.As(err, &r) || errors.Is(err, ErrUnauthenticated) {
		return false
	}
	for _, m := range r.messages {
		if strings.HasPrefix(m, entityNotFoundMessage) {
			return true
		}
	}
	return false
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
