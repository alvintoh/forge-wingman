package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

// TestAutonomyRefusesAWriteMethod asserts the autonomy endpoint refuses every
// method that could set anything: the surface has no such route, and the switch
// is set through its own endpoint (AC5).
func TestAutonomyRefusesAWriteMethod(t *testing.T) {
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Measured: &stubMeasured{}})

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/autonomy", strings.NewReader(`{}`)))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
		}
	}
}

// TestAutonomyWithoutAStoreIsUnavailable asserts a local run with no store
// answers 503 rather than figures nobody could read.
func TestAutonomyWithoutAStoreIsUnavailable(t *testing.T) {
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger()})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/autonomy", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestAutonomyWithoutAStoreIsUnavailableWhateverTheRepo asserts a missing store
// is answered before the repo parameter is judged.
func TestAutonomyWithoutAStoreIsUnavailableWhateverTheRepo(t *testing.T) {
	if rec := serveAutonomy(nil, nil, "/api/autonomy?repo=widget"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestAutonomyReportsAStoreItCannotRead asserts a failed read is a 500 with the
// failure reported, not figures nobody could read.
func TestAutonomyReportsAStoreItCannotRead(t *testing.T) {
	var log strings.Builder
	reader := &stubMeasured{err: errors.New("firestore unreachable")}
	mux := newMux(Deps{
		SPA:      fstest.MapFS{},
		Logger:   slog.New(slog.NewTextHandler(&log, nil)),
		Measured: reader,
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/autonomy", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(log.String(), "autonomyNotRead") {
		t.Fatalf("log = %q, want the failed read reported", log.String())
	}
}

// TestAutonomyAsksForTheMeasuredCap asserts the endpoint reads the measured
// merges under the cap the figures are drawn from, never the whole collection.
func TestAutonomyAsksForTheMeasuredCap(t *testing.T) {
	reader := &stubMeasured{}
	getAutonomy(t, reader)
	if reader.limit != measuredShown {
		t.Errorf("limit asked = %d, want %d", reader.limit, measuredShown)
	}
}

// twoRepositories is 24 measured merges, newest first, alternating between two
// repositories: octo/scratch rewrites 5 of every 100 agent lines and octo/other
// 100, so the newest ten pool to 52.5% and scratch alone reads 5.0%.
func twoRepositories() []dispatcher.Sample {
	merges := make([]dispatcher.Sample, 0, 24)
	for i := range 24 {
		merge := dispatcher.Sample{
			Run: fmt.Sprintf("run-%d", i), Repo: "octo/scratch", Size: "M", PR: 200 - i,
			MergedAt:   measuredAt().Add(-time.Duration(i) * time.Hour),
			BuildModel: "p/a",
			Rewrite:    dispatcher.Rewrite{AgentLines: 100, OwnerLines: 5},
		}
		if i%2 == 1 {
			merge.Repo, merge.Size, merge.BuildModel = "octo/other", "S", "p/b"
			merge.OwnerLines = 100
		}
		merges = append(merges, merge)
	}
	return merges
}

// autonomyBody serves target over merges with the auto-merge switch on and
// returns the body of the 200 it answers.
func autonomyBody(t *testing.T, merges []dispatcher.Sample, target string) string {
	t.Helper()
	return getAutonomyAt(t, &stubMeasured{merges: merges}, &stubAutoMerge{enabled: true}, target).Body.String()
}

// autonomyBlocks is the two blocks of an autonomy answer as served.
type autonomyBlocks struct {
	AutoMerge json.RawMessage `json:"auto_merge"`
	Rewrite   json.RawMessage `json:"rewrite"`
}

func blocksOf(t *testing.T, body string) autonomyBlocks {
	t.Helper()
	var blocks autonomyBlocks
	if err := json.Unmarshal([]byte(body), &blocks); err != nil {
		t.Fatal(err)
	}
	return blocks
}

// TestAutonomyWithoutARepoIsTheWholeAnswer asserts a request naming no repository
// is answered as it was before one could be named: the figures of every merge,
// whatever else the query carries, and the same answer a scoped request gives
// when its repository is the only one.
func TestAutonomyWithoutARepoIsTheWholeAnswer(t *testing.T) {
	merges := twoRepositories()
	var want strings.Builder
	if err := json.NewEncoder(&want).Encode(autonomyResponse{
		AutoMerge: renderAutoMerge(merges, true, true),
		Rewrite:   renderRewrite(merges),
	}); err != nil {
		t.Fatal(err)
	}

	for _, target := range []string{"/api/autonomy", "/api/autonomy?utm=x"} {
		if got := autonomyBody(t, merges, target); got != want.String() {
			t.Errorf("%s = %s, want the figures of every merge %s", target, got, want.String())
		}
	}

	single := twoRepositories()
	for i := range single {
		single[i].Repo = "octo/scratch"
	}
	if scoped, whole := autonomyBody(t, single, "/api/autonomy?repo=octo/scratch"), autonomyBody(t, single, "/api/autonomy"); scoped != whole {
		t.Errorf("scoped = %s, want the whole answer %s", scoped, whole)
	}
}

// scopedFigures is the rewrite figures of a scoped answer that the scoping tests
// read, the tiles and series kept as served.
type scopedFigures struct {
	Tiles  json.RawMessage `json:"tiles"`
	Series json.RawMessage `json:"series"`
	PRs    []struct {
		PR   int    `json:"pr"`
		Repo string `json:"repo"`
	} `json:"prs"`
	BySize []struct {
		Size string `json:"size"`
		PRs  int    `json:"prs"`
	} `json:"by_size"`
	ByModel struct {
		Build []struct {
			Model string `json:"model"`
			PRs   int    `json:"prs"`
		} `json:"build"`
	} `json:"by_model"`
	ByRepo []repoFigures `json:"by_repo"`
}

// repoFigures is one by_repo row, its tiles and series kept as served.
type repoFigures struct {
	Repo   string          `json:"repo"`
	Tiles  json.RawMessage `json:"tiles"`
	Series json.RawMessage `json:"series"`
}

func figuresOf(t *testing.T, body string) scopedFigures {
	t.Helper()
	var figures scopedFigures
	if err := json.Unmarshal(blocksOf(t, body).Rewrite, &figures); err != nil {
		t.Fatal(err)
	}
	return figures
}

// TestAutonomyScopedToARepositoryServesItsOwnFigures asserts a repo parameter
// computes every rewrite figure over that repository's merges alone, its tiles
// and series the ones its by_repo row carries in the whole answer, from the same
// capped read.
func TestAutonomyScopedToARepositoryServesItsOwnFigures(t *testing.T) {
	reader := &stubMeasured{merges: twoRepositories()}
	rec := getAutonomyAt(t, reader, &stubAutoMerge{enabled: true}, "/api/autonomy?repo=octo/scratch")
	if reader.limit != measuredShown {
		t.Errorf("limit asked = %d, want %d", reader.limit, measuredShown)
	}
	figures := figuresOf(t, rec.Body.String())

	var tiles []struct {
		PRs        []int  `json:"prs"`
		Asked      int    `json:"asked"`
		AgentLines int64  `json:"agent_lines"`
		OwnerLines int64  `json:"owner_lines"`
		Rate       string `json:"rate"`
	}
	if err := json.Unmarshal(figures.Tiles, &tiles); err != nil {
		t.Fatal(err)
	}
	if len(tiles) != 3 || tiles[0].Asked != 10 || tiles[0].AgentLines != 1000 || tiles[0].OwnerLines != 50 || tiles[0].Rate != "5.0%" ||
		!slices.Equal(tiles[0].PRs, []int{200, 198, 196, 194, 192, 190, 188, 186, 184, 182}) {
		t.Errorf("tiles = %+v, want the trailing window over scratch's ten newest merges alone", tiles)
	}

	// Twelve scratch merges fill three windows of ten, oldest first.
	var series []struct {
		Repo string `json:"repo"`
		PR   int    `json:"pr"`
		Rate string `json:"rate"`
	}
	if err := json.Unmarshal(figures.Series, &series); err != nil {
		t.Fatal(err)
	}
	if len(series) != 3 || series[0].PR != 196 || series[1].PR != 198 || series[2].PR != 200 {
		t.Fatalf("series = %+v, want the three windows scratch's twelve merges fill", series)
	}
	for _, point := range series {
		if point.Repo != "octo/scratch" || point.Rate != "5.0%" {
			t.Errorf("point = %+v, want scratch's own pooled rate", point)
		}
	}

	if len(figures.PRs) != dispatcher.TrailingPRs {
		t.Fatalf("prs = %+v, want the trailing window of scratch's merges", figures.PRs)
	}
	for i, pr := range figures.PRs {
		if pr.Repo != "octo/scratch" || pr.PR != 200-2*i {
			t.Errorf("prs[%d] = %+v, want scratch's merges newest first", i, pr)
		}
	}

	sizes := map[string]int{}
	for _, row := range figures.BySize {
		sizes[row.Size] = row.PRs
	}
	if want := map[string]int{"S": 0, "M": 12, "L": 0, "unsized": 0}; !maps.Equal(sizes, want) {
		t.Errorf("by_size = %v, want scratch's twelve merges, all size M", sizes)
	}
	if build := figures.ByModel.Build; len(build) != 1 || build[0].Model != "p/a" || build[0].PRs != 12 {
		t.Errorf("by_model.build = %+v, want scratch's model alone", build)
	}

	if len(figures.ByRepo) != 1 || figures.ByRepo[0].Repo != "octo/scratch" {
		t.Fatalf("by_repo = %+v, want the one repository", figures.ByRepo)
	}
	whole := figuresOf(t, autonomyBody(t, twoRepositories(), "/api/autonomy"))
	at := slices.IndexFunc(whole.ByRepo, func(row repoFigures) bool { return row.Repo == "octo/scratch" })
	if at < 0 {
		t.Fatalf("by_repo = %+v, want a scratch row in the whole answer", whole.ByRepo)
	}
	if row := whole.ByRepo[at]; !bytes.Equal(figures.Tiles, row.Tiles) || !bytes.Equal(figures.Series, row.Series) {
		t.Errorf("tiles = %s series = %s, want the scratch row of the whole answer: %s %s", figures.Tiles, figures.Series, row.Tiles, row.Series)
	}

	if upper := autonomyBody(t, twoRepositories(), "/api/autonomy?repo=OCTO/Scratch"); upper != rec.Body.String() {
		t.Errorf("OCTO/Scratch = %s, want the answer octo/scratch gets %s", upper, rec.Body.String())
	}
}

// TestAutonomyAnUnknownRepositoryServesTheEmptyShape asserts a repository with no
// measured merge is answered with the figures of no merges at all, a 200 and not
// an error.
func TestAutonomyAnUnknownRepositoryServesTheEmptyShape(t *testing.T) {
	empty := blocksOf(t, autonomyBody(t, nil, "/api/autonomy"))
	unknown := blocksOf(t, autonomyBody(t, twoRepositories(), "/api/autonomy?repo=octo/missing"))
	if !bytes.Equal(unknown.Rewrite, empty.Rewrite) {
		t.Errorf("rewrite = %s, want the empty shape %s", unknown.Rewrite, empty.Rewrite)
	}
}

// TestAutonomyScopedAnswerLeavesTheGatePooled asserts the auto-merge verdict is
// read over every merge whatever repository the figures are scoped to: the gate
// a scoped request shows is the gate the runner applies.
func TestAutonomyScopedAnswerLeavesTheGatePooled(t *testing.T) {
	pooled := blocksOf(t, autonomyBody(t, twoRepositories(), "/api/autonomy")).AutoMerge
	var gate struct {
		RateValue  string `json:"rate_value"`
		RateAllows bool   `json:"rate_allows"`
		Reason     string `json:"reason"`
	}
	if err := json.Unmarshal(pooled, &gate); err != nil {
		t.Fatal(err)
	}
	if gate.RateValue != "52.5%" || gate.RateAllows || gate.Reason != "rate-above-bar" {
		t.Fatalf("auto_merge = %+v, want the newest ten merges of both repositories pooled", gate)
	}

	for _, repo := range []string{"octo/scratch", "octo/other", "octo/missing"} {
		scoped := blocksOf(t, autonomyBody(t, twoRepositories(), "/api/autonomy?repo="+repo)).AutoMerge
		if !bytes.Equal(scoped, pooled) {
			t.Errorf("repo=%s auto_merge = %s, want the pooled verdict %s", repo, scoped, pooled)
		}
	}
}

// TestAutonomyRefusesAMalformedRepository asserts a repo parameter that is not
// exactly one owner/name repository within its length bound, or a query that
// does not parse, is a 400 saying which before the store is read, while a
// repository at the bound is served.
func TestAutonomyRefusesAMalformedRepository(t *testing.T) {
	const notOneRepo, noParse = "repo is not one owner/name repository", "the query does not parse"
	for _, tt := range []struct{ name, query, want string }{
		{"not owner/name", "repo=widget", notOneRepo},
		{"empty", "repo=", notOneRepo},
		{"a space inside", "repo=octo/a%20b", notOneRepo},
		{"a third part", "repo=octo/scratch/x", notOneRepo},
		{"a leading space", "repo=%20octo/scratch", notOneRepo},
		{"a broken escape", "repo=%zz", noParse},
		{"a broken escape in another parameter", "utm=%zz", noParse},
		{"repeated", "repo=a/b&repo=c/d", notOneRepo},
		{"one over the bound", "repo=octo/" + strings.Repeat("r", maxRepository+1-len("octo/")), notOneRepo},
		{"over the bound", "repo=octo/" + strings.Repeat("r", maxRepository), notOneRepo},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reader := &stubMeasured{merges: twoRepositories()}
			rec := serveAutonomy(reader, &stubAutoMerge{enabled: true}, "/api/autonomy?"+tt.query)
			if rec.Code != http.StatusBadRequest || strings.TrimSpace(rec.Body.String()) != tt.want {
				t.Errorf("answer = %d %q, want %d %q", rec.Code, rec.Body.String(), http.StatusBadRequest, tt.want)
			}
			if reader.limit != 0 {
				t.Errorf("limit asked = %d, want the store left unread", reader.limit)
			}
		})
	}

	atBound := "octo/" + strings.Repeat("r", maxRepository-len("octo/"))
	if rec := serveAutonomy(&stubMeasured{}, nil, "/api/autonomy?repo="+atBound); rec.Code != http.StatusOK {
		t.Errorf("a %d-character repository = %d, want %d", len(atBound), rec.Code, http.StatusOK)
	}
}

// ratedMerges is the window the auto-merge rule reads: ten measured merges of
// one PR each, ownerPerPR of them rewritten.
func ratedMerges(at time.Time, ownerPerPR int64) []dispatcher.Sample {
	measured := make([]dispatcher.Sample, 0, dispatcher.TrailingPRs)
	for i := range dispatcher.TrailingPRs {
		measured = append(measured, dispatcher.Sample{
			PR: 140 - i, MergedAt: at.Add(-time.Duration(i) * time.Hour),
			Rewrite: dispatcher.Rewrite{AgentLines: 1_000, OwnerLines: ownerPerPR},
		})
	}
	return measured
}

// readAutoMerge reads the auto-merge state through the autonomy endpoint, which
// is where the page reads it.
func readAutoMerge(t *testing.T, merges []dispatcher.Sample, switches AutoMergeStore) map[string]any {
	t.Helper()
	rec := getAutonomyAt(t, &stubMeasured{merges: merges}, switches, "/api/autonomy")
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	state, ok := body["auto_merge"].(map[string]any)
	if !ok {
		t.Fatalf("auto_merge = %v, want the switch and the rate", body["auto_merge"])
	}
	return state
}

// TestAutonomyReportsTheAutoMergeSwitchAndTheRatesVerdict asserts the autonomy
// view carries the owner's switch beside the rate's verdict, the two facts the
// effective state needs both of, with the bar read from the dispatcher's own
// constant (AC1).
func TestAutonomyReportsTheAutoMergeSwitchAndTheRatesVerdict(t *testing.T) {
	state := readAutoMerge(t, ratedMerges(measuredAt(), 98), &stubAutoMerge{enabled: true})

	if got, want := slices.Sorted(maps.Keys(state)), []string{"effective", "limit", "rate_allows", "rate_value", "reason", "switch_on", "window"}; !slices.Equal(got, want) {
		t.Fatalf("auto_merge keys = %v, want %v", got, want)
	}
	if state["switch_on"] != true || state["rate_allows"] != true || state["effective"] != true {
		t.Errorf("auto_merge = %v, want the switch on and the 9.8%% rate allowing it", state)
	}
	if state["rate_value"] != "9.8%" || state["limit"] != "10.0%" || state["window"] != 10.0 || state["reason"] != dispatcher.AutoMergeReasonOn {
		t.Errorf("auto_merge = %v, want the rule's own rate against its bar", state)
	}
}

// TestAutonomyReportsARateAboveTheBarAsNotAllowing asserts the two facts are
// separate: the owner's switch is on while the rate does not allow, so
// auto-merge is not effective (AC1).
func TestAutonomyReportsARateAboveTheBarAsNotAllowing(t *testing.T) {
	state := readAutoMerge(t, ratedMerges(measuredAt(), 101), &stubAutoMerge{enabled: true})

	if state["switch_on"] != true || state["rate_allows"] != false || state["effective"] != false {
		t.Errorf("auto_merge = %v, want the switch on and the rate refusing", state)
	}
	if state["rate_value"] != "10.1%" || state["reason"] != dispatcher.AutoMergeReasonRateAboveBar {
		t.Errorf("auto_merge = %v, want the rate with the reason it refused", state)
	}
}

// TestAutonomyReportsAnOffSwitchBesideARateThatAllows asserts the switch off is
// reported on its own, with the rate's verdict still read beside it (AC1).
func TestAutonomyReportsAnOffSwitchBesideARateThatAllows(t *testing.T) {
	state := readAutoMerge(t, ratedMerges(measuredAt(), 0), &stubAutoMerge{})

	if state["switch_on"] != false || state["effective"] != false {
		t.Errorf("auto_merge = %v, want the switch off and no effect", state)
	}
	if state["rate_allows"] != true || state["reason"] != dispatcher.AutoMergeReasonSwitchOff {
		t.Errorf("auto_merge = %v, want the rate still allowing it and the switch named as the reason", state)
	}
}

// TestAutonomyReportsASwitchItCannotReadAsOff asserts a switch that cannot be
// read is reported off with the reason, never on (AC4).
func TestAutonomyReportsASwitchItCannotReadAsOff(t *testing.T) {
	var log strings.Builder
	mux := newMux(Deps{
		SPA:       fstest.MapFS{},
		Logger:    slog.New(slog.NewTextHandler(&log, nil)),
		Measured:  &stubMeasured{merges: ratedMerges(measuredAt(), 0)},
		AutoMerge: &stubAutoMerge{enabled: true, readErr: errors.New("firestore unreachable")},
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/autonomy", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	state, ok := body["auto_merge"].(map[string]any)
	if !ok {
		t.Fatalf("auto_merge = %v, want the switch and the rate", body["auto_merge"])
	}
	if state["switch_on"] != false || state["effective"] != false || state["reason"] != dispatcher.AutoMergeReasonSwitchUnread {
		t.Errorf("auto_merge = %v, want the unread switch reported off with the reason", state)
	}
	if !strings.Contains(log.String(), "autoMergeSwitchUnread") {
		t.Errorf("log = %q, want the unread switch reported", log.String())
	}
}

// postAutoMerge posts body to the auto-merge endpoint under contentType, empty
// for a request carrying no content type at all.
func postAutoMerge(t *testing.T, mux *http.ServeMux, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/auto-merge", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestAutoMergeSetsTheSwitchItIsGiven asserts the endpoint sets the switch its
// store holds and answers the state it set (AC2).
func TestAutoMergeSetsTheSwitchItIsGiven(t *testing.T) {
	stored := &stubAutoMerge{enabled: true}
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), AutoMerge: stored})
	rec := postAutoMerge(t, mux, "application/json", `{"enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if stored.enabled || stored.setAt.IsZero() {
		t.Fatalf("stored = %v at %v, want off recorded", stored.enabled, stored.setAt)
	}
	if got, want := rec.Body.String(), "{\"enabled\":false}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// TestAutoMergeRefusesABodyNamingNoSetting asserts a request that does not say
// whether auto-merge is enabled is refused, so a body naming nothing never reads
// as off (AC3).
func TestAutoMergeRefusesABodyNamingNoSetting(t *testing.T) {
	stored := &stubAutoMerge{enabled: true}
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), AutoMerge: stored})
	for _, body := range []string{`{}`, `not json`, `{"autoMerge":false}`} {
		if rec := postAutoMerge(t, mux, "application/json", body); rec.Code != http.StatusBadRequest {
			t.Errorf("body %q = %d, want %d", body, rec.Code, http.StatusBadRequest)
		}
	}
	if !stored.enabled || !stored.setAt.IsZero() {
		t.Errorf("stored = %v at %v, want a refused body to write nothing", stored.enabled, stored.setAt)
	}
}

// TestAutoMergeRefusesANonJSONContentType asserts the write needs a JSON body,
// which a browser cannot send cross-origin without a preflight: a page that is
// not the surface cannot set the switch with the session IAP establishes (AC3).
func TestAutoMergeRefusesANonJSONContentType(t *testing.T) {
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), AutoMerge: &stubAutoMerge{}})
	for _, contentType := range []string{"", "text/plain", "application/x-www-form-urlencoded"} {
		if rec := postAutoMerge(t, mux, contentType, `{"enabled":false}`); rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("content type %q = %d, want %d", contentType, rec.Code, http.StatusUnsupportedMediaType)
		}
	}
}

// TestAutoMergeAnswersTheWriteAlone asserts the endpoint takes the write and
// nothing else: the state is read from the autonomy view, so a read of the
// switch's own path is no endpoint.
func TestAutoMergeAnswersTheWriteAlone(t *testing.T) {
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), AutoMerge: &stubAutoMerge{}})
	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/auto-merge", strings.NewReader(`{"enabled":false}`)))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
		}
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auto-merge", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestAutoMergeWithoutAStoreIsUnavailable asserts a local run with no store
// answers 503 rather than recording a switch nowhere.
func TestAutoMergeWithoutAStoreIsUnavailable(t *testing.T) {
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger()})
	if rec := postAutoMerge(t, mux, "application/json", `{"enabled":false}`); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestAutoMergeReportsAStoreItCannotWrite asserts a failed write is a 500 with
// the failure reported, and answers no state the switch did not take.
func TestAutoMergeReportsAStoreItCannotWrite(t *testing.T) {
	var log strings.Builder
	mux := newMux(Deps{
		SPA:       fstest.MapFS{},
		Logger:    slog.New(slog.NewTextHandler(&log, nil)),
		AutoMerge: &stubAutoMerge{setErr: errors.New("firestore unreachable")},
	})
	if rec := postAutoMerge(t, mux, "application/json", `{"enabled":false}`); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(log.String(), "autoMergeSwitchNotSet") {
		t.Fatalf("log = %q, want the failed write reported", log.String())
	}
}
