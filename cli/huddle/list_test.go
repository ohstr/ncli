package huddle

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// The signed NIP-98 "u" tag has to be the URL actually requested, and the
// relay rebuilds it from the request's own scheme and host -- so a wrong
// scheme here is a 401 the caller cannot diagnose, not a redirect.
func TestRoomsEndpoint(t *testing.T) {
	for name, tc := range map[string]struct {
		relay string
		want  string
	}{
		"wss becomes https":       {relay: "wss://relay.example", want: "https://relay.example/huddle/rooms"},
		"ws becomes http":         {relay: "ws://localhost:7777", want: "http://localhost:7777/huddle/rooms"},
		"https is left alone":     {relay: "https://relay.example", want: "https://relay.example/huddle/rooms"},
		"a path prefix is kept":   {relay: "wss://relay.example/nostr", want: "https://relay.example/nostr/huddle/rooms"},
		"a trailing slash is not": {relay: "wss://relay.example/", want: "https://relay.example/huddle/rooms"},
		"a query is dropped":      {relay: "wss://relay.example/?x=1", want: "https://relay.example/huddle/rooms"},
	} {
		t.Run(name, func(t *testing.T) {
			base, err := url.Parse(tc.relay)
			require.NoError(t, err)

			got, err := RoomsEndpoint(base)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestRoomsEndpoint_RejectsANonRelayScheme(t *testing.T) {
	base, err := url.Parse("ftp://relay.example")
	require.NoError(t, err)

	_, err = RoomsEndpoint(base)
	require.Error(t, err)
}
