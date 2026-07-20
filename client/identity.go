package client

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	btcec "github.com/flokiorg/go-flokicoin/crypto"
	"github.com/ohstr/nmilat/nip05"
	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/utils"
)

const nip05Timeout = 10 * time.Second

// Identity bundles every representation of a Nostr keypair.
type Identity struct {
	PrivKeyHex string
	PubKeyHex  string
	Nsec       string
	Npub       string
}

// GenerateIdentity mints a brand-new secp256k1 keypair and returns every
// display form of it. This is the only place a new keypair is ever created.
func GenerateIdentity() (*Identity, error) {
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("failed to generate key: %w", err)
	}
	privHex := hex.EncodeToString(priv.Serialize())

	pubHex, err := utils.GetPublicKey(privHex)
	if err != nil {
		return nil, fmt.Errorf("failed to derive public key: %w", err)
	}

	nsec, err := nip19.EncodePrivateKey(privHex)
	if err != nil {
		return nil, err
	}
	npub, err := nip19.EncodePublicKey(pubHex)
	if err != nil {
		return nil, err
	}

	return &Identity{PrivKeyHex: privHex, PubKeyHex: pubHex, Nsec: nsec, Npub: npub}, nil
}

// IdentityInspection is the read-only result of resolving an identifier
// (vault label, npub, hex pubkey, nsec, nprofile, or nip-05 address). It is
// deliberately not named Inspector/Inspect to avoid clashing with the
// unrelated relay-streaming Inspector/InspectSpec types in client/inspect.go.
type IdentityInspection struct {
	Npub       string   `json:"npub"`
	PubKeyHex  string   `json:"pub_hex"`
	Nip05      string   `json:"nip05,omitempty"`
	Relays     []string `json:"relays,omitempty"`
	InVault    bool     `json:"saved"`
	VaultLabel string   `json:"label,omitempty"`
	// PrivKeyHex/Nsec are populated only when the input itself was an nsec
	// (the caller already possesses the private key), or filled in
	// separately by the CLI layer after a successful --reveal.
	PrivKeyHex string `json:"priv_hex,omitempty"`
	Nsec       string `json:"nsec,omitempty"`
}

// FindIdentifier is find's positional identifier argument, resolved to
// exactly one of an event ID or an author pubkey -- never both. The two
// compose differently with find's other filters (see ResolveFindIdentifier),
// which is why this isn't just a *nip01.SubscriptionFilter: an ID is
// specific enough to stand alone as its own OR'd filter, while an author
// needs to be ANDed into every other filter to compose with --kinds/
// --limit/etc. the way a user expects ("this person's kind-1 notes", not
// "this person's anything, OR any kind-1 note from anyone").
type FindIdentifier struct {
	ID     string // hex event ID, set for event-shaped input
	Author string // hex pubkey, set for author-shaped input
}

// ResolveFindIdentifier resolves find's positional identifier argument.
// Event-shaped input resolves ID: a plain hex event ID passes through
// unchanged, and "note1..."/"nevent1..." NIP-19 strings are decoded (an
// nevent's embedded relay/author/kind hints are ignored -- only its ID is
// used). Author-shaped input resolves Author instead: "npub1...",
// "nprofile1..." (its relay hints are likewise ignored), and nip-05
// "name@domain" addresses (resolved via a live HTTPS fetch, same as
// --authors).
func ResolveFindIdentifier(input string) (*FindIdentifier, error) {
	trimmed := strings.TrimSpace(input)
	switch {
	case strings.HasPrefix(trimmed, "note1"):
		id, err := nip19.DecodeNote(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid note: %w", err)
		}
		return &FindIdentifier{ID: id}, nil
	case strings.HasPrefix(trimmed, "nevent1"):
		pointer, err := nip19.DecodeEvent(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid nevent: %w", err)
		}
		return &FindIdentifier{ID: pointer.ID}, nil
	case strings.HasPrefix(trimmed, "npub1"):
		pubHex, err := nip19.DecodePublicKey(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid npub: %w", err)
		}
		return &FindIdentifier{Author: pubHex}, nil
	case strings.HasPrefix(trimmed, "nprofile1"):
		pubHex, err := nip19.DecodeNprofile(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid nprofile: %w", err)
		}
		return &FindIdentifier{Author: pubHex}, nil
	case strings.Contains(trimmed, "@"):
		pubHex, err := ResolveNip05(trimmed)
		if err != nil {
			return nil, err
		}
		return &FindIdentifier{Author: pubHex}, nil
	default:
		return &FindIdentifier{ID: trimmed}, nil
	}
}

