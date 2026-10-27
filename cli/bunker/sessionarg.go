package bunker

import (
	"strings"

	"github.com/ohstr/nmilat/nip19"
)

// sessionPubkey reads an app's pubkey given as hex or npub; anything else
// is returned as given, so the daemon reports it as unknown.
func sessionPubkey(arg string) string {
	if strings.HasPrefix(arg, "npub1") {
		if hex, err := nip19.DecodePublicKey(arg); err == nil {
			return strings.ToLower(hex)
		}
	}
	return arg
}
