package modelprobe

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const cannedRoster = `opencode/big-pickle
{
  "id": "big-pickle",
  "cost": {
    "input": 0,
    "output": 0,
    "cache": {
      "read": 0
    }
  }
}
opencode/paid-model
{
  "id": "paid-model",
  "cost": {
    "input": 0.5,
    "output": 2
  }
}
opencode/no-cost-block
{
  "id": "no-cost-block"
}
opencode/half-priced-free
{
  "id": "half-priced-free",
  "cost": {
    "input": 0,
    "output": 1
  }
}
opencode/kimi-free
{
  "id": "kimi-free",
  "cost": {
    "input": 0,
    "output": 0
  }
}
opencode-go/glm-free
{
  "id": "glm-free",
  "cost": {
    "input": 0,
    "output": 0
  }
}
`

func TestFreeModelsAreThoseCostingNothingNotThoseNamedFree(t *testing.T) {
	roster, err := ParseRoster([]byte(cannedRoster))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"opencode/big-pickle", "opencode/kimi-free"}
	if got := FreeModels(roster); !slices.Equal(got, want) {
		t.Fatalf("FreeModels = %v, want %v (no suffix rule, no cost-less entry, no other provider)", got, want)
	}
}

func TestParseRosterRejectsATruncatedEntry(t *testing.T) {
	if _, err := ParseRoster([]byte("opencode/a\n{\n  \"id\": \"a\"\n")); err == nil {
		t.Fatal("ParseRoster accepted an object that never closes")
	}
}

func TestListRosterReadsTheBinaryOutput(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "opencode")
	script := "#!/bin/sh\n[ \"$1 $2 $3\" = \"models opencode --verbose\" ] || exit 2\ncat <<'EOF'\n" + cannedRoster + "EOF\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	roster, err := ListRoster(context.Background(), bin)
	if err != nil {
		t.Fatal(err)
	}
	if len(roster) != 6 {
		t.Fatalf("roster = %d models, want 6", len(roster))
	}
	if _, err := ListRoster(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("ListRoster succeeded with no binary")
	}
}

func TestParseRosterMarksAModelFreeOnlyWhenBothCostsAreExactlyZero(t *testing.T) {
	for name, cost := range map[string]string{
		"input priced, output zero": `{"input": 1, "output": 0}`,
		"negative input":            `{"input": -1, "output": 0}`,
		"negative output":           `{"input": 0, "output": -1}`,
		"output missing":            `{"input": 0}`,
		"input missing":             `{"output": 0}`,
		"empty cost block":          `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			roster, err := ParseRoster([]byte("opencode/m\n{\n  \"cost\": " + cost + "\n}\n"))
			if err != nil {
				t.Fatal(err)
			}
			if len(roster) != 1 || roster[0].Free {
				t.Fatalf("roster = %+v, want one model that is not free", roster)
			}
		})
	}
}

func TestParseRosterRejectsAnObjectWithNoModelLine(t *testing.T) {
	if _, err := ParseRoster([]byte("{\n  \"id\": \"a\"\n}\n")); err == nil {
		t.Fatal("ParseRoster accepted an object with no model line before it")
	}
}

func TestParseRosterTakesOnlyAWholeLineAsTheModelLine(t *testing.T) {
	roster, err := ParseRoster([]byte("opencode/a\nopencode/b is deprecated\n{\n  \"cost\": {\"input\": 0, \"output\": 0}\n}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(roster) != 1 || roster[0].ID != "opencode/a" {
		t.Fatalf("roster = %+v, want the object attributed to opencode/a", roster)
	}
}
