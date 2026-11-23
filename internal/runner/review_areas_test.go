package runner

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

// testAreas is a configuration the selection tests run against, so they do not
// depend on the paths review_areas.json holds.
var testAreas = mustParseReviewAreas([]byte(`{"areas": [
  {"name": "docs", "paths": ["docs/**", "**/*.md"], "prompt": "Read the docs.", "high_when_run": false},
  {"name": "sql", "paths": ["**/*.sql", "db/**"], "prompt": "Read the queries.", "models": ["opencode-go/a-model"], "high_when_run": true},
  {"name": "cafe", "paths": ["café/**", "my files/**"], "prompt": "Read the cafe.", "high_when_run": false}
]}`))

// section is one file's section of a unified diff, header first, with a hunk so
// the section reads as the compare API writes it.
func section(header string, meta ...string) string {
	lines := append([]string{"diff --git " + header}, meta...)
	return strings.Join(lines, "\n") + "\n@@ -1,2 +1,2 @@\n-old\n+new\n"
}

func plain(p string) string {
	return section("a/"+p+" b/"+p, "index 1111111..2222222 100644", "--- a/"+p, "+++ b/"+p)
}

func added(p string) string {
	return section("a/"+p+" b/"+p, "new file mode 100644", "index 0000000..2222222", "--- /dev/null", "+++ b/"+p)
}

func deleted(p string) string {
	return section("a/"+p+" b/"+p, "deleted file mode 100644", "index 1111111..0000000", "--- a/"+p, "+++ /dev/null")
}

func renamed(from, to string) string {
	return section("a/"+from+" b/"+to, "similarity index 90%", "rename from "+from, "rename to "+to, "--- a/"+from, "+++ b/"+to)
}

func areaNames(areas []ReviewArea) []string {
	var names []string
	for _, a := range areas {
		names = append(names, a.Name)
	}
	return names
}

