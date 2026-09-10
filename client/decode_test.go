package client

import (
	"strings"
	"testing"

	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcw"
)

func TestDecodeEntity_Npub(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity() error = %v", err)
	}

	entity, err := DecodeEntity(id.Npub)
	if err != nil {
		t.Fatalf("DecodeEntity(npub) error = %v", err)
	}
	if entity.Type != "npub" {
		t.Fatalf("Type = %q, want npub", entity.Type)
	}
	if entity.PubKeyHex != id.PubKeyHex {
		t.Fatalf("PubKeyHex = %q, want %q", entity.PubKeyHex, id.PubKeyHex)
	}
	if entity.PrivKeyHex != "" || entity.EventID != "" || entity.Identifier != "" || entity.Kind != nil || entity.Relays != nil {
		t.Fatalf("unexpected non-zero fields on an npub decode: %+v", entity)
	}
}

func TestDecodeEntity_Nsec(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity() error = %v", err)
	}

	entity, err := DecodeEntity(id.Nsec)
	if err != nil {
		t.Fatalf("DecodeEntity(nsec) error = %v", err)
	}
	if entity.Type != "nsec" {
		t.Fatalf("Type = %q, want nsec", entity.Type)
	}
	if entity.PrivKeyHex != id.PrivKeyHex {
		t.Fatalf("PrivKeyHex = %q, want %q", entity.PrivKeyHex, id.PrivKeyHex)
	}
}

func TestDecodeEntity_Note(t *testing.T) {
	eventID := "0000000000000000000000000000000000000000000000000000000000000001"
	note, err := nip19.EncodeNote(eventID)
	if err != nil {
		t.Fatalf("nip19.EncodeNote() error = %v", err)
	}

	entity, err := DecodeEntity(note)
	if err != nil {
		t.Fatalf("DecodeEntity(note) error = %v", err)
	}
	if entity.Type != "note" {
		t.Fatalf("Type = %q, want note", entity.Type)
	}
	if entity.EventID != eventID {
		t.Fatalf("EventID = %q, want %q", entity.EventID, eventID)
	}
}

func TestDecodeEntity_Nprofile(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity() error = %v", err)
	}
	relays := []string{"wss://relay.example.com", "wss://relay2.example.com"}
	nprofile, err := nip19.EncodeProfile(id.PubKeyHex, relays)
	if err != nil {
		t.Fatalf("nip19.EncodeProfile() error = %v", err)
	}

	entity, err := DecodeEntity(nprofile)
	if err != nil {
		t.Fatalf("DecodeEntity(nprofile) error = %v", err)
	}
	if entity.Type != "nprofile" {
		t.Fatalf("Type = %q, want nprofile", entity.Type)
	}
	if entity.PubKeyHex != id.PubKeyHex {
		t.Fatalf("PubKeyHex = %q, want %q", entity.PubKeyHex, id.PubKeyHex)
	}
	if strings.Join(entity.Relays, ",") != strings.Join(relays, ",") {
		t.Fatalf("Relays = %v, want %v", entity.Relays, relays)
	}
}

func TestDecodeEntity_NeventWithKindAndAuthor(t *testing.T) {
	eventID := "0000000000000000000000000000000000000000000000000000000000000001"
	author := strings.Repeat("1", 64)
	relays := []string{"wss://relay.example.com"}
	nevent, err := nip19.EncodeEvent(nip19.EventPointer{ID: eventID, Author: author, Kind: 7, Relays: relays})
	if err != nil {
		t.Fatalf("nip19.EncodeEvent() error = %v", err)
	}

	entity, err := DecodeEntity(nevent)
	if err != nil {
		t.Fatalf("DecodeEntity(nevent) error = %v", err)
	}
	if entity.Type != "nevent" {
		t.Fatalf("Type = %q, want nevent", entity.Type)
	}
	if entity.EventID != eventID {
		t.Fatalf("EventID = %q, want %q", entity.EventID, eventID)
	}
	if entity.PubKeyHex != author {
		t.Fatalf("PubKeyHex = %q, want %q", entity.PubKeyHex, author)
	}
	if entity.Kind == nil || *entity.Kind != 7 {
		t.Fatalf("Kind = %v, want 7", entity.Kind)
	}
	if strings.Join(entity.Relays, ",") != strings.Join(relays, ",") {
		t.Fatalf("Relays = %v, want %v", entity.Relays, relays)
	}
}

