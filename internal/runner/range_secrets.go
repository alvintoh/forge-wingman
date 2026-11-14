package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
)

// ErrTokenShaped reports an object holding a string shaped like a credential.
var ErrTokenShaped = errors.New("branch contains a string shaped like a token")

// errRangeInput reports a scan asked of no repository or no range.
var errRangeInput = errors.New("scanning a range needs a repository and a range")

// tokenShapes is the closed list of credential shapes RangeSecrets looks for.
var tokenShapes = []struct {
	name string
	re   *regexp.Regexp
}{
	{"github-token", regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`)},
	{"github-pat", regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`)},
	{"api-key", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`)},
	{"aws-access-key", regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{"private-key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
}

// RangeSecrets fails when an object that rng adds in the repository at repo
// contains one of secrets (empty ones are skipped) or a string shaped like a
// token. The error names the shape that matched, never the value.
func RangeSecrets(ctx context.Context, repo, rng string, secrets []string) error {
	if repo == "" || rng == "" {
		return errRangeInput
	}
	bin, err := exec.LookPath("git")
	if err != nil {
		return fmt.Errorf("finding git: %w", err)
	}
	list, err := runGit(ctx, bin, repo, nil, "rev-list", "--objects", rng)
	if err != nil {
		return fmt.Errorf("listing %s: %s", rng, detail(err))
	}
	var ids bytes.Buffer
	for _, line := range bytes.Split(list, []byte{'\n'}) {
		if id, _, _ := bytes.Cut(line, []byte{' '}); len(id) > 0 {
			ids.Write(id)
			ids.WriteByte('\n')
		}
	}
	if ids.Len() == 0 {
		return nil
	}
	objects, err := runGit(ctx, bin, repo, &ids, "cat-file", "--batch")
	if err != nil {
		return fmt.Errorf("reading %s: %s", rng, detail(err))
	}
	for _, s := range secrets {
		if s != "" && bytes.Contains(objects, []byte(s)) {
			return ErrSecretInBranch
		}
	}
	for _, shape := range tokenShapes {
		if shape.re.Match(objects) {
			return fmt.Errorf("%w: %s", ErrTokenShaped, shape.name)
		}
	}
	return nil
}
