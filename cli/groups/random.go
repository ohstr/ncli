package groups

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// randomHex returns n random bytes, hex-encoded -- used for both a
// generated group id ("groups create" with no argument) and a generated
// invite code ("groups invite" with no --code"), neither of which has a
// correctness requirement beyond "hard to guess/collide", unlike
// cli/relay's vault-label disambiguator, which can fall back to a fixed
// value on entropy failure because a label collision just reprompts.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate random value: %w", err)
	}
	return hex.EncodeToString(b), nil
}
