package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/option"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// fakeObject is one object the fake bucket lists and serves.
type fakeObject struct {
	name    string
	created time.Time
	body    string
	// size is what the listing reports, the body's length when zero.
	size int
}

// fakeProgressBucket serves a listing and the media of objects, recording the
// prefix and fields each listing asked for.
type fakeProgressBucket struct {
	mu       sync.Mutex
	objects  []fakeObject
	prefixes []string
	fields   []string
	listFail bool
}

func (f *fakeProgressBucket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.HasSuffix(r.URL.Path, "/b/b/o") {
		if f.listFail {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		prefix, offset := r.URL.Query().Get("prefix"), r.URL.Query().Get("startOffset")
		f.prefixes = append(f.prefixes, prefix)
		f.fields = append(f.fields, r.URL.Query().Get("fields"))
		items := []map[string]any{}
		for _, o := range f.objects {
			if !strings.HasPrefix(o.name, prefix) || o.name < offset {
				continue
			}
			size := o.size
			if size == 0 {
				size = len(o.body)
			}
			items = append(items, map[string]any{"bucket": "b", "name": o.name,
				"timeCreated": o.created.Format(time.RFC3339Nano), "size": strconv.Itoa(size)})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
		return
	}
	for _, o := range f.objects {
		if strings.HasSuffix(r.URL.Path, "/"+o.name) {
			_, _ = fmt.Fprint(w, o.body)
			return
		}
	}
	http.NotFound(w, r)
}

func progressStore(t *testing.T, fake *fakeProgressBucket) *Progresses {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	client, err := storage.NewClient(context.Background(),
		option.WithEndpoint(srv.URL+"/storage/v1/"), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return NewProgresses(client, "b")
}

var progressAt = time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)

// progressName names an object in folder written second seconds into the
// window that ends at progressAt, as the writer names it.
func progressName(folder string, second int, id string) string {
	at := progressAt.Add(-2 * time.Hour).Add(time.Duration(second) * time.Second)
	return fmt.Sprintf("progress/%s/%010d-%s.json", folder, at.Unix(), id)
}

func progressRun() Running {
	return Running{RunID: "run-7", Attempt: 1, Stage: "build"}
}

func TestProgressReadsTheNewestObjectByCreationTime(t *testing.T) {
	// The name the writer chooses sorts the older object last, so only the
	// creation time picks the newer one.
	fake := &fakeProgressBucket{objects: []fakeObject{
		{name: progressName("run-7-a1-build", 1, "z"), created: progressAt,
			body: `{"phase":"plan","steps":3,"lines_added":1,"lines_removed":1}`},
		{name: progressName("run-7-a1-build", 2, "a"), created: progressAt.Add(-time.Minute),
			body: `{"phase":"plan","steps":1,"lines_added":0,"lines_removed":0}`},
	}}
	got, err := progressStore(t, fake).Progress(context.Background(), progressRun(), progressAt)
	if err != nil {
		t.Fatal(err)
	}
	want := Progress{Phase: runner.PhasePlan, Steps: 3, Lines: 2, Created: progressAt}
	if got == nil || got.Phase != want.Phase || got.Steps != want.Steps || got.Lines != want.Lines || !got.Created.Equal(want.Created) {
		t.Fatalf("progress = %+v, want %+v", got, want)
	}
	if fake.fields[0] == "" || !strings.Contains(fake.fields[0], "timeCreated") || strings.Contains(fake.fields[0], "metadata") {
		t.Fatalf("listing asked for fields %q, want only name, created and size", fake.fields[0])
	}
}

func TestProgressListsOnlyTheRunsOwnFolder(t *testing.T) {
	for _, tt := range []struct {
		name string
		run  Running
		want string
	}{
		{"a build-stage claim", Running{RunID: "run-7", Attempt: 1, Stage: "build"}, "progress/run-7-a1-build/"},
		{"a plan-stage claim", Running{RunID: "run-7", Attempt: 1, Stage: "plan"}, "progress/run-7-a1-plan/"},
		{"a single-stage claim", Running{RunID: "run-7", Attempt: 1}, "progress/run-7-a1-build/"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Attempt 10's object would match a prefix without the trailing slash.
			fake := &fakeProgressBucket{objects: []fakeObject{
				{name: progressName("run-7-a10-build", 1, "a"), created: progressAt, body: `{"steps":9,"lines_added":0,"lines_removed":0}`},
			}}
			got, err := progressStore(t, fake).Progress(context.Background(), tt.run, progressAt)
			if err != nil {
				t.Fatal(err)
			}
			if got != nil || len(fake.prefixes) != 1 || fake.prefixes[0] != tt.want {
				t.Fatalf("progress = %+v listing %v, want none listing %s", got, fake.prefixes, tt.want)
			}
		})
	}
}

func TestProgressSkipsAnOversizeObject(t *testing.T) {
	fake := &fakeProgressBucket{objects: []fakeObject{
		{name: progressName("run-7-a1-build", 1, "a"), created: progressAt.Add(-time.Minute), body: `{"steps":1,"lines_added":0,"lines_removed":0}`},
		{name: progressName("run-7-a1-build", 2, "a"), created: progressAt, body: `{"steps":2,"lines_added":0,"lines_removed":0}`, size: 4097},
	}}
	got, err := progressStore(t, fake).Progress(context.Background(), progressRun(), progressAt)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Steps != 1 {
		t.Fatalf("progress = %+v, want the older object under the size limit", got)
	}
}

func TestProgressRejectsABodyLongerThanItsListing(t *testing.T) {
	// A whole JSON object one byte over the limit, so only the size check refuses it.
	prefix := `{"steps":1,"lines_added":0,"lines_removed":0,"pad":"`
	oversizeBody := prefix + strings.Repeat("x", 4097-len(prefix)-2) + `"}`
	fake := &fakeProgressBucket{objects: []fakeObject{
		{name: progressName("run-7-a1-build", 1, "a"), created: progressAt, body: oversizeBody, size: 10},
	}}
	got, err := progressStore(t, fake).Progress(context.Background(), progressRun(), progressAt)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("progress = %+v, want none for a body over the limit", got)
	}
}

