package store

import (
	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	prov "github.com/alvintoh/forge-wingman/internal/providers"
)

// modelReasonUpdates are the writes that leave row's build model reason at
// reason, capped at dispatcher.MaxModelReasonBytes, and its rates version at
// ratesVersion, deleting either one that is empty; a field already holding its
// value is not written.
func modelReasonUpdates(row map[string]any, reason, ratesVersion string) []firestore.Update {
	var updates []firestore.Update
	for _, f := range []struct{ path, value string }{
		{modelReasonField, prov.CapText(reason, dispatcher.MaxModelReasonBytes)},
		{ratesVersionField, ratesVersion},
	} {
		_, present := row[f.path]
		switch {
		case f.value == "" && present:
			updates = append(updates, firestore.Update{Path: f.path, Value: firestore.Delete})
		case f.value != "" && str(row, f.path) != f.value:
			updates = append(updates, firestore.Update{Path: f.path, Value: f.value})
		}
	}
	return updates
}
