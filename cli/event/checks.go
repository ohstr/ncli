package event

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip13"
	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/nip26"
	"github.com/ohstr/nmilat/nip40"
	"github.com/ohstr/nmilat/nip57"
	"github.com/ohstr/nmilat/nipOA"
)

const (
	statusOK   = "ok"
	statusFail = "fail"
	statusSkip = "skip"
)

// check is the outcome of one verification step.
type check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func ok(name, detail string) check   { return check{Name: name, Status: statusOK, Detail: detail} }
func fail(name, detail string) check { return check{Name: name, Status: statusFail, Detail: detail} }
func skip(name, detail string) check { return check{Name: name, Status: statusSkip, Detail: detail} }

// expect is what the caller requires of an event beyond being well formed
// and correctly signed.
type expect struct {
	Author  string // hex pubkey, "" for any
	Kind    int    // -1 for any
	MinPow  int
	MaxSkew time.Duration
	Now     time.Time
}

// checkEvent runs every offline check on ev. rf, when non-nil, is the
// reference ev was fetched for: what it names is checked too, since a relay
// can answer a REQ with a different event than the one asked for.
func checkEvent(ev *nip01.Event, rf *ref, want expect) []check {
	var out []check

	if err := ev.Validate(); err != nil {
		out = append(out, fail("format", err.Error()))
		return out
	}
	out = append(out, ok("format", ""))

	hash, err := ev.HashID()
	if err != nil {
		return append(out, fail("id", err.Error()))
	}
	computed := hex.EncodeToString(hash)
	idOK := strings.EqualFold(computed, ev.ID)
	if idOK {
		out = append(out, ok("id", ""))
	} else {
		out = append(out, fail("id", "content hashes to "+computed))
	}
	out = append(out, checkSig(ev, hash))

	if rf != nil && !rf.inline() {
		out = append(out, checkRef(ev, rf))
	}

	author := want.Author
	if author == "" && rf != nil && rf.Author != "" {
		author = rf.Author
	}
	if author != "" {
		if strings.EqualFold(ev.PubKey, author) {
			out = append(out, ok("author", ""))
		} else {
			out = append(out, fail("author", fmt.Sprintf("signed by %s, expected %s", npub(ev.PubKey), npub(author))))
		}
	}

	kind := want.Kind
	if kind < 0 && rf != nil && rf.Kind >= 0 && !rf.Addr {
		kind = rf.Kind
	}
	if kind >= 0 {
		if ev.Kind == kind {
			out = append(out, ok("kind", ""))
		} else {
			out = append(out, fail("kind", fmt.Sprintf("kind %d, expected %d", ev.Kind, kind)))
		}
	}

	out = append(out, checkPow(ev, idOK, want.MinPow))

	if want.MaxSkew > 0 {
		limit := uint64(want.Now.Add(want.MaxSkew).Unix())
		if ev.CreatedAt > limit {
			out = append(out, fail("created_at", fmt.Sprintf("%s is more than %s in the future", time.Unix(int64(ev.CreatedAt), 0).UTC().Format(time.RFC3339), want.MaxSkew)))
		} else {
			out = append(out, ok("created_at", ""))
		}
	}

	if c, present := checkExpiration(ev, want.Now); present {
		out = append(out, c)
	}
	if c, present := checkDelegation(ev); present {
		out = append(out, c)
	}
	if c, present := checkOwnerAuth(ev); present {
		out = append(out, c)
	}
	if c, present := checkZap(ev); present {
		out = append(out, c)
	}
	return out
}

func checkSig(ev *nip01.Event, hash []byte) check {
	sigBytes, err := hex.DecodeString(ev.Sig)
	if err != nil || len(sigBytes) == 0 {
		return fail("sig", "missing or not hex")
	}
	sig, err := schnorr.ParseSignature(sigBytes)
	if err != nil {
		return fail("sig", err.Error())
	}
	pubBytes, err := hex.DecodeString(ev.PubKey)
	if err != nil {
		return fail("sig", "pubkey is not hex")
	}
	pub, err := schnorr.ParsePubKey(pubBytes)
	if err != nil {
		return fail("sig", "pubkey: "+err.Error())
	}
	if !sig.Verify(hash, pub) {
		return fail("sig", "signature does not match the content and pubkey")
	}
	return ok("sig", "")
}

func checkRef(ev *nip01.Event, rf *ref) check {
	if rf.Addr {
		coord := fmt.Sprintf("%d:%s:%s", rf.Kind, rf.Author, rf.D)
		got := fmt.Sprintf("%d:%s:%s", ev.Kind, ev.PubKey, dTag(ev))
		if strings.EqualFold(coord, got) {
			return ok("reference", "")
		}
		return fail("reference", "relay returned "+got+" for "+coord)
	}
	if strings.EqualFold(ev.ID, rf.ID) {
		return ok("reference", "")
	}
	return fail("reference", "relay returned "+ev.ID+" for "+rf.ID)
}

