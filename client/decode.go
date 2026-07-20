package client

import (
	"fmt"
	"strings"

	"github.com/ohstr/nmilat/nip19"
)

// DecodedEntity is the result of decoding one NIP-19 bech32 entity. Kind is
// a pointer so "absent" (nevent's optional kind) and "present as zero"
// (naddr's kind, which is required and may legitimately be 0) stay
// distinguishable -- a plain int with omitempty would conflate the two.
type DecodedEntity struct {
	Type       string   `json:"type"`
	PubKeyHex  string   `json:"pub_hex,omitempty"`
	PrivKeyHex string   `json:"priv_hex,omitempty"`
	EventID    string   `json:"event_id,omitempty"`
	Identifier string   `json:"identifier,omitempty"`
	Kind       *int     `json:"kind,omitempty"`
	Relays     []string `json:"relays,omitempty"`
}

// DecodeEntity decodes a NIP-19 bech32 string -- npub, nsec, note,
// nprofile, nevent, or naddr -- into its constituent fields.
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

	default:
		return nil, fmt.Errorf("%q is not a recognized NIP-19 entity (npub/nsec/note/nprofile/nevent/naddr)", trimmed)
	}
}
