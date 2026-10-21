package dispatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

const (
	githubEndpoint = "https://api.github.com"
	// GitHubAPIVersion is the REST version the workflow-dispatch documentation
	// is written against; GitHub ignores the header's version rather than the
	// shape of the request.
	GitHubAPIVersion = "2026-03-10"
	// maxErrorBytes caps how much of a refusal is kept for the log, since a
	// failure is reported by its message and not by the rest of the body.
	maxErrorBytes = 512
	// maxLinearResponseBytes bounds one Linear page: a page of a hundred
	// oversized descriptions is a ticket the query should not have asked for.
	maxLinearResponseBytes = 4 << 20
	// maxRepoResponseBytes bounds a "get a repository" reply: the field this
	// product reads from it is one boolean.
	maxRepoResponseBytes = 64 << 10
	// pullsPerPage is how many open PRs one listing page reads, and maxPullsPages
	// how many pages a count walks.
	pullsPerPage  = 100
	maxPullsPages = 5
	// maxPullsResponseBytes bounds one page of the open-PR listing.
	maxPullsResponseBytes = 4 << 20
	// closedPRsPerPage is how many closed pull requests one rewrite-rate read
	// lists, and how many commits of one pull request it reads.
	closedPRsPerPage = 100
	// maxListPages bounds how many pages a closed-PR or commit listing is
	// followed for; a listing cut there is reported rather than undercounted.
	maxListPages = 10
	// checkRunsPerPage is how many check runs one read of a commit holds.
	checkRunsPerPage = 100
)

// greenConclusions are the check run conclusions that do not hold a merge, as
// the REST API spells them; GraphQL's uppercase is lowered to match.
var greenConclusions = []string{"success", "neutral", "skipped"}

// GitHub starts the run workflow in one repository.
type GitHub struct {
	Endpoint string
	// Token needs actions: write on every repository the allowlist names.
	Token string
	// Workflow is the run workflow's file name, which the dispatch endpoint
	// accepts in place of its numeric id.
	Workflow string
	// Branch is the ref the workflow is dispatched from: the runner's
	// Workload Identity Federation trusts main only, and run.yml's jobs are
	// gated on it.
	Branch string
	Client *http.Client
}

// Dispatch starts the run workflow for the claimed run in the repository the
// ticket's repo: label was allowlisted to. Any 2xx is a dispatch: GitHub
// documented 204 for this endpoint when it was added and answers 200 with the
// run's URLs now, and both mean the run was started.
//
// The attempt input carries the attempt the claim booked under, so the run
// settles that reservation and no later one; the workflow must declare the
// input before this sends it, or GitHub answers 422.
//
// A claim of the plan stage dispatches with stage=plan, which gates the
// workflows' jobs onto the plan; every other claim dispatches no stage, which
// the workflows' own default reads as build.
func (g GitHub) Dispatch(ctx context.Context, c Claim) error {
	inputs := map[string]string{"run_id": c.RunID, "attempt": strconv.Itoa(c.Attempt)}
	if c.Stage == StagePlan {
		inputs["stage"] = StagePlan
	}
	body, err := json.Marshal(map[string]any{
		"ref":    g.Branch,
		"inputs": inputs,
	})
	if err != nil {
		return fmt.Errorf("encoding the dispatch request: %w", err)
	}
	endpoint := fmt.Sprintf("%s/repos/%s/actions/workflows/%s/dispatches",
		g.endpoint(), c.Repo, url.PathEscape(g.Workflow))
	action := fmt.Sprintf("dispatch run %s in %s", c.RunID, c.Repo)
	res, err := g.do(ctx, http.MethodPost, endpoint, bytes.NewReader(body), action)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	return nil
}

