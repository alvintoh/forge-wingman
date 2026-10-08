package runner

import (
	"crypto/sha256"
	"encoding/binary"
)

// sampleEvery is FR-17's sampling rate as one run in sampleEvery: 20%.
const sampleEvery = 5

// Sampled reports whether runID falls in FR-17's review sample. It hashes the id
// rather than drawing at random, so the record's answer can be checked later.
func Sampled(runID string) bool {
	sum := sha256.Sum256([]byte(runID))
	return binary.BigEndian.Uint64(sum[:8])%sampleEvery == 0
}