func checkPow(ev *nip01.Event, idOK bool, minPow int) check {
	_, hasNonce := nonceTag(ev)
	if !hasNonce && minPow <= 0 {
		return skip("pow", "no nonce tag")
	}
	if !idOK {
		return fail("pow", "id does not match the content")
	}
	got, err := nip13.Difficulty(ev.ID)
	if err != nil {
		return fail("pow", err.Error())
	}
	if hasNonce {
		fields := nip13.Fields{ID: ev.ID, PubKey: ev.PubKey, CreatedAt: ev.CreatedAt, Kind: ev.Kind, Tags: ev.Tags, Content: ev.Content}
		if _, _, err := nip13.ValidatePow(fields); err != nil {
			return fail("pow", err.Error())
		}
	}
	if minPow > 0 && got < minPow {
		return fail("pow", fmt.Sprintf("difficulty %d, need %d", got, minPow))
	}
	return ok("pow", fmt.Sprintf("difficulty %d", got))
}

func nonceTag(ev *nip01.Event) ([]string, bool) {
	for _, t := range ev.Tags {
		if len(t) > 0 && t[0] == "nonce" {
			return t, true
		}
	}
	return nil, false
}

func findTag(ev *nip01.Event, name string) ([]string, bool) {
	for _, t := range ev.Tags {
		if len(t) > 0 && t[0] == name {
			return t, true
		}
	}
	return nil, false
}

func checkExpiration(ev *nip01.Event, now time.Time) (check, bool) {
	if _, present := findTag(ev, "expiration"); !present {
		return check{}, false
	}
	at, err := nip40.GetExpiration(ev.Tags)
	if err != nil {
		return fail("expiration", err.Error()), true
	}
	when := time.Unix(int64(at), 0).UTC().Format(time.RFC3339)
	if uint64(now.Unix()) >= at {
		return fail("expiration", "expired at "+when), true
	}
	return ok("expiration", "expires at "+when), true
}

// checkDelegation verifies a NIP-26 ["delegation", <issuer>, <conditions>,
// <sig>] tag: the token's signature and that this event meets its
// conditions.
func checkDelegation(ev *nip01.Event) (check, bool) {
	t, present := findTag(ev, "delegation")
	if !present {
		return check{}, false
	}
	if len(t) != 4 {
		return fail("delegation", "want [\"delegation\", <pubkey>, <conditions>, <sig>]"), true
	}
	if err := nip26.VerifyDelegationToken(t[1], ev.PubKey, t[2], t[3]); err != nil {
		return fail("delegation", err.Error()), true
	}
	if err := nip26.ValidateConditions(t[2], ev.Kind, ev.CreatedAt); err != nil {
		return fail("delegation", err.Error()), true
	}
	return ok("delegation", "delegated by "+npub(t[1])), true
}

// checkOwnerAuth verifies a NIP-OA owner attestation ("auth" tag).
func checkOwnerAuth(ev *nip01.Event) (check, bool) {
	if _, present := findTag(ev, nipOA.TagName); !present {
		return check{}, false
	}
	tag, err := nipOA.ParseAuthTag(ev.Tags, ev.PubKey)
	if err != nil {
		return fail("owner_auth", err.Error()), true
	}
	if !tag.Conditions.EvaluateKind(ev.Kind) || !tag.Conditions.EvaluateTimeClauses(ev.CreatedAt) {
		return fail("owner_auth", "event does not meet the attestation's conditions"), true
	}
	return ok("owner_auth", "attested by "+npub(tag.OwnerPubkey)), true
}

// checkZap validates a zap request or receipt the way a relay would accept
// it: every MUST-level NIP-57 rule.
func checkZap(ev *nip01.Event) (check, bool) {
	switch ev.Kind {
	case nip57.KindZapReceipt:
		if err := nip57.ValidateZapReceiptForRelay(ev); err != nil {
			return fail("zap", err.Error()), true
		}
		return ok("zap", "receipt matches its request and invoice"), true
	case nip57.KindZapRequest:
		if err := nip57.ValidateZapRequestForRelay(ev); err != nil {
			return fail("zap", err.Error()), true
		}
		return ok("zap", ""), true
	}
	return check{}, false
}

// verdict folds checks into one status.
func verdict(checks []check) string {
	for _, c := range checks {
		if c.Status == statusFail {
			return "invalid"
		}
	}
	return "valid"
}

func failures(checks []check) []string {
	var out []string
	for _, c := range checks {
		if c.Status == statusFail {
			out = append(out, c.Name)
		}
	}
	return out
}

func npub(hexKey string) string {
	if s, err := nip19.EncodePublicKey(hexKey); err == nil {
		return s
	}
	return hexKey
}

func short(s string) string {
	if len(s) <= 16 {
		return s
	}
	return s[:8] + "…" + s[len(s)-6:]
}