func TestDecodeEntity_NeventWithoutKindOmitsIt(t *testing.T) {
	eventID := "0000000000000000000000000000000000000000000000000000000000000001"
	nevent, err := nip19.EncodeEvent(nip19.EventPointer{ID: eventID})
	if err != nil {
		t.Fatalf("nip19.EncodeEvent() error = %v", err)
	}

	entity, err := DecodeEntity(nevent)
	if err != nil {
		t.Fatalf("DecodeEntity(nevent) error = %v", err)
	}
	if entity.Kind != nil {
		t.Fatalf("Kind = %v, want nil when the pointer never declared one", entity.Kind)
	}
}

func TestDecodeEntity_NaddrKindZeroIsNotOmitted(t *testing.T) {
	pubHex := strings.Repeat("2", 64)
	// naddr's Kind is required (unlike nevent's, which is optional) -- a
	// literal 0 (kind 0 = set_metadata) must still come back as a non-nil
	// *0, not be treated as "absent" the way nevent's zero-value would be.
	naddr, err := nip19.EncodeAddr(nip19.EntityPointer{Identifier: "my-id", PublicKey: pubHex, Kind: 0})
	if err != nil {
		t.Fatalf("nip19.EncodeAddr() error = %v", err)
	}

	entity, err := DecodeEntity(naddr)
	if err != nil {
		t.Fatalf("DecodeEntity(naddr) error = %v", err)
	}
	if entity.Type != "naddr" {
		t.Fatalf("Type = %q, want naddr", entity.Type)
	}
	if entity.Identifier != "my-id" {
		t.Fatalf("Identifier = %q, want my-id", entity.Identifier)
	}
	if entity.PubKeyHex != pubHex {
		t.Fatalf("PubKeyHex = %q, want %q", entity.PubKeyHex, pubHex)
	}
	if entity.Kind == nil || *entity.Kind != 0 {
		t.Fatalf("Kind = %v, want a non-nil pointer to 0", entity.Kind)
	}
}

func TestDecodeEntity_InvalidNpub(t *testing.T) {
	if _, err := DecodeEntity("npub1notvalidbech32"); err == nil {
		t.Fatal("DecodeEntity(malformed npub) error = nil, want an error")
	}
}

func TestDecodeEntity_WrongPrefixContent(t *testing.T) {
	// A syntactically valid bech32 nsec string handed to what looks like an
	// npub-prefixed request: DecodePublicKey itself rejects a prefix
	// mismatch, so this must still error rather than silently misdecode.
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity() error = %v", err)
	}
	// id.Nsec starts with "nsec1", so it never reaches the npub branch --
	// this instead exercises the unrecognized-prefix path end to end.
	if _, err := DecodeEntity(id.Nsec[:4]); err == nil {
		t.Fatal("DecodeEntity(truncated, prefix-less input) error = nil, want an error")
	}
}

func TestDecodeEntity_Unrecognized(t *testing.T) {
	if _, err := DecodeEntity("not-a-nip19-entity"); err == nil {
		t.Fatal("DecodeEntity(garbage) error = nil, want an error")
	}
	if _, err := DecodeEntity(""); err == nil {
		t.Fatal("DecodeEntity(empty) error = nil, want an error")
	}
}