// Private reports whether repo is a private repository, read live from
// GitHub's own API (FR-22) rather than a hand-maintained list, which goes
// stale the moment a repository's visibility changes.
func (g GitHub) Private(ctx context.Context, repo string) (bool, error) {
	endpoint := fmt.Sprintf("%s/repos/%s", g.endpoint(), repo)
	res, err := g.do(ctx, http.MethodGet, endpoint, nil, fmt.Sprintf("read repository %s", repo))
	if err != nil {
		return false, err
	}
	defer func() { _ = res.Body.Close() }()
	var body struct {
		Private bool `json:"private"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxRepoResponseBytes)).Decode(&body); err != nil {
		return false, fmt.Errorf("decoding repository %s: %w", repo, err)
	}
	return body.Private, nil
}

// OpenAgentPRs counts the open pull requests in repo whose head branch is one
// of the runner's own. It needs pull_requests: read on every repository the
// allowlist names, and fails rather than report a count that may be short when
// the open PRs fill maxPullsPages pages.
func (g GitHub) OpenAgentPRs(ctx context.Context, repo string) (int, error) {
	n := 0
	for page := 1; page <= maxPullsPages; page++ {
		endpoint := fmt.Sprintf("%s/repos/%s/pulls?state=open&per_page=%d&page=%d", g.endpoint(), repo, pullsPerPage, page)
		heads, err := g.openPullHeads(ctx, endpoint, repo)
		if err != nil {
			return 0, err
		}
		for _, ref := range heads {
			if strings.HasPrefix(ref, runner.BranchPrefix) {
				n++
			}
		}
		if len(heads) < pullsPerPage {
			return n, nil
		}
	}
	return 0, fmt.Errorf("%s may hold more than %d open pull requests", repo, maxPullsPages*pullsPerPage)
}

// PullOpen reports whether pull request number in repo is open.
func (g GitHub) PullOpen(ctx context.Context, repo string, number int) (bool, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/pulls/%d", g.endpoint(), repo, number)
	res, err := g.do(ctx, http.MethodGet, endpoint, nil, fmt.Sprintf("read pull request %d in %s", number, repo))
	if err != nil {
		return false, err
	}
	defer func() { _ = res.Body.Close() }()
	var body struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxRepoResponseBytes)).Decode(&body); err != nil {
		return false, fmt.Errorf("decoding pull request %d of %s: %w", number, repo, err)
	}
	return body.State == "open", nil
}

// openPullHeads is the head branch of every PR on one page of the listing.
func (g GitHub) openPullHeads(ctx context.Context, endpoint, repo string) ([]string, error) {
	res, err := g.do(ctx, http.MethodGet, endpoint, nil, fmt.Sprintf("list open pull requests in %s", repo))
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	var pulls []struct {
		Head struct {
			Ref string `json:"ref"`
		} `json:"head"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxPullsResponseBytes)).Decode(&pulls); err != nil {
		return nil, fmt.Errorf("decoding the open pull requests of %s: %w", repo, err)
	}
	heads := make([]string, len(pulls))
	for i, p := range pulls {
		heads[i] = p.Head.Ref
	}
	return heads, nil
}

// ErrClosedTruncated is returned with the PRs read when the closed listing runs
// past the page cap, so the caller can tell a window that may hide merges.
var ErrClosedTruncated = errors.New("the closed pull request listing was cut at the page cap")

// Closed returns repo's closed pull requests, most recently updated first, up to
// maxListPages pages: the window a merged PR is first measured in and a revert is
// seen in, both read from the one listing. A listing cut at the cap returns the
// PRs read with ErrClosedTruncated.
func (g GitHub) Closed(ctx context.Context, repo string) ([]ClosedPR, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/pulls?state=closed&sort=updated&direction=desc&per_page=%d",
		g.endpoint(), repo, closedPRsPerPage)
	var out []ClosedPR
	capped, err := g.paged(ctx, endpoint, fmt.Sprintf("list closed pull requests in %s", repo), func(body io.Reader) error {
		page, err := decodeClosedPRs(body, repo)
		out = append(out, page...)
		return err
	})
	if err != nil {
		return nil, err
	}
	if capped {
		return out, ErrClosedTruncated
	}
	return out, nil
}

