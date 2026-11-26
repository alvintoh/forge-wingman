package planrefresh

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

const benchmarkOfTheSample = "llm-stats code index (trueskill 3.1)"

// readSample decodes the trimmed reply recorded from the built-in endpoint on
// 2026-11-26, as the leaderboard served it.
func readSample(t *testing.T) BoardReport {
	t.Helper()
	body, err := os.ReadFile("testdata/llmstats_code_index_2026-11-26.json")
	if err != nil {
		t.Fatal(err)
	}
	report, err := decodeBoard(body)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestBoardDecodesTheRecordedReply(t *testing.T) {
	got := readSample(t)
	if got.Benchmark != benchmarkOfTheSample {
		t.Fatalf("benchmark %q, want %q", got.Benchmark, benchmarkOfTheSample)
	}
	want := providers.Score{Listed: true, Value: 38.44, Benchmark: benchmarkOfTheSample,
		Provenance: providers.ProvenanceVendorReported, Margin: "sigma 1.53 over 9 of 70 benchmarks"}
	if score := got.Models["deepseek-v4.1-flash"]; score != want {
		t.Fatalf("deepseek-v4.1-flash %+v, want %+v", score, want)
	}
	for model, value := range map[string]float64{
		"claude-haiku-5-5":         37.15,
		"gemini-3.8-flash":         32.57,
		"gemini-3.5-flash-lite":    15.1,
		"deepseek-v4.1-flash-1017": 38.55,
	} {
		if score := got.Models[model]; !score.Listed || score.Value != value {
			t.Errorf("%s %+v, want listed at %v", model, score, value)
		}
	}
}

// TestBoardDoesNotReadOneModelAsAnother asserts a model the leaderboard does not
// carry is unlisted even when it carries a sibling whose id the model's own id
// begins with.
func TestBoardDoesNotReadOneModelAsAnother(t *testing.T) {
	got := readSample(t)
	if score := got.Models["muse-spark-1.3-contributor"]; score.Listed {
		t.Fatalf("muse-spark-1.3-contributor %+v, want it unlisted: the leaderboard carries only muse-spark-1.3", score)
	}
	if !got.Models["muse-spark-1.3"].Listed {
		t.Fatal("muse-spark-1.3 is not listed, though the leaderboard carries it")
	}
}

// TestBoardProvenanceNamesOnlyWhatTheSourceStates asserts only scores all
// published by the vendor read as vendor-reported, and that the site's own
// "verified" source is not read as an independent run.
func TestBoardProvenanceNamesOnlyWhatTheSourceStates(t *testing.T) {
	for _, tt := range []struct {
		name    string
		sources []string
		want    providers.Provenance
	}{
		{"all self-reported", []string{"self_reported", "self_reported"}, providers.ProvenanceVendorReported},
		{"verified", []string{"verified"}, providers.ProvenanceUnknown},
		{"mixed", []string{"self_reported", "verified"}, providers.ProvenanceUnknown},
		{"none", nil, providers.ProvenanceUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := boardProvenance(tt.sources); got != tt.want {
				t.Fatalf("boardProvenance(%v) = %q, want %q", tt.sources, got, tt.want)
			}
		})
	}
	if got := readSample(t).Models["u2"].Provenance; got != providers.ProvenanceUnknown {
		t.Fatalf("u2, whose scores are all verified, is %q, want unknown", got)
	}
}

// TestBoardReadsWhatOnlyDescribesLeniently asserts a benchmark count read as 9.0
// or as text, and a method of the wrong type, cost a word of the margin or of the
// benchmark and not the read, while the rating itself is still read.
func TestBoardReadsWhatOnlyDescribesLeniently(t *testing.T) {
	for name, tt := range map[string]struct {
		method, counts, benchmark, margin string
	}{
		"a count as 9.0":   {`"trueskill"`, `,"games_played":9.0,"games_available":70.0`, "llm-stats code index (trueskill 3.1)", "sigma 1.5 over 9 of 70 benchmarks"},
		"a count as text":  {`"trueskill"`, `,"games_played":"9","games_available":"70"`, "llm-stats code index (trueskill 3.1)", "sigma 1.5 over 9 of 70 benchmarks"},
		"a count as words": {`"trueskill"`, `,"games_played":"many","games_available":70`, "llm-stats code index (trueskill 3.1)", "sigma 1.5"},
		"no counts":        {`"trueskill"`, ``, "llm-stats code index (trueskill 3.1)", "sigma 1.5"},
		"a method as 5":    {`5`, ``, "llm-stats code index (3.1)", "sigma 1.5"},
	} {
		t.Run(name, func(t *testing.T) {
			body := `{"category_id":"code","method":` + tt.method + `,"methodology":{"version":"3.1"},` +
				`"models":[{"model_id":"a","conservative":4,"sigma":1.5` + tt.counts + `}]}`
			got, err := decodeBoard([]byte(body))
			if err != nil {
				t.Fatal(err)
			}
			if score := got.Models["a"]; score.Value != 4 || score.Margin != tt.margin || score.Benchmark != tt.benchmark {
				t.Fatalf("score %+v, want value 4, margin %q, benchmark %q", score, tt.margin, tt.benchmark)
			}
		})
	}
}

