package planrefresh

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

// leaderboardEndpoint is the Coding leaderboard a refresh reads. UNVERIFIED: no
// probe has confirmed the address or the reply's shape, so the command lets the
// owner name another one, and a reply this build cannot read is recorded as the
// leaderboard being unread rather than guessed at.
const leaderboardEndpoint = "https://llm-stats.com/api/leaderboard/coding"

// maxReplyBytes caps what a JSON source's reply is read as.
const maxReplyBytes = 1 << 20

// Board reads the Coding leaderboard.
type Board struct {
	// Endpoint is the leaderboard's address; empty means the built-in one.
	Endpoint string
	// HTTP is the client requests go through; nil means NewClient().
	HTTP *http.Client
}

// BoardReport is the leaderboard as read: the benchmark and date it states, and
// its own reading of each model it carries. Models is keyed by the name the
// leaderboard states and by that name's own last segment, so a model is found by
// either the full id the leaderboard states or the short name it is known by.
type BoardReport struct {
	Benchmark string
	On        string
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
	var reply struct {
		Benchmark string `json:"benchmark"`
		Date      string `json:"date"`
		Models    []struct {
			Model      string  `json:"model"`
			Score      float64 `json:"score"`
			Provenance string  `json:"provenance"`
			Margin     string  `json:"margin"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		return BoardReport{}, fmt.Errorf("decoding the leaderboard: %w", err)
	}
	report := BoardReport{
		Benchmark: providers.CapText(strings.TrimSpace(reply.Benchmark), maxFieldBytes),
		On:        providers.CapText(strings.TrimSpace(reply.Date), maxFieldBytes),
		Models:    make(map[string]providers.Score, len(reply.Models)),
	}
	// A short name is set only where no exact name holds it, so a model is never
	// read as another that shares its last segment.
	shorts := make(map[string]providers.Score)
	for _, m := range reply.Models {
		name := providers.CapText(strings.TrimSpace(m.Model), maxFieldBytes)
		if name == "" {
			continue
		}
		score := providers.Score{
			Listed:     true,
			Value:      m.Score,
			Benchmark:  report.Benchmark,
			ScoredOn:   report.On,
			Provenance: providers.ParseProvenance(m.Provenance),
			Margin:     providers.CapText(strings.TrimSpace(m.Margin), maxFieldBytes),
		}
		report.Models[name] = score
		if short := bare(name); short != name {
			if _, taken := shorts[short]; !taken {
				shorts[short] = score
			}
		}
	}
	for short, score := range shorts {
		if _, exact := report.Models[short]; !exact {
			report.Models[short] = score
		}
	}
	return report, nil
}
