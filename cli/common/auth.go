package common

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/ohstr/nmilat/nip01"
)

// GenerateNIP98Header creates a signed Nostr event (kind 27235) and returns
// it as an Authorization header value.
//
// body is the exact request body the header will accompany, hashed into the
// payload tag so the signature covers it: without that, a captured header is
// good for any body at the same URL and method until it expires. Pass nil for
// a request that carries no body.
func GenerateNIP98Header(privKey, url, method string, body []byte) (string, error) {
	sum := sha256.Sum256(body)
	event := nip01.NewEvent(27235, "",
		[]string{"u", url},
		[]string{"method", method},
		[]string{"payload", hex.EncodeToString(sum[:])},
	)

	if err := event.Sign(privKey); err != nil {
		return "", fmt.Errorf("failed to sign NIP-98 event: %w", err)
	}

	eventJSON, err := json.Marshal(event)
	if err != nil {
		return "", fmt.Errorf("failed to marshal NIP-98 event: %w", err)
	}

	encoded := base64.StdEncoding.EncodeToString(eventJSON)

	return "Nostr " + encoded, nil
}