// paged reads endpoint and every page its Link header names next, up to
// maxListPages, handing each body to each. It reports whether the cap cut the
// listing off. A next link to another host is not followed, so the token goes
// only where it was meant to.
func (g GitHub) paged(ctx context.Context, endpoint, action string, each func(io.Reader) error) (capped bool, err error) {
	for page := 1; endpoint != ""; page++ {
		if page > maxListPages {
			return true, nil
		}
		res, err := g.do(ctx, http.MethodGet, endpoint, nil, action)
		if err != nil {
			return false, err
		}
		next := nextLink(res.Header.Get("Link"))
		err = each(res.Body)
		_ = res.Body.Close()
		if err != nil {
			return false, err
		}
		if next != "" && !strings.HasPrefix(next, g.endpoint()+"/") {
			return false, fmt.Errorf("%s: the next page is on another host", action)
		}
		endpoint = next
	}
	return false, nil
}

// nextLink is the rel="next" target of a Link header, or "".
func nextLink(header string) string {
	for _, part := range strings.Split(header, ",") {
		target, rel, ok := strings.Cut(part, ";")
		if !ok || !strings.Contains(rel, `rel="next"`) {
			continue
		}
		return strings.Trim(strings.TrimSpace(target), "<>")
	}
	return ""
}

// Pull returns one pull request of repo, whatever its state.
func (g GitHub) Pull(ctx context.Context, repo string, number int) (ClosedPR, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/pulls/%d", g.endpoint(), repo, number)
	res, err := g.do(ctx, http.MethodGet, endpoint, nil, fmt.Sprintf("read pull request %s#%d", repo, number))
	if err != nil {
		return ClosedPR{}, err
	}
	defer func() { _ = res.Body.Close() }()
	pulls, err := decodeClosedPRs(res.Body, repo)
	if err != nil {
		return ClosedPR{}, err
	}
	if len(pulls) == 0 {
		return ClosedPR{}, fmt.Errorf("GitHub answered no pull request for %s#%d", repo, number)
	}
	return pulls[0], nil
}

// PullStatus is where one pull request stands: whether it is open, a draft or
// merged, and the commit its head is at.
type PullStatus struct {
	Open    bool
	Draft   bool
	Merged  bool
	HeadSHA string
}

