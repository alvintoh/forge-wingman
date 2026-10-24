package dispatcher

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ConcurrencySettings reads the optional limits and tuning the WINGMAN_*
// variables name. An unset one is zero, so its default applies; one that is set
// must be positive, and the fall band must lie above the rise band. The names
// are the keys infra/variables.tf allows in dispatcher_settings.
func ConcurrencySettings(getenv func(string) string) (Limits, Tuning, error) {
	var l Limits
	var t Tuning
	for _, s := range []struct {
		key  string
		into *int
	}{
		{"WINGMAN_PLATFORM_CAP", &l.PlatformCap},
		{"WINGMAN_LARGE_CAP", &l.LargeCap},
		{"WINGMAN_REVIEW_WIP", &l.ReviewWIP},
		{"WINGMAN_STABLE_RUNS", &t.StableRuns},
	} {
		if err := positiveInt(getenv, s.key, s.into); err != nil {
			return l, t, err
		}
	}
	for _, s := range []struct {
		key  string
		into *float64
	}{
		{"WINGMAN_RISE_WITHIN", &t.RiseWithin},
		{"WINGMAN_HALVE_BEYOND", &t.HalveBeyond},
	} {
		raw := strings.TrimSpace(getenv(s.key))
		if raw == "" {
			continue
		}
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil || !(f > 0) || math.IsInf(f, 0) {
			return l, t, fmt.Errorf("%s = %q is not a positive number", s.key, raw)
		}
		*s.into = f
	}
	if eff := t.OrDefault(); eff.HalveBeyond <= eff.RiseWithin {
		return l, t, errors.New("WINGMAN_HALVE_BEYOND must exceed WINGMAN_RISE_WITHIN")
	}
	return l, t, nil
}

// TwoStage reports whether WINGMAN_TWO_STAGE switches runs to separate plan and
// build stages: only "on", in any case, does.
func TwoStage(getenv func(string) string) bool {
	return strings.EqualFold(strings.TrimSpace(getenv("WINGMAN_TWO_STAGE")), "on")
}

// positiveInt reads a WINGMAN_ setting that must be a positive whole number,
// leaving into as it was for one the environment does not set.
func positiveInt(getenv func(string) string, key string, into *int) error {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return fmt.Errorf("%s = %q is not a positive whole number", key, raw)
	}
	*into = n
	return nil
}