// TestBoardReadsSomeRatedModelsOfAMixedReply asserts a reply where only some
// models carry a rating is read, listing exactly those.
func TestBoardReadsSomeRatedModelsOfAMixedReply(t *testing.T) {
	got, err := decodeBoard([]byte(`{"models":[{"model_id":"a","rating":3},{"model_id":"b","conservative":5},{"id":"c","conservative":6}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 1 || !got.Models["b"].Listed {
		t.Fatalf("models %+v, want only b, the one model with an id and a rating", got.Models)
	}
}

// TestBoardLeavesOutAModelWithNoFigure asserts a model the leaderboard states no
// rating for is unlisted rather than scored zero, while a stated zero is kept.
func TestBoardLeavesOutAModelWithNoFigure(t *testing.T) {
	got, err := decodeBoard([]byte(`{"models":[{"model_id":"scored","conservative":0},{"model_id":"unscored"},{"conservative":9}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Models["scored"].Listed {
		t.Fatalf("scored %+v, want a stated rating of zero kept", got.Models["scored"])
	}
	if got.Models["unscored"].Listed || len(got.Models) != 1 {
		t.Fatalf("models %+v, want only the one model carrying a rating", got.Models)
	}
}

// TestBoardRefusesWhatItCannotRead asserts each reply that is not a leaderboard
// is an error, so a refresh records the leaderboard as unread rather than as one
// carrying no models.
func TestBoardRefusesWhatItCannotRead(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
	}{
		{"a 404", http.StatusNotFound, `{"detail":"Not Found"}`},
		{"a 500", http.StatusInternalServerError, "boom"},
		{"an empty body", http.StatusOK, ""},
		{"malformed JSON", http.StatusOK, `{"models":[`},
		{"models of the wrong type", http.StatusOK, `{"models":"not a list"}`},
		{"a rating of the wrong type beside a rated model", http.StatusOK,
			`{"models":[{"model_id":"a","conservative":1},{"model_id":"b","conservative":"x"}]}`},
		{"models with the rating renamed", http.StatusOK, `{"category_id":"code","models":[{"model_id":"a","rating":3}]}`},
		{"models with the id renamed", http.StatusOK, `{"category_id":"code","models":[{"id":"x","conservative":3}]}`},
		{"an empty list", http.StatusOK, `{"category_id":"code","models":[]}`},
		{"another JSON document", http.StatusOK, `{"status":"healthy"}`},
		{"an oversized reply", http.StatusOK, `{"models":[{"model_id":"a","conservative":1}],"pad":"` + strings.Repeat("x", maxReplyBytes) + `"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := bodyServer(t, tt.status, tt.body)
			if _, err := (Board{Endpoint: srv.URL, HTTP: srv.Client()}).Read(context.Background()); err == nil {
				t.Fatal("Read accepted what it cannot read")
			}
		})
	}
}

func TestBoardReportsAConnectionError(t *testing.T) {
	srv := bodyServer(t, http.StatusOK, "{}")
	endpoint, client := srv.URL, srv.Client()
	srv.Close()
	if _, err := (Board{Endpoint: endpoint, HTTP: client}).Read(context.Background()); err == nil {
		t.Fatal("Read accepted a connection that failed")
	}
}

func TestBoardReadsTheBuiltInEndpointWhenNoneIsNamed(t *testing.T) {
	var read string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		read = r.URL.String()
		return &http.Response{StatusCode: http.StatusNotFound, Body: http.NoBody, Header: http.Header{}}, nil
	})}
	if _, err := (Board{HTTP: client}).Read(context.Background()); err == nil {
		t.Fatal("Read accepted a 404")
	}
	if read != "https://api.llm-stats.com/leaderboard/categories/code/index" {
		t.Fatalf("read %q, want the llm-stats.com code index", read)
	}
}

// roundTripFunc is an http.RoundTripper made of a function.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