// PullState reads where one pull request of repo stands.
func (g GitHub) PullState(ctx context.Context, repo string, number int) (PullStatus, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/pulls/%d", g.endpoint(), repo, number)
	res, err := g.do(ctx, http.MethodGet, endpoint, nil, fmt.Sprintf("read pull request %s#%d", repo, number))
	if err != nil {
		return PullStatus{}, err
	}
	defer func() { _ = res.Body.Close() }()
	var body struct {
		State  string `json:"state"`
		Draft  bool   `json:"draft"`
		Merged bool   `json:"merged"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxPullsResponseBytes)).Decode(&body); err != nil {
		return PullStatus{}, fmt.Errorf("decoding pull request %s#%d: %w", repo, number, err)
	}
	return PullStatus{Open: body.State == "open", Draft: body.Draft, Merged: body.Merged, HeadSHA: body.Head.SHA}, nil
}

// ChecksGreen reports whether commit sha of repo passes everything that gates
// a merge: every check run completed green, and the combined commit status,
// when the commit has any status, is success.
// A commit with no check run is not green.
func (g GitHub) ChecksGreen(ctx context.Context, repo, sha string) (bool, error) {
	runsGreen, err := g.checkRunsGreen(ctx, repo, sha)
	if err != nil || !runsGreen {
		return false, err
	}
	return g.statusGreen(ctx, repo, sha)
}

// checkRunsGreen reads the commit's check runs. More runs than one page holds
// is an error rather than a guess.
func (g GitHub) checkRunsGreen(ctx context.Context, repo, sha string) (bool, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/commits/%s/check-runs?per_page=%d", g.endpoint(), repo, url.PathEscape(sha), checkRunsPerPage)
	res, err := g.do(ctx, http.MethodGet, endpoint, nil, fmt.Sprintf("read the check runs of %s@%s", repo, sha))
	if err != nil {
		return false, err
	}
	defer func() { _ = res.Body.Close() }()
	var body struct {
		Total int `json:"total_count"`
		Runs  []struct {
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxPullsResponseBytes)).Decode(&body); err != nil {
		return false, fmt.Errorf("decoding the check runs of %s@%s: %w", repo, sha, err)
	}
	if body.Total > len(body.Runs) {
		return false, fmt.Errorf("%d check runs on %s@%s exceed the %d one read holds", body.Total, repo, sha, checkRunsPerPage)
	}
	if len(body.Runs) == 0 {
		return false, nil
	}
	for _, run := range body.Runs {
		if !slices.Contains(greenConclusions, run.Conclusion) {
			return false, nil
		}
	}
	return true, nil
}

// statusGreen reads the commit's combined status, which holds the legacy
// statuses that check runs do not include. A commit with none is green.
func (g GitHub) statusGreen(ctx context.Context, repo, sha string) (bool, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/commits/%s/status", g.endpoint(), repo, url.PathEscape(sha))
	res, err := g.do(ctx, http.MethodGet, endpoint, nil, fmt.Sprintf("read the status of %s@%s", repo, sha))
	if err != nil {
		return false, err
	}
	defer func() { _ = res.Body.Close() }()
	var body struct {
		State string `json:"state"`
		Total int    `json:"total_count"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxPullsResponseBytes)).Decode(&body); err != nil {
		return false, fmt.Errorf("decoding the status of %s@%s: %w", repo, sha, err)
	}
	return body.Total == 0 || body.State == "success", nil
}

// Merge squash-merges pull request number of repo if its head is still at sha,
// and returns the commit the merge made. A merge GitHub will not make comes back
// as a *APIError.
func (g GitHub) Merge(ctx context.Context, repo string, number int, sha string) (string, error) {
	body, err := json.Marshal(map[string]string{"merge_method": "squash", "sha": sha})
	if err != nil {
		return "", fmt.Errorf("encoding the merge request: %w", err)
	}
	endpoint := fmt.Sprintf("%s/repos/%s/pulls/%d/merge", g.endpoint(), repo, number)
	res, err := g.do(ctx, http.MethodPut, endpoint, bytes.NewReader(body), fmt.Sprintf("merge pull request %s#%d", repo, number))
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()
	var merged struct {
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxRepoResponseBytes)).Decode(&merged); err != nil {
		return "", fmt.Errorf("decoding the merge of %s#%d: %w", repo, number, err)
	}
	return merged.SHA, nil
}

// PullRef names one pull request of a repository.
type PullRef struct {
	Repo   string
	Number int
}

// Check states of a pull request's head commit.
const (
	CheckGreen   = "green"
	CheckFailing = "failing"
	CheckPending = "pending"
)

// PullSummary is what a list of pull requests shows of one: its title, whether
// it is a draft, the commit its head is at, and how that commit's checks stand.
type PullSummary struct {
	Title   string
	Draft   bool
	HeadSHA string
	// Checks is one of CheckGreen, CheckFailing and CheckPending; a head with
	// no check yet is pending.
	Checks string
	// ChecksSummary names the checks in that state, like "go and web".
	ChecksSummary string
}

const (
	// summaryContexts is how many checks of a head commit one read lists.
	summaryContexts = 50
	// summaryNames is how many check names a summary spells out.
	summaryNames = 4
	// MaxSummaryRefs bounds one batched read.
	MaxSummaryRefs = 100
)