func TestTouchedReviewAreas(t *testing.T) {
	for name, tt := range map[string]struct {
		diff string
		want []string
	}{
		"no file matches an area":           {plain("main.go") + plain("web/app.ts"), nil},
		"an empty diff":                     {"", nil},
		"a changed file":                    {plain("docs/a.txt"), []string{"docs"}},
		"an added file":                     {added("db/schema.txt"), []string{"sql"}},
		"a deleted file":                    {deleted("docs/gone.txt"), []string{"docs"}},
		"a rename into an area":             {renamed("main.go", "docs/main.txt"), []string{"docs"}},
		"a rename out of an area":           {renamed("docs/main.txt", "main.go"), []string{"docs"}},
		"a rename between two areas":        {renamed("docs/a.txt", "db/a.txt"), []string{"docs", "sql"}},
		"a rename inside no area":           {renamed("main.go", "cmd/main.go"), nil},
		"many files of one area":            {plain("docs/a.txt") + plain("docs/b.txt") + added("docs/c/d.txt") + plain("README.md"), []string{"docs"}},
		"a path in two areas":               {plain("db/notes.md"), []string{"docs", "sql"}},
		"configuration order, not diff":     {plain("db/a.txt") + plain("docs/a.txt"), []string{"docs", "sql"}},
		"a quoted header with a space":      {section(`"a/my files/a b.txt" "b/my files/a b.txt"`, "--- \"a/my files/a b.txt\"", "+++ \"b/my files/a b.txt\""), []string{"cafe"}},
		"an unquoted header with a space":   {section("a/my files/a b.txt b/my files/a b.txt"), []string{"cafe"}},
		"a quoted header with octal bytes":  {section(`"a/caf\303\251/x.txt" "b/caf\303\251/x.txt"`), []string{"cafe"}},
		"a quoted rename with octal bytes":  {section(`a/main.go "b/caf\303\251/x.txt"`, "rename from main.go", `rename to "caf\303\251/x.txt"`), []string{"cafe"}},
		"a quote and a backslash in a name": {section(`"a/db/q\"\\x.txt" "b/db/q\"\\x.txt"`), []string{"sql"}},
		"a body line that looks like a header": {
			"diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-rename to docs/x.txt\n+--- a/docs/y.txt\n",
			nil,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := areaNames(touchedReviewAreas(testAreas, tt.diff))
			if !slices.Equal(got, tt.want) {
				t.Fatalf("areas = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTouchedReviewAreasReturnsTheAreasConfiguration(t *testing.T) {
	got := touchedReviewAreas(testAreas, plain("db/q.sql"))
	if len(got) != 1 {
		t.Fatalf("areas = %v, want sql", areaNames(got))
	}
	a := got[0]
	if a.Prompt != "Read the queries." || !a.HighWhenRun || !slices.Equal(a.Models, []string{"opencode-go/a-model"}) {
		t.Fatalf("area = %+v, want the configured prompt, models and high_when_run", a)
	}
	a.Models[0], a.Paths[0] = "changed", "changed"
	if testAreas[1].Models[0] != "opencode-go/a-model" || testAreas[1].Paths[0] != "**/*.sql" {
		t.Fatal("changing a returned area changed the configuration")
	}
}

func TestTouchedReviewAreasReadsTheEmbeddedConfiguration(t *testing.T) {
	for name, tt := range map[string]struct {
		diff string
		want []string
	}{
		"a Go file in no area":            {plain("internal/runner/loop.go"), nil},
		"a workflow":                      {plain(".github/workflows/run.yml"), []string{"infra"}},
		"a Dockerfile in a subdirectory":  {plain("tools/agent/Dockerfile"), []string{"infra"}},
		"the identity file in two":        {plain("infra/identity.tf"), []string{"infra", "security"}},
		"the runner's pr review code":     {plain("internal/runner/pr_review.go") + plain("cmd/runner/pr_review.go"), nil},
		"the secrets file":                {plain("infra/secrets.tf"), []string{"infra", "security"}},
		"the provider limits":             {plain("internal/providers/providers.json"), []string{"cloud"}},
		"the webhook handler":             {plain("internal/webhook/handler.go"), []string{"security"}},
		"a store file":                    {plain("internal/store/runs.go"), []string{"data"}},
		"the Firestore indexes":           {plain("infra/firestore.tf"), []string{"data", "infra"}},
		"a web type file":                 {plain("web/src/config/types.ts"), []string{"data"}},
		"a web file that is not a type":   {plain("web/src/config/switch.tsx"), nil},
		"a surface file that is not auth": {plain("cmd/surface/config.go"), nil},
		"the surface auth":                {plain("cmd/surface/auth.go"), []string{"security"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := areaNames(TouchedReviewAreas(tt.diff))
			if !slices.Equal(got, tt.want) {
				t.Fatalf("areas = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEmbeddedReviewAreas(t *testing.T) {
	areas, err := parseReviewAreas(reviewAreasJSON)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(areaNames(areas), []string{"data", "infra", "cloud", "security"}) {
		t.Fatalf("areas = %v, want data, infra, cloud, security in that order", areaNames(areas))
	}
	var file struct {
		Areas []reviewAreaFile `json:"areas"`
	}
	if err := json.Unmarshal(reviewAreasJSON, &file); err != nil {
		t.Fatal(err)
	}
	for _, f := range file.Areas {
		if !slices.Equal(f.Models, []string{"opencode-go/claude-haiku-5-5"}) {
			t.Errorf("%s writes models %v, want the model it runs spelled out", f.Name, f.Models)
		}
	}
	for _, a := range areas {
		if want := a.Name == "cloud" || a.Name == "security"; a.HighWhenRun != want {
			t.Errorf("%s high_when_run = %v, want %v", a.Name, a.HighWhenRun, want)
		}
		if !slices.Equal(a.Models, []string{"opencode-go/claude-haiku-5-5"}) {
			t.Errorf("%s models = %v, want the default", a.Name, a.Models)
		}
	}
}

func TestParseReviewAreasRejectsAnInvalidFile(t *testing.T) {
	const area = `{"name": "x", "paths": ["a/**"], "prompt": "Look.", "high_when_run": false}`
	for name, tt := range map[string]struct {
		doc, want string
	}{
		"an unknown field in an area":    {`{"areas": [{"name": "x", "paths": ["a"], "prompt": "p", "high_when_run": false, "extra": 1}]}`, `"x"`},
		"an unknown top-level field":     {`{"areas": [` + area + `], "extra": 1}`, "unknown field"},
		"not JSON":                       {`{"areas": [`, "decoding review areas"},
		"no areas":                       {`{"areas": []}`, "no area"},
		"a duplicate area":               {`{"areas": [` + area + `, ` + area + `]}`, `"x"`},
		"an empty path list":             {`{"areas": [{"name": "x", "paths": [], "prompt": "p", "high_when_run": false}]}`, `"x"`},
		"no path list":                   {`{"areas": [{"name": "x", "prompt": "p", "high_when_run": false}]}`, `"x"`},
		"an empty prompt":                {`{"areas": [{"name": "x", "paths": ["a"], "prompt": "", "high_when_run": false}]}`, `"x"`},
		"a blank prompt":                 {`{"areas": [{"name": "x", "paths": ["a"], "prompt": " \n", "high_when_run": false}]}`, `"x"`},
		"an empty model list":            {`{"areas": [{"name": "x", "paths": ["a"], "prompt": "p", "high_when_run": false, "models": []}]}`, `"x"`},
		"a malformed model":              {`{"areas": [{"name": "x", "paths": ["a"], "prompt": "p", "high_when_run": false, "models": ["nomodel"]}]}`, `"x"`},
		"a repeated model":               {`{"areas": [{"name": "x", "paths": ["a"], "prompt": "p", "high_when_run": false, "models": ["a/b", "a/b"]}]}`, `"x"`},
		"an empty path":                  {`{"areas": [{"name": "x", "paths": [""], "prompt": "p", "high_when_run": false}]}`, `"x"`},
		"a rooted path":                  {`{"areas": [{"name": "x", "paths": ["/a"], "prompt": "p", "high_when_run": false}]}`, `"x"`},
		"a trailing slash":               {`{"areas": [{"name": "x", "paths": ["a/"], "prompt": "p", "high_when_run": false}]}`, `"x"`},
		"a malformed glob":               {`{"areas": [{"name": "x", "paths": ["a/[b"], "prompt": "p", "high_when_run": false}]}`, `"x"`},
		"a free model with no smoke run": {`{"areas": [{"name": "x", "paths": ["a"], "prompt": "p", "high_when_run": false, "models": ["opencode-go/unproven-model:free"]}]}`, `"x"`},
		"a double star beside text":      {`{"areas": [{"name": "x", "paths": ["a/**.go"], "prompt": "p", "high_when_run": false}]}`, `"x"`},
		"a trailing double star glued":   {`{"areas": [{"name": "x", "paths": ["a**"], "prompt": "p", "high_when_run": false}]}`, `"x"`},
		"a leading double star glued":    {`{"areas": [{"name": "x", "paths": ["**.go"], "prompt": "p", "high_when_run": false}]}`, `"x"`},
		"no high_when_run":               {`{"areas": [{"name": "x", "paths": ["a"], "prompt": "p"}]}`, `"x"`},
		"a null high_when_run":           {`{"areas": [{"name": "x", "paths": ["a"], "prompt": "p", "high_when_run": null}]}`, `"x"`},
		"an area with no name":           {`{"areas": [{"paths": ["a"], "prompt": "p", "high_when_run": false}]}`, "review area 1"},
		"a name that is not a keyword":   {`{"areas": [{"name": "My Area", "paths": ["a"], "prompt": "p", "high_when_run": false}]}`, `"My Area"`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseReviewAreas([]byte(tt.doc))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseReviewAreas error = %v, want one naming %s", err, tt.want)
			}
		})
	}
}

func TestParseReviewAreasDefaultsTheModels(t *testing.T) {
	areas, err := parseReviewAreas([]byte(`{"areas": [
	  {"name": "a", "paths": ["a"], "prompt": "p", "high_when_run": false},
	  {"name": "b", "paths": ["b"], "prompt": "p", "high_when_run": false, "models": ["opencode-go/one", "opencode-go/two"]}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(areas[0].Models, []string{"opencode-go/claude-haiku-5-5"}) || areas[0].HighWhenRun {
		t.Errorf("a = %+v, want the default model and not high-when-run", areas[0])
	}
	if !slices.Equal(areas[1].Models, []string{"opencode-go/one", "opencode-go/two"}) {
		t.Errorf("b models = %v, want the two configured", areas[1].Models)
	}
}

func TestMatchPath(t *testing.T) {
	for name, tt := range map[string]struct {
		pattern, file string
		want          bool
	}{
		"a literal":                           {"infra/identity.tf", "infra/identity.tf", true},
		"a literal is the whole path":         {"infra/identity.tf", "x/infra/identity.tf", false},
		"a literal is not a directory prefix": {"cmd/surface", "cmd/surface/auth.go", false},
		"a star stays in a segment":           {"cmd/*.go", "cmd/a/b.go", false},
		"a star in a segment":                 {"cmd/surface/auth*.go", "cmd/surface/auth_test.go", true},
		"a trailing double star":              {"infra/**", "infra/a/b.tf", true},
		"a trailing double star needs a file": {"infra/**", "infra", false},
		"a prefix is not a directory":         {"infra/**", "infra.md", false},
		"a leading double star":               {"**/Dockerfile", "Dockerfile", true},
		"a leading double star, nested":       {"**/Dockerfile", "tools/agent/Dockerfile", true},
		"a middle double star":                {"web/src/**/types.ts", "web/src/config/types.ts", true},
		"a middle double star, none":          {"web/src/**/types.ts", "web/src/types.ts", true},
		"a middle double star, a miss":        {"web/src/**/types.ts", "web/src/config/type.ts", false},
		"case matters":                        {"**/Dockerfile", "dockerfile", false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := matchPath(tt.pattern, tt.file); got != tt.want {
				t.Fatalf("matchPath(%q, %q) = %v, want %v", tt.pattern, tt.file, got, tt.want)
			}
		})
	}
}

func TestDiffPathsFallsBackToTheMarkerLines(t *testing.T) {
	for name, tt := range map[string]struct {
		diff string
		want []string
	}{
		"an unreadable header with markers":       {"diff --git garbage\n--- a/docs/a.txt\t\n+++ b/docs/a.txt\t\n@@ -1 +1 @@\n-x\n+y\n", []string{"docs/a.txt", "docs/a.txt"}},
		"an unreadable header, a deletion":        {"diff --git garbage\n--- a/docs/a b.txt\t\n+++ /dev/null\n@@ -1 +0,0 @@\n-x\n", []string{"docs/a b.txt"}},
		"quoted marks followed by a tab":          {"diff --git garbage\n--- \"a/docs/a b.txt\"\t\n+++ \"b/docs/a b.txt\"\t\n@@ -1 +1 @@\n-x\n+y\n", []string{"docs/a b.txt", "docs/a b.txt"}},
		"an unreadable header, quoted marks":      {"diff --git garbage\n--- /dev/null\n+++ \"b/caf\\303\\251.txt\"\n@@ -0,0 +1 @@\n+x\n", []string{"café.txt"}},
		"a header that reads beats markers":       {"diff --git a/x b/x\n--- a/y\n+++ b/y\n@@ -1 +1 @@\n-x\n+y\n", []string{"x"}},
		"a binary file has only its header":       {"diff --git a/img.png b/img.png\nindex 1..2 100644\nBinary files a/img.png and b/img.png differ\n", []string{"img.png"}},
		"a mode change has only its header":       {"diff --git a/run.sh b/run.sh\nold mode 100644\nnew mode 100755\n", []string{"run.sh"}},
		"text before the first header":            {"From abc\nrename to docs/x\ndiff --git a/x b/x\n", []string{"x"}},
		"a hunk line that looks like a marker":    {"diff --git garbage\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n--- a/docs/z.txt\n+y\n", []string{"main.go", "main.go"}},
		"a directory ending in a space and b":     {"diff --git a/docs/x b/y.txt b/docs/x b/y.txt\n", []string{"docs/x b/y.txt"}},
		"a quoted header not followed by a space": {"diff --git \"a/docs/x\"Xb/docs/x\n", nil},
		"a header naming two different paths":     {"diff --git a/x b/y\n", nil},
		"a copy has both sides":                   {"diff --git a/a b/b\ncopy from a\ncopy to b\n", []string{"a", "b"}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := diffPaths(tt.diff); !slices.Equal(got, tt.want) {
				t.Fatalf("diffPaths = %q, want %q", got, tt.want)
			}
		})
	}
}

// patternSamples gives each embedded pattern a path it must match: a real file
// where the repository has one, so a typo or a rename turns a test red, and a
// synthetic one for a pattern nothing here matches yet.
var patternSamples = map[string]struct {
	path      string
	synthetic bool
}{
	"**/*.sql":                          {"db/query.sql", true},
	"**/migrations/**":                  {"db/migrations/0001.txt", true},
	"**/Dockerfile":                     {"tools/agent/Dockerfile", false},
	"**/Dockerfile.*":                   {"tools/agent/Dockerfile.dev", true},
	".github/actions/**":                {".github/actions/golangci-lint/action.yml", false},
	".github/workflows/**":              {".github/workflows/run.yml", false},
	"cmd/runner/auto_merge.go":          {"cmd/runner/auto_merge.go", false},
	"cmd/runner/egress_proxy.go":        {"cmd/runner/egress_proxy.go", false},
	"cmd/surface/auth*.go":              {"cmd/surface/auth.go", false},
	"cmd/webhook/**":                    {"cmd/webhook/main.go", false},
	"infra/**":                          {"infra/apis.tf", false},
	"infra/firestore.tf":                {"infra/firestore.tf", false},
	"infra/identity.tf":                 {"infra/identity.tf", false},
	"infra/secrets.tf":                  {"infra/secrets.tf", false},
	"infra/settings.auto.tfvars":        {"infra/settings.auto.tfvars", false},
	"internal/dispatcher/ruleset.go":    {"internal/dispatcher/ruleset.go", false},
	"internal/dispatcher/secrets*.go":   {"internal/dispatcher/secrets.go", false},
	"internal/linear/oauth*.go":         {"internal/linear/oauth.go", false},
	"internal/providers/providers.json": {"internal/providers/providers.json", false},
	"internal/runner/agent_egress.go":   {"internal/runner/agent_egress.go", false},
	"internal/runner/egress_proxy.go":   {"internal/runner/egress_proxy.go", false},
	"internal/runner/fixroundstate.go":  {"internal/runner/fixroundstate.go", false},
	"internal/runner/range_secrets*.go": {"internal/runner/range_secrets.go", false},
	"internal/runner/record.go":         {"internal/runner/record.go", false},
	"internal/store/**":                 {"internal/store/breaker.go", false},
	"internal/store/ruleset.go":         {"internal/store/ruleset.go", false},
	"internal/webhook/**":               {"internal/webhook/handler.go", false},
	"web/src/**/types.ts":               {"web/src/config/types.ts", false},
}

func TestEmbeddedReviewAreasPatternsMatchTheirSamples(t *testing.T) {
	used := map[string]bool{}
	for _, a := range embeddedReviewAreas {
		for _, pattern := range a.Paths {
			used[pattern] = true
			sample, ok := patternSamples[pattern]
			switch {
			case !ok:
				t.Errorf("%s: pattern %q has no sample path", a.Name, pattern)
			case !matchPath(pattern, sample.path):
				t.Errorf("%s: pattern %q does not match its sample %q", a.Name, pattern, sample.path)
			case !a.matches(sample.path):
				t.Errorf("%s: area does not match its pattern's sample %q", a.Name, sample.path)
			}
			if ok && !sample.synthetic {
				if _, err := os.Stat("../../" + sample.path); err != nil {
					t.Errorf("%s: sample %q is not a file in the repository: %v", a.Name, sample.path, err)
				}
			}
		}
	}
	for pattern := range patternSamples {
		if !used[pattern] {
			t.Errorf("sample for %q matches no configured pattern", pattern)
		}
	}
}
