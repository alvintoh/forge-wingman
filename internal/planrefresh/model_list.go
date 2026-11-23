package planrefresh

import (
	"fmt"
	"slices"
	"strings"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

// refusalReason is which completeness check a model list failed.
type refusalReason string

const (
	refusedOverCap      refusalReason = "over-cap"
	refusedNotAModelID  refusalReason = "not-a-model-id"
	refusedMissingRated refusalReason = "missing-rated-model"
	refusedDroppedShare refusalReason = "dropped-share"
)

// listRefusal is why checkModelList refused a model list.
type listRefusal struct {
	reason refusalReason
	detail string
}

func (r *listRefusal) Error() string { return r.detail }

// refuse is a refusal for reason, its detail formatted as fmt.Sprintf does.
func refuse(reason refusalReason, format string, args ...any) *listRefusal {
	return &listRefusal{reason: reason, detail: fmt.Sprintf(format, args...)}
}

// checkModelList refuses a model list that cannot be the vendor's whole list, so
// a short or garbled reply is recorded as unread rather than as models dropped:
// one over the cap, one holding an id that is not a model id, one missing a model
// on the plan's rate card, or one dropping more than maxDroppedPercent of the last
// good list.
func checkModelList(ids, rateCard []string, last []providers.ModelFacts) *listRefusal {
	if len(ids) > maxModels {
		return refuse(refusedOverCap, "the model list holds %d models, over the cap of %d", len(ids), maxModels)
	}
	for _, id := range ids {
		if !isModelID(id) {
			return refuse(refusedNotAModelID, "the model list holds %q, which is not a model id", providers.CapText(id, maxFieldBytes))
		}
	}
	var missing []string
	for _, id := range rateCard {
		if !slices.Contains(ids, id) {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return refuse(refusedMissingRated, "the model list lacks %s, on the plan's rate card", strings.Join(missing, ", "))
	}
	dropped := 0
	for _, m := range last {
		if !slices.Contains(ids, m.Model) {
			dropped++
		}
	}
	if dropped*100 > maxDroppedPercent*len(last) {
		return refuse(refusedDroppedShare, "the model list drops %d of the last good list's %d models, over %d%% of it",
			dropped, len(last), maxDroppedPercent)
	}
	return nil
}

// isModelID reports whether id has a vendor model id's shape: lowercase letters
// and digits, separated by '.', '-', '_', ':' or '/', starting with a letter or a
// digit, and no longer than a stored field. A word of prose, a blank or an id
// with a space or a capital is not one.
func isModelID(id string) bool {
	if id == "" || len(id) > maxFieldBytes || !isLowerAlnum(rune(id[0])) {
		return false
	}
	for _, r := range id {
		if !isLowerAlnum(r) && !strings.ContainsRune(".-_:/", r) {
			return false
		}
	}
	return true
}

// isLowerAlnum reports whether r is an ASCII lowercase letter or digit.
func isLowerAlnum(r rune) bool { return r >= 'a' && r <= 'z' || r >= '0' && r <= '9' }
