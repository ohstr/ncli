package signer

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip44"
	"github.com/ohstr/nmilat/nip46"
	"github.com/ohstr/nmilat/nip59"
	"github.com/ohstr/nmilat/nipLS"
)

// nip44_message_keys is denied unless a rule names it: a counterparts-only
// rule, which already allowed every encrypt/decrypt method, does not.
func TestMessageKeysDeniedByDefault(t *testing.T) {
	_, friend := testKey(t, "friend")
	_, other := testKey(t, "other")
	p := mustParse(t, basicPolicy, friend)
	ev := func(method, cp string) Decision {
		return Evaluate(Request{Method: method, Counterpart: cp, Now: t0}, p, noneUsed)
	}
	if d := ev(nip46.MethodNIP44MessageKeys, friend); d.Allow {
		t.Fatal("counterparts-only rule allowed nip44_message_keys")
	}
	for _, m := range []string{nip46.MethodNIP44Decrypt, nip46.MethodNIP44Encrypt, nip46.MethodNIP04Decrypt} {
		if d := ev(m, friend); !d.Allow || d.UnwrapFrom != nil {
			t.Fatalf("%s for friend: %+v", m, d)
		}
	}

	p = mustParse(t, "kind: signer-policy\nrules:\n  - name: proofs\n    allow: {methods: [nip44_message_keys], counterparts: [%s]}\n", npub(t, friend))
	if d := ev(nip46.MethodNIP44MessageKeys, friend); !d.Allow || d.Rule != "proofs" {
		t.Fatalf("named counterpart: %+v", d)
	}
	if d := ev(nip46.MethodNIP44MessageKeys, other); d.Allow {
		t.Fatal("other counterpart allowed")
	}
	if d := ev(nip46.MethodNIP44Decrypt, friend); d.Allow {
		t.Fatal("a message_keys rule allowed decrypt")
	}
}

// Checked-in policies still load, and none of them allows the new method.
func TestShippedPoliciesUnchanged(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(file), "..", "examples", "signer", "agent-policy.yaml")
	_, pub := testKey(t, "signer")
	p, err := LoadPolicy(path, LoadOptions{SignerPub: pub})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range p.Rules {
		if s := r.selector(); s.unwrapFrom != nil || containsString(s.Methods, nip46.MethodNIP44MessageKeys) {
			t.Fatalf("rule %s changed meaning", r.Name)
		}
	}
	_, anyone := testKey(t, "anyone")
	if d := Evaluate(Request{Method: nip46.MethodNIP44MessageKeys, Counterpart: anyone, Now: t0, SignerPub: pub}, p, noneUsed); d.Allow {
		t.Fatal("example policy allows nip44_message_keys")
	}
}

// The example messaging policy loads and decides as its comments say.
func TestExampleMessagingPolicy(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(file), "..", "examples", "signer", "messaging-policy.yaml")
	const (
		self    = "7d899a75887357482f435cca415699c0dcf29807c604edf05e7b494de275d326"
		alice   = "180c1a67e139062ec4c58182926f7b7e55912227b10d3ab645fbb8c16bbd5e7b"
		mallory = "2055a977d599b9943ddd0f7f57f21a7a1906afdd867bd0360383655ae5638ff6"
	)
	p, err := LoadPolicy(path, LoadOptions{SignerPub: self})
	if err != nil {
		t.Fatal(err)
	}
	crypto := func(method, cp string) Decision {
		return Evaluate(Request{Method: method, Counterpart: cp, Now: t0, SignerPub: self}, p, noneUsed)
	}
	for _, c := range []struct {
		method, cp, rule string
		allow            bool
	}{
		{nip46.MethodNIP44Encrypt, alice, "dm-crypto", true},
		{nip46.MethodNIP44Encrypt, self, "dm-crypto", true},
		{nip46.MethodNIP44Encrypt, mallory, "", false},
		{nip46.MethodNIP44MessageKeys, alice, "dm-proofs", true},
		{nip46.MethodNIP44MessageKeys, mallory, "", false},
		// Any other counterpart is a wrap's one-time key: allowed only
		// for a seal from a listed sender.
		{nip46.MethodNIP44Decrypt, mallory, "dm-inbox", true},
	} {
		d := crypto(c.method, c.cp)
		if d.Allow != c.allow || (c.allow && d.Rule != c.rule) {
			t.Errorf("%s %s: %+v", c.method, c.cp[:8], d)
		}
	}
	if d := crypto(nip46.MethodNIP44Decrypt, mallory); d.UnwrapFrom == nil || d.UnwrapFrom[mallory] || !d.UnwrapFrom[alice] {
		t.Fatalf("inbox rule %+v", d)
	}
	for kind, allow := range map[int]bool{13: true, 9: true, 7: true, 1111: true, 0: true, 10050: true, 4: false, 30618: false} {
		d := Evaluate(Request{Method: nip46.MethodSignEvent, Event: target(t, self, kind, t0, "x"), Now: t0, SignerPub: self}, p, noneUsed)
		if d.Allow != allow {
			t.Errorf("kind %d: %+v", kind, d)
		}
	}
}

