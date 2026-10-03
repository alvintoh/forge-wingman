package providers

import (
	"strings"
	"testing"
	"time"
)

func TestNewDefinitionReadsAWellFormedPlan(t *testing.T) {
	got, err := NewDefinition(" Go Plan ", "15.50", []string{"https://vendor.example/pricing  #plans"},
		[]string{"opencode:glm-5"}, "hard-stop")
	if err != nil {
		t.Fatal(err)
	}
	want := Definition{
		Name:           "Go Plan",
		MonthlyPrice:   15_500_000,
		Pages:          []Locator{{URL: "https://vendor.example/pricing", Selector: "#plans"}},
		Harnesses:      []HarnessPair{{Harness: "opencode", Model: "glm-5"}},
		LimitBehaviour: LimitHardStop,
	}
	if got.Name != want.Name || got.MonthlyPrice != want.MonthlyPrice || got.LimitBehaviour != want.LimitBehaviour ||
		len(got.Pages) != 1 || got.Pages[0] != want.Pages[0] || len(got.Harnesses) != 1 || got.Harnesses[0] != want.Harnesses[0] {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestNewDefinitionRefusesWhatItCannotStore(t *testing.T) {
	for name, tt := range map[string]struct {
		call func() (Definition, error)
		want string
	}{
		"empty name": {func() (Definition, error) { return NewDefinition(" ", "15", nil, nil, "hard-stop") }, "name is empty"},
		"zero price": {func() (Definition, error) { return NewDefinition("p", "0", nil, nil, "hard-stop") }, "not a positive amount"},
		"infinite price": {func() (Definition, error) { return NewDefinition("p", "+Inf", nil, nil, "hard-stop") },
			"not a positive amount"},
		"not a number": {func() (Definition, error) { return NewDefinition("p", "cheap", nil, nil, "hard-stop") },
			"not a positive amount"},
		"unknown limit": {func() (Definition, error) { return NewDefinition("p", "15", nil, nil, "soft") }, "limit behaviour"},
		"http page": {func() (Definition, error) {
			return NewDefinition("p", "15", []string{"http://v.example"}, nil, "hard-stop")
		}, "not an https URL"},
		"page with no host": {func() (Definition, error) { return NewDefinition("p", "15", []string{"https:///x"}, nil, "hard-stop") },
			"not an https URL"},
		"page URL too long": {func() (Definition, error) {
			return NewDefinition("p", "15", []string{"https://v.example/" + strings.Repeat("a", maxURLBytes)}, nil, "hard-stop")
		}, "longer than"},
		"harness without a model": {func() (Definition, error) {
			return NewDefinition("p", "15", nil, []string{"opencode:"}, "hard-stop")
		}, "not harness:model"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := tt.call(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestParseLimitBehaviourAcceptsEachBehaviourAndRefusesOthers(t *testing.T) {
	for _, s := range []string{"hard-stop", "can-spend-past", "unknown"} {
		if b, err := ParseLimitBehaviour(s); err != nil || string(b) != s {
			t.Errorf("%q: behaviour %q, err %v", s, b, err)
		}
	}
	if _, err := ParseLimitBehaviour("soft"); err == nil {
		t.Error("soft was accepted")
	}
}

func TestParseVerdictRefusesTheRunLevelUnknown(t *testing.T) {
	if _, err := ParseVerdict("unknown"); err == nil {
		t.Fatal("unknown was accepted as a recordable verdict")
	}
	if v, err := ParseVerdict("restricted"); err != nil || v != VerdictRestricted {
		t.Fatalf("verdict %q, err %v", v, err)
	}
}

func TestNewVerdictNoteRefusesAnImpossibleDateAndANonHTTPSSource(t *testing.T) {
	if _, err := NewVerdictNote("allowed", "w", "https://v.example/terms", "2026-02-30"); err == nil {
		t.Fatal("2026-02-30 was accepted as a read date")
	}
	if _, err := NewVerdictNote("allowed", "w", "javascript:alert(1)", "2026-09-30"); err == nil {
		t.Fatal("a non-https source was accepted")
	}
}

func TestCapTextNeverSplitsARuneAndReplacesInvalidUTF8(t *testing.T) {
	if got := capText("a€b", 2); got != "a" {
		t.Fatalf("capText = %q, want a: the cap falls inside the three-byte €", got)
	}
	if got := capText("a\xffb", 10); got != "a\uFFFDb" {
		t.Fatalf("capText = %q, want the invalid byte replaced", got)
	}
}

func TestNewReplyRefusesEmptyText(t *testing.T) {
	if _, err := NewReply("  ", time.Time{}); err == nil {
		t.Fatal("an empty reply was accepted")
	}
}
