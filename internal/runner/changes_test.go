package runner

import (
	"errors"
	"slices"
	"testing"
)

func TestParsePorcelain(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want []string
	}{
		{"clean", "", nil},
		{"modified, added and untracked", " M cmd/surface/main.go\x00A  internal/v.go\x00?? notes.txt\x00",
			[]string{"cmd/surface/main.go", "internal/v.go", "notes.txt"}},
		{"deleted", " D old.go\x00", []string{"old.go"}},
		{"rename names both sides", "R  new.go\x00old.go\x00 M x.go\x00", []string{"new.go", "old.go", "x.go"}},
		{"spaces are not quoted", "?? a file.go\x00", []string{"a file.go"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePorcelain([]byte(tt.out))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("paths = %q, want %q", got, tt.want)
			}
		})
	}

	for _, bad := range []string{"M\x00", "XYZpath\x00", "R  new.go"} {
		if _, err := ParsePorcelain([]byte(bad)); !errors.Is(err, ErrPorcelain) {
			t.Errorf("ParsePorcelain(%q) err = %v, want ErrPorcelain", bad, err)
		}
	}
}

func TestParseShortstat(t *testing.T) {
	for _, tc := range []struct {
		out  string
		want DiffLines
	}{
		{" 2 files changed, 10 insertions(+), 3 deletions(-)\n", DiffLines{Added: 10, Removed: 3}},
		{" 1 file changed, 1 insertion(+)\n", DiffLines{Added: 1}},
		{" 1 file changed, 4 deletions(-)\n", DiffLines{Removed: 4}},
		{"", DiffLines{}},
	} {
		if got := parseShortstat([]byte(tc.out)); got != tc.want {
			t.Errorf("parseShortstat(%q) = %+v, want %+v", tc.out, got, tc.want)
		}
	}
}
