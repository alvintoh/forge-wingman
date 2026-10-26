package dispatcher

import "testing"

func envOf(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func TestConcurrencySettingsReadsEverySetting(t *testing.T) {
	l, tu, err := ConcurrencySettings(envOf(map[string]string{
		"WINGMAN_PLATFORM_CAP": " 8 ", "WINGMAN_LARGE_CAP": "2", "WINGMAN_REVIEW_WIP": "4",
		"WINGMAN_STABLE_RUNS": "3", "WINGMAN_RISE_WITHIN": " 1.1 ", "WINGMAN_HALVE_BEYOND": "1.8",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if want := (Limits{PlatformCap: 8, LargeCap: 2, ReviewWIP: 4}); l != want {
		t.Fatalf("limits = %+v, want %+v", l, want)
	}
	if want := (Tuning{StableRuns: 3, RiseWithin: 1.1, HalveBeyond: 1.8}); tu != want {
		t.Fatalf("tuning = %+v, want %+v", tu, want)
	}
}

func TestConcurrencySettingsLeavesUnsetOnesToTheirDefaults(t *testing.T) {
	l, tu, err := ConcurrencySettings(envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	if l != (Limits{}) || tu != (Tuning{}) {
		t.Fatalf("limits %+v tuning %+v, want zero so the defaults apply", l, tu)
	}
	if got, want := l.OrDefault(), (Limits{PlatformCap: 20, LargeCap: 1, ReviewWIP: 3, RepoCap: 5}); got != want {
		t.Fatalf("filled limits = %+v, want %+v", got, want)
	}
}

// TestConcurrencySettingsIgnoresALeftoverDailyRunCap: a WINGMAN_DAILY_RUNS still
// set in the environment is not read, even when it is not a number.
func TestConcurrencySettingsIgnoresALeftoverDailyRunCap(t *testing.T) {
	if _, _, err := ConcurrencySettings(envOf(map[string]string{"WINGMAN_DAILY_RUNS": "many"})); err != nil {
		t.Fatalf("a leftover WINGMAN_DAILY_RUNS failed the read: %v", err)
	}
}

func TestConcurrencySettingsRefusesUnusableValues(t *testing.T) {
	for name, kv := range map[string]map[string]string{
		"a zero cap":                              {"WINGMAN_PLATFORM_CAP": "0"},
		"a negative cap":                          {"WINGMAN_LARGE_CAP": "-1"},
		"a cap that is not a number":              {"WINGMAN_REVIEW_WIP": "many"},
		"a fractional count":                      {"WINGMAN_STABLE_RUNS": "2.5"},
		"a count beyond the int range":            {"WINGMAN_REVIEW_WIP": "99999999999999999999"},
		"a zero rise band":                        {"WINGMAN_RISE_WITHIN": "0"},
		"a rise band that is not a number":        {"WINGMAN_RISE_WITHIN": "NaN"},
		"an infinite fall band":                   {"WINGMAN_HALVE_BEYOND": "Inf"},
		"a fall band under the default rise band": {"WINGMAN_HALVE_BEYOND": "1.2"},
		"a rise band over the default fall band":  {"WINGMAN_RISE_WITHIN": "2"},
		"equal bands":                             {"WINGMAN_RISE_WITHIN": "1.4", "WINGMAN_HALVE_BEYOND": "1.4"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := ConcurrencySettings(envOf(kv)); err == nil {
				t.Fatalf("%v was accepted", kv)
			}
		})
	}
}

func TestTwoStageIsOnOnlyForOn(t *testing.T) {
	for name, tt := range map[string]struct {
		value string
		want  bool
	}{
		"on":              {"on", true},
		"ON with padding": {"  On ", true},
		"unset":           {"", false},
		"off":             {"off", false},
		"anything else":   {"someday", false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := TwoStage(envOf(map[string]string{"WINGMAN_TWO_STAGE": tt.value})); got != tt.want {
				t.Fatalf("TwoStage = %v for %q, want %v", got, tt.value, tt.want)
			}
		})
	}
}
