package common

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/ohstr/nmilat/nip01"
)

// GenerateNIP98Header creates a signed Nostr event (kind 27235) and returns
// the formatted "Nostr <base64>" Authorization header value.
func GenerateNIP98Header(privKey, url, method string) (string, error) {
	// Create NIP-98 event
	// Tags: u (URL) and method
	event := nip01.NewEvent(27235, "",
		[]string{"u", url},
		[]string{"method", method},
	)

	// Sign the event (Sign method populates PubKey, ID, and Sig)
	if err := event.Sign(privKey); err != nil {
		return "", fmt.Errorf("failed to sign NIP-98 event: %w", err)
	}

	// JSON encode
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return "", fmt.Errorf("failed to marshal NIP-98 event: %w", err)
	}

	// Base64 encode
	encoded := base64.StdEncoding.EncodeToString(eventJSON)

	return "Nostr " + encoded, nil
}