func TestProgressNamesNoneForAnInvalidRun(t *testing.T) {
	fake := &fakeProgressBucket{}
	got, err := progressStore(t, fake).Progress(context.Background(), Running{RunID: "../x", Stage: "build"}, progressAt)
	if err != nil || got != nil || len(fake.prefixes) != 0 {
		t.Fatalf("progress = %+v, err %v after %d listings, want none and no listing", got, err, len(fake.prefixes))
	}
}

func TestProgressReportsAFailedListing(t *testing.T) {
	_, err := progressStore(t, &fakeProgressBucket{listFail: true}).Progress(context.Background(), progressRun(), progressAt)
	if err == nil {
		t.Fatal("Progress succeeded on a failed listing")
	}
}

func TestDecodeProgressKeepsOnlyACompleteReport(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want Progress
	}{
		{"every count at its bound", `{"phase":"review","steps":1000000,"lines_added":400000,"lines_removed":600000}`,
			Progress{Phase: runner.PhaseReview, Steps: 1000000, Lines: 1000000, Created: progressAt}},
		{"an unknown phase", `{"phase":"hacking","steps":0,"lines_added":2,"lines_removed":1}`,
			Progress{Steps: 0, Lines: 3, Created: progressAt}},
		{"a phase of the wrong type", `{"phase":3,"steps":4,"lines_added":0,"lines_removed":0}`,
			Progress{Steps: 4, Created: progressAt}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := decodeProgress([]byte(tt.body), progressAt)
			if got == nil || *got != tt.want {
				t.Fatalf("progress = %+v, want %+v", got, tt.want)
			}
		})
	}
	for _, tt := range []struct{ name, body string }{
		{"not JSON", `not json`},
		{"a null body", `null`},
		{"an array", `[1]`},
		{"a missing count", `{"steps":1,"lines_added":1}`},
		{"a null count", `{"steps":null,"lines_added":1,"lines_removed":1}`},
		{"a count of the wrong type", `{"steps":"4","lines_added":1,"lines_removed":1}`},
		{"a fractional count", `{"steps":1,"lines_added":1.5,"lines_removed":1}`},
		{"a negative count", `{"steps":1,"lines_added":1,"lines_removed":-1}`},
		{"a count over a million", `{"steps":1000001,"lines_added":0,"lines_removed":0}`},
		{"lines summing over a million", `{"steps":1,"lines_added":600000,"lines_removed":400001}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := decodeProgress([]byte(tt.body), progressAt); got != nil {
				t.Fatalf("progress = %+v, want none", got)
			}
		})
	}
}

func TestProgressListsOnlyTheRecentWindow(t *testing.T) {
	// 250 objects named just over two hours ago would fill the 200 cap ahead
	// of the recent one, since names list oldest first.
	fake := &fakeProgressBucket{}
	for i := range 250 {
		at := progressAt.Add(-2*time.Hour - 5*time.Minute).Add(time.Duration(i) * time.Second)
		fake.objects = append(fake.objects, fakeObject{
			name:    fmt.Sprintf("progress/run-7-a1-build/%010d-a.json", at.Unix()),
			created: at, body: `{"steps":1,"lines_added":0,"lines_removed":0}`,
		})
	}
	recent := fakeObject{name: progressName("run-7-a1-build", 7000, "a"), created: progressAt.Add(-time.Minute),
		body: `{"steps":8,"lines_added":0,"lines_removed":0}`}
	b := progressStore(t, fake)
	t.Run("a recent object is read past the old ones", func(t *testing.T) {
		fake.objects = append(fake.objects, recent)
		defer func() { fake.objects = fake.objects[:250] }()
		got, err := b.Progress(context.Background(), progressRun(), progressAt)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || got.Steps != 8 {
			t.Fatalf("progress = %+v, want the recent object", got)
		}
	})
	t.Run("nothing in the window is no progress", func(t *testing.T) {
		got, err := b.Progress(context.Background(), progressRun(), progressAt)
		if err != nil {
			t.Fatal(err)
		}
		if got != nil {
			t.Fatalf("progress = %+v, want none", got)
		}
	})
}
