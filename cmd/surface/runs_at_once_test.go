package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// stubRecommender answers with the recommendation it holds.
type stubRecommender struct {
	rec store.RecommendedRunsAtOnce
	err error
}

func (s stubRecommender) RecommendedRunsAtOnce(context.Context) (store.RecommendedRunsAtOnce, error) {
	return s.rec, s.err
}

var defaultTuning = dispatcher.DefaultTuning

// atOnceMux serves the mux with n saved, rec behind the recommendation, and
// env as the dispatcher settings.
func atOnceMux(n *stubRunsPerRepo, rec RunsAtOnceRecommender, env map[string]string) *http.ServeMux {
	d := Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Recommend: rec, Getenv: func(k string) string { return env[k] }}
	if n != nil {
		d.RunsAtOnce = n
	}
	return newMux(d)
}

func serveAtOnce(t *testing.T, mux *http.ServeMux, method, body string) (*httptest.ResponseRecorder, runsAtOnceResponse) {
	t.Helper()
	req := httptest.NewRequest(method, "/api/runs-at-once", strings.NewReader(body))
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var got runsAtOnceResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
	}
	return rec, got
}

func TestRunsAtOnceServesTheSavedNWithTheRecommendationBesideIt(t *testing.T) {
	rec := store.RecommendedRunsAtOnce{At: 2, Tuning: defaultTuning,
		Recommendation: dispatcher.Recommendation{N: 3, Stable: 5, Basis: dispatcher.BasisRise}}
	res, got := serveAtOnce(t, atOnceMux(&stubRunsPerRepo{n: 2}, stubRecommender{rec: rec}, nil), http.MethodGet, "")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", res.Code, res.Body.String())
	}
	want := runsAtOnceResponse{RunsAtOnce: 2, Max: 15, Recommendation: &recommendationView{
		N: 3, Basis: "rise", Stable: 5, StableRuns: 5, RiseWithin: 1.25, HalveBeyond: 1.5}}
	if got.RunsAtOnce != want.RunsAtOnce || got.Max != want.Max || got.Recommendation == nil || *got.Recommendation != *want.Recommendation {
		t.Fatalf("got %+v %+v, want %+v %+v", got, got.Recommendation, want, want.Recommendation)
	}
}

func TestRunsAtOnceRecommendsTheOwnersNWhileNothingIsObservedAtIt(t *testing.T) {
	for name, rec := range map[string]store.RecommendedRunsAtOnce{
		"observed at another N": {At: 2, Tuning: defaultTuning, Recommendation: dispatcher.Recommendation{N: 3, Stable: 5, Basis: dispatcher.BasisRise}},
		"nothing observed":      {At: 3, Tuning: defaultTuning},
	} {
		t.Run(name, func(t *testing.T) {
			_, got := serveAtOnce(t, atOnceMux(&stubRunsPerRepo{n: 3}, stubRecommender{rec: rec}, nil), http.MethodGet, "")
			if r := got.Recommendation; r == nil || r.N != 3 || r.Basis != "none" || r.Stable != 0 {
				t.Fatalf("recommendation = %+v, want 3 with nothing observed", r)
			}
		})
	}
}

func TestRunsAtOnceHoldsTheRecommendationAndNToTheMax(t *testing.T) {
	for name, tt := range map[string]struct {
		saved, at, max int
		env            map[string]string
	}{
		"fifteen under the default platform cap":  {saved: 20, at: 15, max: 15},
		"a configured platform cap under fifteen": {saved: 15, at: 12, max: 12, env: map[string]string{"WINGMAN_PLATFORM_CAP": "12"}},
	} {
		t.Run(name, func(t *testing.T) {
			rec := store.RecommendedRunsAtOnce{At: tt.at, Tuning: defaultTuning,
				Recommendation: dispatcher.Recommendation{N: tt.at + 1, Stable: 5, Basis: dispatcher.BasisRise}}
			mux := atOnceMux(&stubRunsPerRepo{n: tt.saved}, stubRecommender{rec: rec}, tt.env)
			_, got := serveAtOnce(t, mux, http.MethodGet, "")
			if got.RunsAtOnce != tt.max || got.Max != tt.max || got.Recommendation == nil || got.Recommendation.N != tt.max {
				t.Fatalf("got %+v %+v, want N, max and the recommendation held to %d", got, got.Recommendation, tt.max)
			}
		})
	}
}

