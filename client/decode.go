package client

import (
	"fmt"
	"strings"

	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcw"
)

// DecodedEntity is the result of decoding one bech32 entity -- a NIP-19
// entity (npub/nsec/note/nprofile/nevent/naddr), a NIP-CASH cash-token-
// family string, or a NIP-CW circlehub1... connection. Kind is a pointer so
// "absent" (nevent's optional kind) and "present as zero" (naddr's kind,
// which is required and may legitimately be 0) stay distinguishable -- a
// plain int with omitempty would conflate the two. IdentityRequired and
// AttestedAmountMillis are pointers for the identical reason: a cash
// token's hint fields are each either absent or meaningfully present,
// including present-as-false/present-as-zero.
//
// Secret is deliberately never a field here, for either bech32 family that
// carries one (cash token, circlehub) -- decode is inspection, never a way
// to extract a working spending/dialing credential.
type DecodedEntity struct {
	Type       string   `json:"type"`
	PubKeyHex  string   `json:"pub_hex,omitempty"`
	PrivKeyHex string   `json:"priv_hex,omitempty"`
	EventID    string   `json:"event_id,omitempty"`
	Identifier string   `json:"identifier,omitempty"`
	Kind       *int     `json:"kind,omitempty"`
	Relays     []string `json:"relays,omitempty"`

	// HRP is only set for the two bech32 families whose prefix varies
	// (cash-token-family: lokicash/satscash/...) or is worth echoing back
	// explicitly (circlehub) -- the fixed NIP-19 types leave it empty since
	// Type already says exactly which one it is.
	HRP string `json:"hrp,omitempty"`

	// IdentityRequired, MintSignatureHex, and AttestedAmountMillis are
	// cash-token-only (NIP-CASH's optional identity-required hint and
	// mint-provenance pair). MintSignatureHex/AttestedAmountMillis are only
	// ever both set or both absent -- a lone half is dropped by the
	// decoder itself (nipcash.Decode's own both-or-neither rule).
	IdentityRequired     *bool   `json:"identity_required,omitempty"`
	MintSignatureHex     string  `json:"mint_signature,omitempty"`
	AttestedAmountMillis *uint64 `json:"attested_amount_millis,omitempty"`

	// Label is circlehub-only: the connection's optional human-readable
	// name.
	Label string `json:"label,omitempty"`
}

// DecodeEntity decodes a bech32 string into its constituent fields --
// any of the six NIP-19 entities (npub, nsec, note, nprofile, nevent,
// naddr), a NIP-CASH cash-token-family string (lokicash1..., satscash1...,
// any other HRP -- nipcash.Decode itself accepts any HRP, same rule
// cashctl's own dial.Sniff uses), or a NIP-CW circlehub1... Circle Hub
// connection. A cashhub1... string (NIP-CASH's mint-side Hub connection)
// is recognized but rejected with a specific error -- there is no local
// decoder for that format in this codebase, same stance cashctl's dial
// package takes.
func DecodeEntity(input string) (*DecodedEntity, error) {
	trimmed := strings.TrimSpace(input)

	switch {
	case strings.HasPrefix(trimmed, "npub1"):
		hexKey, err := nip19.DecodePublicKey(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid npub: %w", err)
		}
		return &DecodedEntity{Type: "npub", PubKeyHex: hexKey}, nil

	case strings.HasPrefix(trimmed, "nsec1"):
		hexKey, err := nip19.DecodePrivateKey(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid nsec: %w", err)
		}
		return &DecodedEntity{Type: "nsec", PrivKeyHex: hexKey}, nil

	case strings.HasPrefix(trimmed, "note1"):
		hexID, err := nip19.DecodeNote(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid note: %w", err)
		}
		return &DecodedEntity{Type: "note", EventID: hexID}, nil

	case strings.HasPrefix(trimmed, "nprofile1"):
		pointer, err := nip19.DecodeProfile(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid nprofile: %w", err)
		}
		return &DecodedEntity{Type: "nprofile", PubKeyHex: pointer.PublicKey, Relays: pointer.Relays}, nil

	case strings.HasPrefix(trimmed, "nevent1"):
		pointer, err := nip19.DecodeEvent(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid nevent: %w", err)
		}
		entity := &DecodedEntity{Type: "nevent", EventID: pointer.ID, PubKeyHex: pointer.Author, Relays: pointer.Relays}
		if pointer.Kind != 0 {
			kind := pointer.Kind
			entity.Kind = &kind
		}
		return entity, nil

	case strings.HasPrefix(trimmed, "naddr1"):
		pointer, err := nip19.DecodeAddr(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid naddr: %w", err)
		}
		kind := pointer.Kind
		return &DecodedEntity{
			Type:       "naddr",
			PubKeyHex:  pointer.PublicKey,
			Identifier: pointer.Identifier,
			Kind:       &kind,
			Relays:     pointer.Relays,
		}, nil

	case strings.HasPrefix(trimmed, "circlehub1"):
		conn, err := nipcw.DecodeCircleHubConnection(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid circlehub connection: %w", err)
		}
		return &DecodedEntity{
			Type:      "circlehub",
			HRP:       nipcw.CircleHubConnectionHRP,
			PubKeyHex: conn.WalletPubkey,
			Relays:    conn.RelayURLs,
			Label:     conn.Label,
		}, nil

	case strings.HasPrefix(trimmed, "cashhub1"):
		return nil, fmt.Errorf("%q is a Cash Hub connection (mint-side) -- no local decoder for this format", trimmed)

	default:
		// Any other bech32 string is tried as a cash-token-family token --
		// nipcash.Decode accepts any HRP, so there's no fixed prefix to
		// switch on the way there is for circlehub/cashhub.
		if tok, err := nipcash.Decode(trimmed); err == nil {
			entity := &DecodedEntity{
				Type:             "cash_token",
				HRP:              tok.HRP,
				PubKeyHex:        tok.WalletPubkey,
				Relays:           tok.RelayURLs,
				IdentityRequired: tok.IdentityRequired,
			}
			if tok.HasProvenance() {
				entity.MintSignatureHex = fmt.Sprintf("%x", tok.MintSignature)
				entity.AttestedAmountMillis = tok.AttestedAmountMillis
			}
			return entity, nil
		}
		return nil, fmt.Errorf("%q is not a recognized entity (npub/nsec/note/nprofile/nevent/naddr, a circlehub1... connection, or a cash-token-family string)", trimmed)
	}
}
