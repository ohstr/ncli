//go:build ignore

// bunker-demo-client is throwaway tooling for docs/vhs/bunker.tape only --
// excluded from `go build ./...`/`go vet ./...`/`go test ./...` by the
// build tag above, and never shipped. It exists purely to play the "app"
// side of one NIP-46 pairing against a real `ncli bunker` daemon while the
// tape records, since ncli itself has no NIP-46 client at all (see
// skills/ncli-bunker/SKILL.md's "Using bunker with an AI agent" section --
// id sign/publish/apply only ever sign with a local vault/nsec key).
//
// Plays the nostrconnect:// direction specifically -- this "app" generates
// the connection URI, and the signer (the human, inside the `ncli bunker`
// TUI) pastes it in. The signer then publishes a "connect" response
// carrying the URI's own secret to this client's pubkey (see NIP-46's
// "Direct connection initiated by the client", and cli/bunker/daemon.go's
// InitiateNostrconnectWithGrants); this client recognizes the pairing by
// that secret and learns the signer's pubkey from the response's author.
// It answers nothing -- the spec gives it nothing to answer with. That's
// the reverse of the bunker:// direction (this same file used to play,
// before bunker.tape switched scenarios), where the client sends a
// "connect" request instead.
//
// The identity/secret below are FIXED, not freshly generated per run, on
// purpose: bunker.tape's own visible "paste this into the TUI" step has to
// type a literal nostrconnect:// string embedded directly in the .tape
// file -- VHS has no way to interpolate a shell/runtime value into a
// keystroke sequence, so whatever URI gets typed on screen has to be known
// in advance and match, byte for byte, whatever this program actually
// listens for. Regenerating either of these consts without also updating
// the literal URI hardcoded in bunker.tape's own "Type" step breaks the
// recording (the signer would send its connect request to a pubkey nothing
// is listening on). Demo-only keypair, never funded, never saved to any
// vault, never reused outside this recording.
//
// Usage: go run docs/vhs/bunker-demo-client.go
//
// Prints its own nostrconnect:// URI to stderr for reference (bunker.tape
// itself doesn't read this output -- the literal is already baked into the
// tape to match these same fixed consts), then waits for the signer's
// "connect" response, followed by the same two demo sign_event requests
// this file has always sent: a kind 1 (expected approved, with a
// remembered "kind 1 only" grant) and, once that's answered, a kind 7
// (expected *rejected* -- it falls outside the kind-1-only grant just
// created, so it queues for a fresh decision same as the first one did). A
// A rejection on that second request is treated as the expected, successful
// outcome, not a failure; a timeout on either request still is. Exits once
// both are resolved, or after 90s.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip46"
	relayclient "github.com/ohstr/nmilat/relay/client"
	"github.com/ohstr/nmilat/wire"
)

const (
	// Fixed demo-only identity -- see the package doc comment above.
	demoClientPrivHex = "b74ca01088f62ca292bbce4ee63304e87ce705077807e373ed90083e1cb46882"
	demoClientPubHex  = "61ffd6c45416a60c586decb4e69a5bd5df553ec1a0c1f18b8d1bf71181c5182a"
	// Fixed demo-only pairing secret -- same entropy as cli/bunker.NewSecret
	// (16 random bytes, hex-encoded), just generated once and frozen here
	// instead of at runtime.
	demoSecret = "1f1c2d205d715375f192c87468490d82"
	demoRelay  = "wss://relay.ohstr.com"
	// Short on purpose -- board.go's Trusted Apps/Request History tables
	// give every column equal, uncapped SetExpansion(1) with no truncation
	// (see SessionsTable.Update/HistoryTable.Update), so a self-reported
	// Name (URL) pair as long as a real app's often is pushes Kinds/Grants
	// (or, in history, Status) clean off the right edge of a normal-width
	// terminal -- not something this demo should paper over by picking a
	// suspiciously terse Name/Url, but exercising that column-width
	// behavior wasn't bunker.tape's point either.
	demoAppName = "Demo App"
	demoAppURL  = "https://ncli.dev"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	fmt.Fprintln(os.Stderr, "this client's nostrconnect:// URI (fixed -- must match bunker.tape's literal):")
	fmt.Fprintln(os.Stderr, buildNostrconnectURI())

	relayURL, err := url.Parse(demoRelay)
	must(err)

	conn, err := relayclient.Connect(ctx, relayURL)
	must(err)
	defer conn.Close()

	const subID = "bunker-demo"
	conn.SubscribeWithID(subID, nip01.NewSubscriptionFilterGroup(&nip01.SubscriptionFilter{
		Kinds: []int{nip46.KindRequest},
		Tags:  map[string][]string{"p": {demoClientPubHex}},
	}))
	incoming := conn.Events(subID)

	fmt.Fprintln(os.Stderr, "waiting for the signer to paste our URI and connect...")
	signerPub, err := awaitConnect(ctx, incoming)
	must(err)
	fmt.Fprintln(os.Stderr, "paired with", signerPub, "-- sending a demo sign_event request")

	unsigned := nip01.NewUnsignedEvent(1, signerPub, "gm -- signed remotely by an agent, via ncli bunker")
	rawEvent, err := json.Marshal(unsigned)
	must(err)

	signReq, signID, err := nip46.NewRequestEvent(demoClientPrivHex, signerPub, nip46.MethodSignEvent,
		[]string{string(rawEvent)}, nip46.EncryptionNIP44V2)
	must(err)
	must(signReq.Sign(demoClientPrivHex))
	if !conn.Send(signReq) {
		fatal("failed to send sign_event request")
	}

	if err := awaitOK(ctx, incoming, signID); err != nil {
		fatal("sign_event (kind 1): %v", err)
	}
	fmt.Fprintln(os.Stderr, "signed event received -- sending a second request (kind 7, outside the kind-1-only grant)")

	unsigned2 := nip01.NewUnsignedEvent(7, signerPub, "+")
	rawEvent2, err := json.Marshal(unsigned2)
	must(err)

	signReq2, signID2, err := nip46.NewRequestEvent(demoClientPrivHex, signerPub, nip46.MethodSignEvent,
		[]string{string(rawEvent2)}, nip46.EncryptionNIP44V2)
	must(err)
	must(signReq2.Sign(demoClientPrivHex))
	if !conn.Send(signReq2) {
		fatal("failed to send second sign_event request")
	}

	switch err := awaitOK(ctx, incoming, signID2); {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		fatal("sign_event (kind 7): timed out waiting for a decision: %v", err)
	case err != nil:
		fmt.Fprintln(os.Stderr, "second request (kind 7) rejected as expected -- done")
	default:
		fmt.Fprintln(os.Stderr, "second request (kind 7) unexpectedly approved -- done")
	}
}

