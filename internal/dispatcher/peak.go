package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

// PeakMode is whether a run waits out its model's peak-price hours.
type PeakMode string

const (
	// PeakOffPeakOnly holds a run whose model prices peak hours until it can finish outside them.
	PeakOffPeakOnly PeakMode = "off-peak-only"
	// PeakAlways admits a run at any hour.
	PeakAlways PeakMode = "always"
)

// ErrUnknownPeakMode is a stored peak mode that names neither mode.
var ErrUnknownPeakMode = errors.New("unknown peak mode")

// Valid reports whether m names a mode.
func (m PeakMode) Valid() bool { return m == PeakOffPeakOnly || m == PeakAlways }

// PeakModes reads the owner's peak mode.
type PeakModes interface {
	Mode(ctx context.Context) (PeakMode, error)
}

// readPeakMode reads the mode once for a poll. A mode that cannot be read, or
// names no mode, holds like the default, which fails toward the saving.
func readPeakMode(ctx context.Context, d Deps) PeakMode {
	mode, err := d.PeakModes.Mode(ctx)
	if err == nil && !mode.Valid() {
		err = fmt.Errorf("%w %q", ErrUnknownPeakMode, string(mode))
	}
	if err != nil {
		d.Logger.Warn("peakModeUnread", "err", err.Error())
		return PeakOffPeakOnly
	}
	return mode
}

// PeakHeld reports whether a run of model, estimated at minutes, waits for the
// peak window to pass: its model prices peak hours and the run would start in
// one or reach the next. An unknown estimate, zero, holds only while a window
// is open, which is what the surface's peak view asks with — it has no run to
// estimate.
func PeakHeld(mode PeakMode, model string, now time.Time, minutes int64) bool {
	if mode == PeakAlways {
		return false
	}
	rates, _ := providers.RatesFor(model)
	if rates.Peak == nil {
		return false
	}
	return rates.Peak.Spans(now, time.Duration(minutes)*time.Minute)
}
