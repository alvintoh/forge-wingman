package dispatcher

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// repositoryPattern is what GitHub calls a repository: an owner and a name.
var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

// ValidRepository reports whether repo is written as owner/name.
func ValidRepository(repo string) bool {
	return repositoryPattern.MatchString(repo)
}

// ParsePullURL reads the repository and number out of a pull request page URL.
func ParsePullURL(raw string) (repo string, number int, err error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" {
		return "", 0, fmt.Errorf("%q is not a github.com pull request URL", raw)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 4 || parts[2] != "pull" {
		return "", 0, fmt.Errorf("%q is not a github.com pull request URL", raw)
	}
	number, err = strconv.Atoi(parts[3])
	if err != nil || number <= 0 {
		return "", 0, fmt.Errorf("%q names no pull request number", raw)
	}
	repo = parts[0] + "/" + parts[1]
	if !ValidRepository(repo) || slices.Contains(parts[:2], ".") || slices.Contains(parts[:2], "..") {
		return "", 0, fmt.Errorf("%q names no valid repository", raw)
	}
	return repo, number, nil
}