// buildNostrconnectURI renders this client's own connection URI from the
// fixed consts above -- documentation of what bunker.tape's own literal
// "Type" step has to match, not something the tape reads at runtime (VHS
// tapes are static text; there's no way to feed this program's live output
// back into a keystroke sequence).
//
// name/url/perms are NIP-46's own query params. An earlier version packed
// name and url into a single JSON "metadata" param, which the spec does
// not define; ncli still reads that form, but emitting it here would make
// this demo the one client that does.
func buildNostrconnectURI() string {
	q := url.Values{}
	q.Set("relay", demoRelay)
	q.Set("secret", demoSecret)
	q.Set("name", demoAppName)
	q.Set("url", demoAppURL)

	u := url.URL{Scheme: "nostrconnect", Host: demoClientPubHex, RawQuery: q.Encode()}
	return u.String()
}

// awaitConnect blocks until the signer's "connect" response arrives
// carrying this URI's own secret, and returns the signer's pubkey -- taken
// from the response event's author, which is how a nostrconnect:// client
// learns who its signer is -- for the two demo sign_event requests that
// follow. Nothing is sent back: the pairing is complete on arrival.
//
// The secret is compared in constant time. It is the only thing telling
// the intended signer's response apart from anyone else's who saw the URI
// on the relay, so a timing side channel here would be a real one.
func awaitConnect(ctx context.Context, incoming <-chan *wire.EventSubscriptionResponse) (string, error) {
	for {
		select {
		case ev := <-incoming:
			// A request and a response share kind 24133, distinguished
			// only by their decrypted content -- skip anything that parses
			// as a request (a "method" field), rather than reading its
			// absent result as an empty secret.
			if req, err := nip46.ParseRequestEvent(ev.Event, demoClientPrivHex); err == nil && req.Method != "" {
				continue
			}

			resp, err := nip46.ParseResponseEvent(ev.Event, demoClientPrivHex)
			if err != nil {
				continue
			}
			if subtle.ConstantTimeCompare([]byte(resp.Result), []byte(demoSecret)) != 1 {
				continue
			}
			return ev.Event.PubKey, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// awaitOK blocks for the response event answering requestID, returning its
// error (if the signer rejected/errored) or nil once a matching success
// response arrives.
func awaitOK(ctx context.Context, incoming <-chan *wire.EventSubscriptionResponse, requestID string) error {
	for {
		select {
		case ev := <-incoming:
			resp, err := nip46.ParseResponseEvent(ev.Event, demoClientPrivHex)
			if err != nil || resp.RequestID != requestID {
				continue
			}
			if resp.Error != "" {
				return fmt.Errorf("rejected: %s", resp.Error)
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func must(err error) {
	if err != nil {
		fatal("%v", err)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "bunker-demo-client: "+format+"\n", args...)
	os.Exit(1)
}