func TestUnwrapFromValidation(t *testing.T) {
	_, friend := testKey(t, "friend")
	for name, rule := range map[string]string{
		"with counterparts": "allow: {unwrap_from: [%[1]s], counterparts: [%[1]s]}",
		"with kinds":        "allow: {unwrap_from: [%[1]s], kinds: [13]}",
		"other method":      "allow: {unwrap_from: [%[1]s], methods: [nip44_encrypt]}",
		"two methods":       "allow: {unwrap_from: [%[1]s], methods: [nip44_decrypt, nip04_decrypt]}",
		"deny rule":         "deny: {unwrap_from: [%[1]s]}",
		"bad key":           "allow: {unwrap_from: [npub1nope]}",
		"keys for anyone":   "allow: {methods: [nip44_message_keys]}",
		"keys and more":     "allow: {methods: [nip44_message_keys, nip44_encrypt]}",
	} {
		body := "kind: signer-policy\nrules:\n  - name: r\n    " + rule + "\n"
		if _, err := LoadPolicy(writePolicy(t, body, friend), LoadOptions{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	p := mustParse(t, "kind: signer-policy\nrules:\n  - name: inbox\n    allow: {unwrap_from: [%s]}\n", npub(t, friend))
	if got := p.Rules[0].Allow.Methods; len(got) != 1 || got[0] != nip46.MethodNIP44Decrypt {
		t.Fatalf("methods %v", got)
	}
}

func TestCheckUnwrapResult(t *testing.T) {
	friendPriv, friend := testKey(t, "friend")
	malPriv, _ := testKey(t, "mallory")
	d := Decision{Allow: true, UnwrapFrom: map[string]bool{friend: true}}

	seal := signed(t, friendPriv, nip59.KindSeal, t0, "ct")
	if r := CheckUnwrapResult(d, mustJSON(t, seal)); r != "" {
		t.Fatalf("friend's seal refused: %s", r)
	}
	for name, result := range map[string]string{
		"mallory's seal": mustJSON(t, signed(t, malPriv, nip59.KindSeal, t0, "ct")),
		"a rumor":        mustJSON(t, signed(t, friendPriv, 14, t0, "hi")),
		"plain text":     "hello",
		"forged seal":    mustJSON(t, &nip01.Event{ID: seal.ID, PubKey: friend, Kind: 13, Content: "other", Tags: [][]string{}, Sig: seal.Sig, CreatedAt: seal.CreatedAt}),
	} {
		if r := CheckUnwrapResult(d, result); r == "" {
			t.Errorf("%s released", name)
		}
	}
	if r := CheckUnwrapResult(Decision{Allow: true}, "anything"); r != "" {
		t.Fatal("a rule without unwrap_from checked the result")
	}
}

// A seal is backdated up to two days (NIP-59); other kinds keep the
// policy's age limit.
func TestSealAge(t *testing.T) {
	_, pub := testKey(t, "signer")
	p := mustParse(t, "kind: signer-policy\nrules:\n  - {name: seals, allow: {kinds: [13]}}\n  - {name: notes, allow: {kinds: [1]}}\n")
	eval := func(kind int, age time.Duration) Decision {
		return Evaluate(Request{Method: nip46.MethodSignEvent, Event: target(t, pub, kind, t0.Add(-age), "x"), Now: t0, SignerPub: pub}, p, noneUsed)
	}
	if d := eval(13, 47*time.Hour); !d.Allow {
		t.Fatalf("2-day-old seal: %+v", d)
	}
	if d := eval(13, 49*time.Hour); d.Allow {
		t.Fatal("seal older than NIP-59 allows")
	}
	if d := eval(1, time.Hour); d.Allow {
		t.Fatal("an hour-old note allowed")
	}
}

const inboxPolicy = `kind: signer-policy
rules:
  - name: friend
    allow: {methods: [nip44_encrypt, nip44_decrypt], counterparts: [%[1]s]}
  - name: proofs
    allow: {methods: [nip44_message_keys], counterparts: [%[1]s]}
  - name: inbox
    allow: {unwrap_from: [%[1]s]}
  - name: seals
    allow: {kinds: [13]}
`

// Through the socket: a friend's DM opens, Mallory's never leaves the
// signer, whatever the caller asks for.
func TestServerUnwrapFrom(t *testing.T) {
	priv, pub := testKey(t, "signer")
	friendPriv, friend := testKey(t, "friend")
	malPriv, _ := testKey(t, "mallory")
	r := startServer(t, Config{PrivKeyHex: priv, PolicyPath: writePolicy(t, inboxPolicy, friend)})
	c := dial(t, r.uri)
	ctx := ctx5(t)

	friendKey, _ := nip46.NewLocalKey(friendPriv)
	malKey, _ := nip46.NewLocalKey(malPriv)
	dm := func(from *nip46.LocalKey, text string) (*nip01.Event, *nip01.Event) {
		rumor := nip01.NewEvent(14, text, []string{"p", pub})
		seal, err := nip59.Seal(ctx, from, rumor, pub)
		if err != nil {
			t.Fatal(err)
		}
		wrap, err := nip59.WrapSeal(seal, pub)
		if err != nil {
			t.Fatal(err)
		}
		return seal, wrap
	}

	friendSeal, friendWrap := dm(friendKey, "from a friend")
	got, err := nip59.Unwrap(ctx, c, friendWrap, nip59.OpenOptions{From: []string{friend}})
	if err != nil || got.Rumor.Content != "from a friend" {
		t.Fatalf("friend's DM: %+v %v", got, err)
	}

	malSeal, malWrap := dm(malKey, "MALLORY SECRET")
	if _, err := nip59.Unwrap(ctx, c, malWrap, nip59.OpenOptions{}); !errors.Is(err, nipLS.ErrDenied) {
		t.Fatalf("mallory's wrap: %v", err)
	}
	// Asking for the seal layer directly, or any message from mallory.
	if _, err := c.Decrypt(ctx, nip59.EncryptionNIP44V2, malKey.PubKey(), malSeal.Content); !errors.Is(err, nipLS.ErrDenied) {
		t.Fatalf("mallory's seal: %v", err)
	}
	plain, _ := malKey.Encrypt(ctx, nip59.EncryptionNIP44V2, pub, "MALLORY SECRET")
	if _, err := c.Decrypt(ctx, nip59.EncryptionNIP44V2, malKey.PubKey(), plain); !errors.Is(err, nipLS.ErrDenied) {
		t.Fatalf("mallory's message: %v", err)
	}

	// Proof keys: for the friend only.
	keys, err := c.MessageKeys(ctx, friend, friendSeal.Content)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nip44.OpenWithKeys(keys, friendSeal.Content); err != nil {
		t.Fatal(err)
	}
	if _, err := c.MessageKeys(ctx, malKey.PubKey(), malSeal.Content); !errors.Is(err, nipLS.ErrDenied) {
		t.Fatalf("mallory's keys: %v", err)
	}

	log := r.decisions.String()
	if strings.Contains(log, "MALLORY") || strings.Contains(log, "from a friend") {
		t.Fatal("decision log carries message content")
	}
	var unwrapDenials int
	for _, rec := range r.decisions.records(t) {
		if rec.Rule == "inbox" && rec.Decision == "deny" {
			unwrapDenials++
		}
	}
	if unwrapDenials != 3 {
		t.Fatalf("inbox denials %d\n%s", unwrapDenials, log)
	}
}

func mustJSON(t *testing.T, ev *nip01.Event) string {
	t.Helper()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