func TestRunsAtOnceServesNullWhenTheRecommendationCannotBeRead(t *testing.T) {
	res, got := serveAtOnce(t, atOnceMux(&stubRunsPerRepo{n: 2}, stubRecommender{err: errors.New("no ledger")}, nil), http.MethodGet, "")
	if res.Code != http.StatusOK || got.RunsAtOnce != 2 || got.Recommendation != nil {
		t.Fatalf("status %d, got %+v, want N 2 with a null recommendation", res.Code, got)
	}
}

func TestRunsAtOnceReportsAReadFailureAndNeedsAStore(t *testing.T) {
	if res, _ := serveAtOnce(t, atOnceMux(&stubRunsPerRepo{readErr: errors.New("x")}, stubRecommender{}, nil), http.MethodGet, ""); res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", res.Code)
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if res, _ := serveAtOnce(t, atOnceMux(nil, stubRecommender{}, nil), method, `{"runs_at_once": 3}`); res.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, want 503", method, res.Code)
		}
	}
}

// TestSetRunsAtOnceSavesAWholeNumberFromOneToFifteen is the range, inclusive at both ends.
func TestSetRunsAtOnceSavesAWholeNumberFromOneToFifteen(t *testing.T) {
	for _, n := range []int{1, 3, 15} {
		saved := &stubRunsPerRepo{n: 2}
		res, got := serveAtOnce(t, atOnceMux(saved, stubRecommender{rec: store.RecommendedRunsAtOnce{At: 2, Tuning: defaultTuning}}, nil),
			http.MethodPost, fmt.Sprintf(`{"runs_at_once": %d}`, n))
		if res.Code != http.StatusOK || saved.n != n || got.RunsAtOnce != n || saved.setAt.IsZero() {
			t.Fatalf("status %d, stored %d, answered %d, want %d saved", res.Code, saved.n, got.RunsAtOnce, n)
		}
		if r := got.Recommendation; n != 2 && (r == nil || r.N != n || r.Basis != "none") {
			t.Fatalf("recommendation = %+v, want %d with nothing observed after the save", r, n)
		}
	}
}

// TestSetRunsAtOnceRefusesAValueOutsideOneToFifteen is the range's 400.
func TestSetRunsAtOnceRefusesAValueOutsideOneToFifteen(t *testing.T) {
	for name, tt := range map[string]struct {
		body string
		env  map[string]string
	}{
		"zero":                   {`{"runs_at_once": 0}`, nil},
		"negative":               {`{"runs_at_once": -1}`, nil},
		"above fifteen":          {`{"runs_at_once": 16}`, nil},
		"above a configured cap": {`{"runs_at_once": 11}`, map[string]string{"WINGMAN_PLATFORM_CAP": "10"}},
		"not a whole number":     {`{"runs_at_once": 2.5}`, nil},
		"a string":               {`{"runs_at_once": "3"}`, nil},
		"no value":               {`{}`, nil},
	} {
		t.Run(name, func(t *testing.T) {
			saved := &stubRunsPerRepo{n: 2}
			res, _ := serveAtOnce(t, atOnceMux(saved, stubRecommender{}, tt.env), http.MethodPost, tt.body)
			if res.Code != http.StatusBadRequest || saved.n != 2 || !saved.setAt.IsZero() {
				t.Fatalf("status %d, stored %d, want 400 and nothing saved", res.Code, saved.n)
			}
		})
	}
}

func TestSetRunsAtOnceNeedsAJSONBodyAndReportsAFailedSave(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/runs-at-once", strings.NewReader("runs_at_once=3"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	atOnceMux(&stubRunsPerRepo{n: 2}, stubRecommender{}, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", rec.Code)
	}
	if res, _ := serveAtOnce(t, atOnceMux(&stubRunsPerRepo{setErr: errors.New("x")}, stubRecommender{}, nil), http.MethodPost, `{"runs_at_once": 3}`); res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", res.Code)
	}
}
