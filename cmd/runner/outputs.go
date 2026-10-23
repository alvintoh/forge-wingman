package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// errNoOutput is a summary with nowhere to go: outside Actions nothing reports it.
var errNoOutput = errors.New("no GITHUB_OUTPUT to report the summary to")

func writeSummary(path string, s runner.Summary) error {
	if path == "" {
		return errNoOutput
	}
	v, err := s.Encode()
	if err != nil {
		return err
	}
	return writeOutputs(path, map[string]string{"summary": v})
}

// writeOutputs appends single-line step outputs to $GITHUB_OUTPUT, or does nothing
// outside Actions.
func writeOutputs(path string, kv map[string]string) error {
	for k, v := range kv {
		if strings.ContainsAny(k+v, "\r\n") {
			return fmt.Errorf("output %s spans lines", k)
		}
	}
	if path == "" {
		return nil
	}
	var out strings.Builder
	for k, v := range kv {
		fmt.Fprintf(&out, "%s=%s\n", k, v)
	}
	return appendFile(path, out.String())
}

// writeMultilineOutput appends one step output that may span lines to
// $GITHUB_OUTPUT, between random delimiters the value does not contain.
func writeMultilineOutput(path, key, value string) error {
	if path == "" {
		return nil
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	delim := "EOF_" + hex.EncodeToString(b)
	if strings.Contains(value, delim) {
		return fmt.Errorf("output %s contains its delimiter", key)
	}
	return appendFile(path, fmt.Sprintf("%s<<%s\n%s\n%s\n", key, delim, value, delim))
}

// appendFile appends content to the file at path, creating it owner-only if absent.
func appendFile(path, content string) (err error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if _, err := f.WriteString(content); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