// ResolveIdentifier resolves input -- a saved vault label, an npub, a hex
// pubkey, an nsec, an nprofile, or a nip-05 address -- to an
// IdentityInspection.
func ResolveIdentifier(input string) (*IdentityInspection, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, fmt.Errorf("empty identifier")
	}

	// nsec: the caller already has the private key, so decode it directly
	// and return the full view -- no vault-label lookup makes sense here.
	if strings.HasPrefix(trimmed, "nsec1") {
		privHex, err := nip19.DecodePrivateKey(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid nsec: %w", err)
		}
		pubHex, err := utils.GetPublicKey(privHex)
		if err != nil {
			return nil, fmt.Errorf("invalid nsec: %w", err)
		}
		npub, err := nip19.EncodePublicKey(pubHex)
		if err != nil {
			return nil, err
		}
		nsec, err := nip19.EncodePrivateKey(privHex)
		if err != nil {
			return nil, err
		}

		result := &IdentityInspection{
			Npub: npub, PubKeyHex: pubHex,
			PrivKeyHex: privHex, Nsec: nsec,
		}
		if entry, found, err := FindVaultEntry(npub); err != nil {
			return nil, fmt.Errorf("vault lookup failed: %w", err)
		} else if found {
			result.InVault = true
			result.VaultLabel = entry.Label
		}
		return result, nil
	}

	// Vault label -- cheapest, most common "did I already save this" case,
	// and safe to check before the shape-based branches below: labels
	// can't collide with npub1's bech32 prefix, 64-char hex, or nip-05's
	// "name@domain" shape.
	if entry, found, err := FindVaultEntry(trimmed); err != nil {
		return nil, fmt.Errorf("vault lookup failed: %w", err)
	} else if found && strings.EqualFold(entry.Label, trimmed) {
		pubHex, err := nip19.DecodePublicKey(entry.Npub)
		if err != nil {
			return nil, fmt.Errorf("corrupt vault entry %q: %w", entry.Label, err)
		}
		return &IdentityInspection{
			Npub: entry.Npub, PubKeyHex: pubHex,
			InVault: true, VaultLabel: entry.Label,
		}, nil
	}

	// npub / hex pubkey / nprofile / nip-05 -- mutually exclusive shapes by
	// construction, so there's no real ambiguity to arbitrate between
	// them; order here is purely local-before-network.
	var pubHex, nip05Identifier string
	var relays []string
	switch {
	case strings.HasPrefix(trimmed, "npub1"):
		h, err := nip19.DecodePublicKey(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid npub: %w", err)
		}
		pubHex = h

	case strings.HasPrefix(trimmed, "nprofile1"):
		pointer, err := nip19.DecodeProfile(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid nprofile: %w", err)
		}
		pubHex = pointer.PublicKey
		relays = pointer.Relays

	case utils.Validate32Key(trimmed) == nil:
		pubHex = strings.ToLower(trimmed)

	case strings.Contains(trimmed, "@"):
		h, err := ResolveNip05(trimmed)
		if err != nil {
			return nil, err
		}
		pubHex = h
		nip05Identifier = trimmed

	default:
		return nil, fmt.Errorf("%q is not a saved vault label, npub, hex pubkey, nsec, nprofile, or nip-05 identifier", trimmed)
	}

	npub, err := nip19.EncodePublicKey(pubHex)
	if err != nil {
		return nil, err
	}

	result := &IdentityInspection{Npub: npub, PubKeyHex: pubHex, Nip05: nip05Identifier, Relays: relays}

	// Vault-membership check always runs on the *resolved* pubkey,
	// regardless of which path resolved it -- so inspecting by npub or
	// nip-05 of an already-saved identity still reports its label.
	if entry, found, err := FindVaultEntry(npub); err != nil {
		return nil, fmt.Errorf("vault lookup failed: %w", err)
	} else if found {
		result.InVault = true
		result.VaultLabel = entry.Label
	}

	return result, nil
}

// ResolveFilterAuthors replaces every nip-05 "name@domain" entry in each
// filter's Authors with its resolved hex pubkey, in place -- lets
// --authors and a --targets file's embedded filters mix nip-05 addresses
// with hex pubkeys/prefixes, which are left untouched.
func ResolveFilterAuthors(specs []*FilterSpec) error {
	for _, spec := range specs {
		for i, a := range spec.Authors {
			if !strings.Contains(a, "@") {
				continue
			}
			pubHex, err := ResolveNip05(a)
			if err != nil {
				return err
			}
			spec.Authors[i] = pubHex
		}
	}
	return nil
}

// ResolveNip05 resolves a "name@domain" nip-05 identifier to its hex pubkey
// by fetching the domain's well-known nostr.json document over HTTPS.
func ResolveNip05(identifier string) (string, error) {
	name, _, ok := strings.Cut(identifier, "@")
	if !ok || name == "" {
		return "", fmt.Errorf("invalid nip-05 identifier %q", identifier)
	}
	url := utils.GetNip05URL(identifier)
	if url == "" {
		return "", fmt.Errorf("invalid nip-05 identifier %q", identifier)
	}
	return fetchNip05PubKey(url, name, identifier)
}

// fetchNip05PubKey is split out from ResolveNip05 so tests can point it at
// an httptest.Server directly -- ResolveNip05 always builds a real
// https://<domain>/... URL that can't itself be redirected at a local
// test server.
func fetchNip05PubKey(url, name, identifier string) (string, error) {
	httpClient := &http.Client{Timeout: nip05Timeout}
	resp, err := httpClient.Get(url)
	if err != nil {
		return "", fmt.Errorf("nip-05 lookup for %q failed: %w", identifier, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("nip-05 lookup for %q: server returned %s", identifier, resp.Status)
	}

	var doc nip05.IdentityResponse
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", fmt.Errorf("nip-05 lookup for %q: invalid response: %w", identifier, err)
	}

	pubHex, ok := doc.Names[name]
	if !ok {
		return "", fmt.Errorf("nip-05 identifier %q not found at %s", identifier, utils.GetDomainOnly(identifier))
	}
	if err := utils.Validate32Key(pubHex); err != nil {
		return "", fmt.Errorf("nip-05 identifier %q returned an invalid pubkey: %w", identifier, err)
	}
	return strings.ToLower(pubHex), nil
}