// pullSummaryFields is read from every pull request of a batched read.
var pullSummaryFields = fmt.Sprintf(`title isDraft headRefOid
commits(last: 1) { nodes { commit { statusCheckRollup { state contexts(first: %d) { nodes {
__typename
... on CheckRun { name status conclusion }
... on StatusContext { context state }
} } } } } }`, summaryContexts)

// PullSummaries reads title, draft, head and check state of every pull request
// in refs with ONE GraphQL request. A pull request GitHub does not return is
// absent from the result. It is a read for display: a merge reads the pull
// request again.
func (g GitHub) PullSummaries(ctx context.Context, refs []PullRef) (map[PullRef]PullSummary, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if len(refs) > MaxSummaryRefs {
		return nil, fmt.Errorf("%d pull requests exceed the %d one read holds", len(refs), MaxSummaryRefs)
	}
	var decl, selection strings.Builder
	vars := make(map[string]any, 3*len(refs))
	for i, ref := range refs {
		owner, name, ok := strings.Cut(ref.Repo, "/")
		if !ok {
			return nil, fmt.Errorf("%q is not an owner/name repository", ref.Repo)
		}
		fmt.Fprintf(&decl, "$o%[1]d: String!, $n%[1]d: String!, $p%[1]d: Int!, ", i)
		fmt.Fprintf(&selection, "r%[1]d: repository(owner: $o%[1]d, name: $n%[1]d) { pullRequest(number: $p%[1]d) { %[2]s } }\n", i, pullSummaryFields)
		vars[fmt.Sprintf("o%d", i)], vars[fmt.Sprintf("n%d", i)], vars[fmt.Sprintf("p%d", i)] = owner, name, ref.Number
	}
	payload, err := json.Marshal(map[string]any{"query": fmt.Sprintf("query(%s) {\n%s}", strings.TrimSuffix(decl.String(), ", "), selection.String()), "variables": vars})
	if err != nil {
		return nil, fmt.Errorf("encoding the pull request read: %w", err)
	}
	res, err := g.do(ctx, http.MethodPost, g.endpoint()+"/graphql", bytes.NewReader(payload), "read the pull requests")
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	var body struct {
		Data map[string]*struct {
			PullRequest *struct {
				Title      string `json:"title"`
				IsDraft    bool   `json:"isDraft"`
				HeadRefOid string `json:"headRefOid"`
				Commits    struct {
					Nodes []struct {
						Commit struct {
							Rollup *struct {
								State    string `json:"state"`
								Contexts struct {
									Nodes []summaryContext `json:"nodes"`
								} `json:"contexts"`
							} `json:"statusCheckRollup"`
						} `json:"commit"`
					} `json:"nodes"`
				} `json:"commits"`
			} `json:"pullRequest"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxPullsResponseBytes)).Decode(&body); err != nil {
		return nil, fmt.Errorf("decoding the pull requests: %w", err)
	}
	if len(body.Data) == 0 && len(body.Errors) > 0 {
		return nil, fmt.Errorf("GitHub refused to read the pull requests: %s", body.Errors[0].Message)
	}
	out := make(map[PullRef]PullSummary, len(refs))
	for i, ref := range refs {
		repo := body.Data[fmt.Sprintf("r%d", i)]
		if repo == nil || repo.PullRequest == nil {
			continue
		}
		pr := repo.PullRequest
		sum := PullSummary{Title: pr.Title, Draft: pr.IsDraft, HeadSHA: pr.HeadRefOid, Checks: CheckPending, ChecksSummary: "no checks yet"}
		if len(pr.Commits.Nodes) > 0 && pr.Commits.Nodes[0].Commit.Rollup != nil {
			rollup := pr.Commits.Nodes[0].Commit.Rollup
			sum.Checks = rollupChecks(rollup.State)
			sum.ChecksSummary = namesInState(rollup.Contexts.Nodes, sum.Checks)
		}
		out[ref] = sum
	}
	return out, nil
}

// summaryContext is one check run or commit status of a head commit.
type summaryContext struct {
	Type       string `json:"__typename"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Context    string `json:"context"`
	State      string `json:"state"`
}

// state is the context's own state, in the CheckGreen, CheckFailing and
// CheckPending words.
func (c summaryContext) state() string {
	if c.Type == "StatusContext" {
		return rollupChecks(c.State)
	}
	if c.Status != "COMPLETED" {
		return CheckPending
	}
	if slices.Contains(greenConclusions, strings.ToLower(c.Conclusion)) {
		return CheckGreen
	}
	return CheckFailing
}

func (c summaryContext) name() string {
	if c.Type == "StatusContext" {
		return c.Context
	}
	return c.Name
}

// rollupChecks maps GitHub's StatusState onto the three check states; a state
// it does not know is not green.
func rollupChecks(state string) string {
	switch state {
	case "SUCCESS":
		return CheckGreen
	case "PENDING", "EXPECTED":
		return CheckPending
	}
	return CheckFailing
}

// namesInState spells out the checks in state, like "go and web".
func namesInState(contexts []summaryContext, state string) string {
	var names []string
	for _, c := range contexts {
		if n := c.name(); c.state() == state && n != "" && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	more := 0
	if len(names) > summaryNames {
		more = len(names) - summaryNames
		names = names[:summaryNames]
	}
	switch {
	case len(names) == 0:
		return ""
	case more > 0:
		return fmt.Sprintf("%s and %d more", strings.Join(names, ", "), more)
	case len(names) == 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// closedPR is one entry of a pull request listing or read, as the rewrite rate
// needs it.
type closedPR struct {
	Number   int        `json:"number"`
	Title    string     `json:"title"`
	Body     string     `json:"body"`
	URL      string     `json:"html_url"`
	MergedAt *time.Time `json:"merged_at"`
	Head     struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

// decodeClosedPRs reads a pull request reply — a listing, or one pull request
// answered on its own — into the fields the rewrite rate is measured from. A
// reply that is neither is an error rather than a zero-valued pull request, so a
// shape GitHub changed is seen rather than measured over.
func decodeClosedPRs(body io.Reader, repo string) ([]ClosedPR, error) {
	raw, err := io.ReadAll(io.LimitReader(body, maxPullsResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("reading the pull requests of %s: %w", repo, err)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var pulls []closedPR
	switch trimmed[0] {
	case '[':
		if err := json.Unmarshal(trimmed, &pulls); err != nil {
			return nil, fmt.Errorf("decoding the pull requests of %s: %w", repo, err)
		}
	case '{':
		var one closedPR
		if err := json.Unmarshal(trimmed, &one); err != nil {
			return nil, fmt.Errorf("decoding the pull request of %s: %w", repo, err)
		}
		pulls = []closedPR{one}
	default:
		return nil, fmt.Errorf("decoding the pull requests of %s: the reply is neither a listing nor a pull request", repo)
	}
	out := make([]ClosedPR, 0, len(pulls))
	for _, p := range pulls {
		pr := ClosedPR{Number: p.Number, Title: p.Title, Body: p.Body, Head: p.Head.Ref, URL: p.URL}
		if p.MergedAt != nil {
			pr.MergedAt = *p.MergedAt
		}
		out = append(out, pr)
	}
	return out, nil
}

// Commits returns the commits of pull request number, each with who GitHub
// resolves its author to and how many lines it changed. The listing carries no
// line counts, so each commit is read once. A listing cut at the page cap ends in
// an unmeasured commit, so a window holding it measures nothing for the
// auto-merge rule.
func (g GitHub) Commits(ctx context.Context, repo string, number int) ([]Commit, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/pulls/%d/commits?per_page=%d",
		g.endpoint(), repo, number, closedPRsPerPage)
	var out []Commit
	capped, err := g.paged(ctx, endpoint, fmt.Sprintf("list the commits of %s#%d", repo, number), func(body io.Reader) error {
		var commits []struct {
			SHA    string `json:"sha"`
			Author *struct {
				Login string `json:"login"`
			} `json:"author"`
			Commit struct {
				Message string `json:"message"`
				Author  struct {
					Name  string `json:"name"`
					Email string `json:"email"`
				} `json:"author"`
			} `json:"commit"`
		}
		if err := json.NewDecoder(io.LimitReader(body, maxPullsResponseBytes)).Decode(&commits); err != nil {
			return fmt.Errorf("decoding the commits of %s#%d: %w", repo, number, err)
		}
		for _, c := range commits {
			added, removed, measured, err := g.commitLines(ctx, repo, c.SHA)
			if err != nil {
				return err
			}
			commit := Commit{
				Name:       c.Commit.Author.Name,
				Email:      c.Commit.Author.Email,
				Message:    c.Commit.Message,
				Unmeasured: !measured,
				Additions:  added,
				Deletions:  removed,
			}
			if c.Author != nil {
				commit.Login = c.Author.Login
			}
			out = append(out, commit)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if capped {
		out = append(out, Commit{Unmeasured: true})
	}
	return out, nil
}

// commitLines reads one commit's own line counts. GitHub answers null for a
// commit of more than 300 files, which is reported as unmeasured rather than as
// no lines, so the PR measures nothing rather than a rate from a number it does
// not have.
func (g GitHub) commitLines(ctx context.Context, repo, sha string) (added, removed int64, measured bool, err error) {
	endpoint := fmt.Sprintf("%s/repos/%s/commits/%s", g.endpoint(), repo, url.PathEscape(sha))
	res, err := g.do(ctx, http.MethodGet, endpoint, nil, fmt.Sprintf("read commit %s of %s", sha, repo))
	if err != nil {
		return 0, 0, false, err
	}
	defer func() { _ = res.Body.Close() }()
	var body struct {
		Stats *struct {
			Additions int64 `json:"additions"`
			Deletions int64 `json:"deletions"`
		} `json:"stats"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxPullsResponseBytes)).Decode(&body); err != nil {
		return 0, 0, false, fmt.Errorf("decoding commit %s of %s: %w", sha, repo, err)
	}
	if body.Stats == nil {
		return 0, 0, false, nil
	}
	return body.Stats.Additions, body.Stats.Deletions, true, nil
}

// do sends an authenticated GitHub API request and returns its response on
// any 2xx — the caller closes its body. reqBody may be nil for a GET. action
// names what the call was trying to do, for both the request-build error and
// a refusal's message.
func (g GitHub) do(ctx context.Context, method, endpoint string, reqBody io.Reader, action string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reqBody)
	if err != nil {
		return nil, fmt.Errorf("building the request to %s: %w", action, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+g.Token)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-GitHub-Api-Version", GitHubAPIVersion)
	client := g.Client
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", action, err)
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return res, nil
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBytes))
	return nil, &APIError{Status: res.StatusCode, Reason: refusalReason(raw), message: fmt.Sprintf("GitHub refused to %s: %s: %s", action, res.Status, snippet(raw))}
}

// refusalReason is the message of GitHub's JSON error body, or the body itself
// when it holds none.
func refusalReason(raw []byte) string {
	var body struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &body) == nil && body.Message != "" {
		return body.Message
	}
	return snippet(raw)
}

// APIError is a GitHub refusal, carrying the HTTP status so a caller can
// classify it without reading the message.
type APIError struct {
	Status int
	// Reason is the message GitHub gave, or its body where it gave none.
	Reason  string
	message string
}

func (e *APIError) Error() string { return e.message }

func (g GitHub) endpoint() string {
	if g.Endpoint == "" {
		return githubEndpoint
	}
	return g.Endpoint
}

// snippet renders a reply's body for a log line, on one line and truncated.
func snippet(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > maxErrorBytes {
		s = s[:maxErrorBytes] + "…"
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", "")
}
