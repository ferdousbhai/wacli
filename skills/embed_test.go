package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestRevisionMatchesContent(t *testing.T) {
	sum := sha256.Sum256(Wacli)
	if got := hex.EncodeToString(sum[:]); got != revisionHash {
		t.Fatalf("SKILL.md changed: bump Revision and set revisionHash to %s", got)
	}
}
