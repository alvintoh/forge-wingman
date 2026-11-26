package planrefresh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

// leaderboardEndpoint is the llm-stats.com code-category index, the data behind its
// "Best AI for Coding" page. Probed 2026-11-26: 200 application/json of about 224 KB
// with no key, shaped {category_id, method, methodology{version}, games[], models[{
// model_id, sigma, conservative, games_played, games_available, benchmark_results{id:
// {score, source}}}]}. It is the site's own path, not its keyed API at
// docs.llm-stats.com, so a reply this build cannot read is recorded as the
// leaderboard being unread. Its scores are vendor-reported in 99.7% of the
// benchmark results underneath (1148 of 1151 on the probe date), so a model's
// provenance reads vendor-reported.
const leaderboardEndpoint = "https://api.llm-stats.com/leaderboard/categories/code/index"

// maxReplyBytes caps what a JSON source's reply is read as.
const maxReplyBytes = 1 << 20

// llmStatsSelfReported is the source llm-stats.com states for a score the model's
// vendor published.
const llmStatsSelfReported = "self_reported"

// Board reads the Coding leaderboard.
type Board struct {
	// Endpoint is the leaderboard's address; empty means the built-in one.
	Endpoint string
	// HTTP is the client requests go through; nil means NewClient().
	HTTP *http.Client
}

// BoardReport is the leaderboard as read: the benchmark it states and its own
// reading of each model it carries, keyed by the id the leaderboard states.
type BoardReport struct {
	Benchmark string
	Models    map[string]providers.Score
}

// Read reads the leaderboard. A reply this build cannot decode is an error, which
// a refresh records as the leaderboard being unread rather than as an empty one.
func (b Board) Read(ctx context.Context) (BoardReport, error) {
	endpoint := b.Endpoint
	if endpoint == "" {
		endpoint = leaderboardEndpoint
	}
	body, err := readBody(ctx, b.HTTP, endpoint, maxReplyBytes)
	if err != nil {
		return BoardReport{}, err
	}
	return decodeBoard(body)
}

// lenientText is a text field of the reply that only names the benchmark: any
// other JSON type reads as empty rather than failing the whole read.
type lenientText string

func (l *lenientText) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*l = lenientText(s)
	}
	return nil
}

// decodeBoard decodes an llm-stats.com category index. A model's figure is its
// conservative TrueSkill rating (mu - 3 sigma), the figure the site ranks by, so a
// sparsely scored model does not outrank a well-evidenced one; a model with no such
// figure is left out, as one the leaderboard does not score, and a reply that
// rates no model is an error. Only the fields a score is read from are typed
// strictly: the benchmark's name and the benchmark counts in the margin are
// descriptive, so a reshaped one costs a word of text, not the read. The reply
// states no date, so no score carries one.
func decodeBoard(body []byte) (BoardReport, error) {
	var reply struct {
		Category    lenientText `json:"category_id"`
		Method      lenientText `json:"method"`
		Methodology struct {
			Version lenientText `json:"version"`
		} `json:"methodology"`
		Models []struct {
			ID             string          `json:"model_id"`
			Conservative   *float64        `json:"conservative"`
			Sigma          float64         `json:"sigma"`
			GamesPlayed    json.RawMessage `json:"games_played"`
			GamesAvailable json.RawMessage `json:"games_available"`
			Results        map[string]struct {
				Source string `json:"source"`
			} `json:"benchmark_results"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		return BoardReport{}, fmt.Errorf("decoding the leaderboard: %w", err)
	}
	benchmark := providers.CapText(benchmarkName(string(reply.Category), string(reply.Method), string(reply.Methodology.Version)), maxFieldBytes)
	report := BoardReport{Benchmark: benchmark, Models: make(map[string]providers.Score, len(reply.Models))}
	for _, m := range reply.Models {
		name := providers.CapText(strings.TrimSpace(m.ID), maxFieldBytes)
		if name == "" || m.Conservative == nil {
			continue
		}
		sources := make([]string, 0, len(m.Results))
		for _, r := range m.Results {
			sources = append(sources, r.Source)
		}
		report.Models[name] = providers.Score{
			Listed:     true,
			Value:      *m.Conservative,
			Benchmark:  benchmark,
			Provenance: boardProvenance(sources),
			Margin:     boardMargin(m.Sigma, m.GamesPlayed, m.GamesAvailable),
		}
	}
	if len(report.Models) == 0 {
		return BoardReport{}, errors.New("the leaderboard carries no rated models")
	}
	return report, nil
}

// benchmarkName is the benchmark a reply states, as the index of its category by
// its rating method.
func benchmarkName(category, method, version string) string {
	name := strings.Join(strings.Fields("llm-stats "+category+" index"), " ")
	if how := strings.Join(strings.Fields(method+" "+version), " "); how != "" {
		name += " (" + how + ")"
	}
	return name
}

// boardMargin is the margin a model's rating states: its sigma, and how many of
// the benchmarks available it was scored on when the reply gives both as numbers.
func boardMargin(sigma float64, played, available json.RawMessage) string {
	margin := "sigma " + strconv.FormatFloat(sigma, 'f', -1, 64)
	p, pOK := count(played)
	a, aOK := count(available)
	if pOK && aOK {
		margin += " over " + p + " of " + a + " benchmarks"
	}
	return margin
}

// count is a benchmark count as the reply states it, accepting 9 and 9.0, and
// false for anything that is not a number.
func count(raw json.RawMessage) (string, bool) {
	n, err := strconv.ParseFloat(strings.Trim(string(raw), `"`), 64)
	if err != nil {
		return "", false
	}
	return strconv.FormatFloat(n, 'f', -1, 64), true
}

// boardProvenance is who ran a model's benchmarks, from the source llm-stats.com
// states for each. Only scores all published by the vendor are vendor-reported:
// the site does not define its other source, "verified", so a model with any
// score from it, or with none, is unknown rather than read as independently run.
func boardProvenance(sources []string) providers.Provenance {
	if len(sources) == 0 {
		return providers.ProvenanceUnknown
	}
	for _, s := range sources {
		if s != llmStatsSelfReported {
			return providers.ProvenanceUnknown
		}
	}
	return providers.ProvenanceVendorReported
}
