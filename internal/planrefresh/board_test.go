package planrefresh

import (
	"context"
	"net/http"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

// boardReply is the Coding leaderboard's own reply: two models, one the
// leaderboard ran itself and one the vendor reported.
const boardReply = `{"benchmark":"Coding","date":"2026-10-12","models":[
	{"model":"deepseek/deepseek-v4.1-flash","score":61.2,"provenance":"independent","margin":"±1.8"},
	{"model":"glm-5","score":54,"provenance":"vendor-reported","margin":"±0.9"}]}`

func TestBoardReadsEachModelsScore(t *testing.T) {
	srv := bodyServer(t, http.StatusOK, boardReply)
	got, err := (Board{Endpoint: srv.URL, HTTP: srv.Client()}).Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Benchmark != "Coding" || got.On != "2026-10-12" {
		t.Fatalf("benchmark %q on %q, want the leaderboard's own", got.Benchmark, got.On)
	}
	want := providers.Score{Listed: true, Value: 61.2, Benchmark: "Coding", ScoredOn: "2026-10-12",
		Provenance: providers.ProvenanceIndependently, Margin: "±1.8"}
	if score := got.Models["deepseek/deepseek-v4.1-flash"]; score != want {
		t.Fatalf("score %+v, want %+v", score, want)
	}
	if score := got.Models["deepseek-v4.1-flash"]; score != want {
		t.Fatal("the model is not found under its own last segment")
	}
	if score := got.Models["glm-5"]; score.Provenance != providers.ProvenanceVendorReported || score.Value != 54 {
		t.Fatalf("glm-5 %+v, want the vendor's own report of it", score)
	}
}

// TestBoardRefusesWhatItCannotRead asserts an error status and a reply this build
// cannot read are refused, so a refresh records the leaderboard as unread rather
// than as one carrying no models.
func TestBoardRefusesWhatItCannotRead(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
	}{
		{"a 500", http.StatusInternalServerError, "boom"},
		{"a malformed body", http.StatusOK, `{"models":"not a list"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := bodyServer(t, tt.status, tt.body)
			if _, err := (Board{Endpoint: srv.URL, HTTP: srv.Client()}).Read(context.Background()); err == nil {
				t.Fatal("Read accepted what it cannot read")
			}
		})
	}
}

// TestBoardPrefersAnExactNameToAShortOne asserts a model the leaderboard names
// exactly is never replaced by another model sharing its last segment, whichever
// the leaderboard lists first.
func TestBoardPrefersAnExactNameToAShortOne(t *testing.T) {
	for name, models := range map[string]string{
		"short first": `{"model":"acme/glm-5","score":10},{"model":"glm-5","score":20}`,
		"exact first": `{"model":"glm-5","score":20},{"model":"acme/glm-5","score":10}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := bodyServer(t, http.StatusOK, `{"benchmark":"Coding","models":[`+models+`]}`)
			got, err := (Board{Endpoint: srv.URL, HTTP: srv.Client()}).Read(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got.Models["glm-5"].Value != 20 || got.Models["acme/glm-5"].Value != 10 {
				t.Fatalf("glm-5 %v acme/glm-5 %v, want each model its own score", got.Models["glm-5"].Value, got.Models["acme/glm-5"].Value)
			}
		})
	}
}