func TestDecodeEntity_CashToken(t *testing.T) {
	pubkey := strings.Repeat("a", 64)
	secret := strings.Repeat("b", 64)
	relays := []string{"wss://relay.example.com"}
	idRequired := true
	amount := uint64(5000)
	mintSig := make([]byte, 65)

	tok, err := nipcash.Encode(nipcash.Token{
		HRP:                  "lokicash",
		WalletPubkey:         pubkey,
		Secret:               secret,
		RelayURLs:            relays,
		IdentityRequired:     &idRequired,
		MintSignature:        mintSig,
		AttestedAmountMillis: &amount,
	})
	if err != nil {
		t.Fatalf("nipcash.Encode() error = %v", err)
	}

	entity, err := DecodeEntity(tok)
	if err != nil {
		t.Fatalf("DecodeEntity(cash token) error = %v", err)
	}
	if entity.Type != "cash_token" {
		t.Fatalf("Type = %q, want cash_token", entity.Type)
	}
	if entity.HRP != "lokicash" {
		t.Fatalf("HRP = %q, want lokicash", entity.HRP)
	}
	if entity.PubKeyHex != pubkey {
		t.Fatalf("PubKeyHex = %q, want %q", entity.PubKeyHex, pubkey)
	}
	if strings.Join(entity.Relays, ",") != strings.Join(relays, ",") {
		t.Fatalf("Relays = %v, want %v", entity.Relays, relays)
	}
	if entity.IdentityRequired == nil || *entity.IdentityRequired != true {
		t.Fatalf("IdentityRequired = %v, want a non-nil pointer to true", entity.IdentityRequired)
	}
	if entity.MintSignatureHex == "" {
		t.Fatal("MintSignatureHex is empty, want the mint signature hex")
	}
	if entity.AttestedAmountMillis == nil || *entity.AttestedAmountMillis != amount {
		t.Fatalf("AttestedAmountMillis = %v, want a non-nil pointer to %d", entity.AttestedAmountMillis, amount)
	}
}

func TestDecodeEntity_CashTokenWithoutProvenance(t *testing.T) {
	tok, err := nipcash.Encode(nipcash.Token{
		HRP:          "satscash",
		WalletPubkey: strings.Repeat("a", 64),
		Secret:       strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatalf("nipcash.Encode() error = %v", err)
	}

	entity, err := DecodeEntity(tok)
	if err != nil {
		t.Fatalf("DecodeEntity(cash token) error = %v", err)
	}
	if entity.MintSignatureHex != "" || entity.AttestedAmountMillis != nil {
		t.Fatalf("expected no provenance fields, got mint_signature=%q attested_amount=%v", entity.MintSignatureHex, entity.AttestedAmountMillis)
	}
	if entity.IdentityRequired != nil {
		t.Fatalf("IdentityRequired = %v, want nil when the token never set the hint", entity.IdentityRequired)
	}
}

func TestDecodeEntity_CircleHub(t *testing.T) {
	pubkey := strings.Repeat("a", 64)
	secret := strings.Repeat("b", 64)
	relays := []string{"wss://relay.example.com"}

	conn, err := nipcw.EncodeCircleHubConnection(nipcw.CircleHubConnection{
		WalletPubkey: pubkey,
		Secret:       secret,
		RelayURLs:    relays,
		Label:        "test-hub",
	})
	if err != nil {
		t.Fatalf("nipcw.EncodeCircleHubConnection() error = %v", err)
	}

	entity, err := DecodeEntity(conn)
	if err != nil {
		t.Fatalf("DecodeEntity(circlehub) error = %v", err)
	}
	if entity.Type != "circlehub" {
		t.Fatalf("Type = %q, want circlehub", entity.Type)
	}
	if entity.HRP != nipcw.CircleHubConnectionHRP {
		t.Fatalf("HRP = %q, want %q", entity.HRP, nipcw.CircleHubConnectionHRP)
	}
	if entity.PubKeyHex != pubkey {
		t.Fatalf("PubKeyHex = %q, want %q", entity.PubKeyHex, pubkey)
	}
	if strings.Join(entity.Relays, ",") != strings.Join(relays, ",") {
		t.Fatalf("Relays = %v, want %v", entity.Relays, relays)
	}
	if entity.Label != "test-hub" {
		t.Fatalf("Label = %q, want test-hub", entity.Label)
	}
}

func TestDecodeEntity_CashHubRejected(t *testing.T) {
	// cashhub1... is NIP-CASH's mint-side Hub connection -- recognized (so
	// the error names it specifically) but never decoded, since this
	// codebase has no local decoder for that format.
	if _, err := DecodeEntity("cashhub1qqp24wcvayfdl"); err == nil {
		t.Fatal("DecodeEntity(cashhub) error = nil, want an error")
	}
}

func TestDecodeEntity_InvalidCircleHub(t *testing.T) {
	if _, err := DecodeEntity("circlehub1notvalidbech32"); err == nil {
		t.Fatal("DecodeEntity(malformed circlehub) error = nil, want an error")
	}
}
