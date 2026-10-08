package signer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadPolicyRejects(t *testing.T) {
	_, approver := testKey(t, "approver")
	cases := []struct {
		name, body, want string
	}{
		{"wrong kind", "kind: bunker\nrules: [{name: a, deny: {}}]", "kind must be"},
		{"unknown field", "kind: signer-policy\nrules: [{name: a, deny: {}, bogus: 1}]", "unknown field"},
		{"no rules", "kind: signer-policy\nrules: []", "at least one rule"},
		{"both allow and deny", "kind: signer-policy\nrules: [{name: a, allow: {kinds: [1]}, deny: {}}]", "exactly one of allow or deny"},
		{"empty allow selector", "kind: signer-policy\nrules: [{name: a, allow: {}}]", "--allow-catch-all"},
		{"unknown method", "kind: signer-policy\nrules: [{name: a, allow: {methods: [get_secret]}}]", "unknown method"},
		{"kinds with decrypt", "kind: signer-policy\nrules: [{name: a, allow: {methods: [nip44_decrypt], kinds: [1]}}]", "only apply to sign_event"},
		{"bad regex", "kind: signer-policy\nrules: [{name: a, allow: {kinds: [1]}, deny_matching: ['(']}]", "deny_matching #1"},
		{"bad rate", "kind: signer-policy\nrules: [{name: a, allow: {kinds: [1]}, rate: 5/d}]", "unit must be"},
		{"duplicate rule", "kind: signer-policy\nrules: [{name: a, deny: {}}, {name: a, deny: {}}]", "duplicate name"},
		{"deny with extras", "kind: signer-policy\nrules: [{name: a, deny: {}, rate: 1/s}]", "deny rules take only a selector"},
		{"max_age over cap", "kind: signer-policy\nrules: [{name: a, allow: {kinds: [1]}, require: {attestations: [{name: x, kinds: [9], authors: [" + approver + "], max_age: 25h, binds: [{tag: e, from: id}]}]}}]", "exceeds"},
		{"no binds", "kind: signer-policy\nrules: [{name: a, allow: {kinds: [1]}, require: {attestations: [{name: x, kinds: [9], authors: [" + approver + "], max_age: 1h, binds: []}]}}]", "binds is required"},
		{"no authors", "kind: signer-policy\nrules: [{name: a, allow: {kinds: [1]}, require: {attestations: [{name: x, kinds: [9], max_age: 1h, binds: [{tag: e, from: id}]}]}}]", "at least one key"},
		{"unknown placeholder", "kind: signer-policy\nrules: [{name: a, allow: {kinds: [1]}, require: {attestations: [{name: x, kinds: [9], authors: [" + approver + "], max_age: 1h, binds: [{content: 'approve {commit}'}]}]}}]", "unknown placeholder {commit}"},
		{"bad from", "kind: signer-policy\nrules: [{name: a, allow: {kinds: [1]}, require: {attestations: [{name: x, kinds: [9], authors: [" + approver + "], max_age: 1h, binds: [{tag: e, from: commit}]}]}}]", "want \"id\""},
		{"two bind forms", "kind: signer-policy\nrules: [{name: a, allow: {kinds: [1]}, require: {attestations: [{name: x, kinds: [9], authors: [" + approver + "], max_age: 1h, binds: [{tag: e, from: id, content: x}]}]}}]", "exactly one of"},
		{"require on decrypt", "kind: signer-policy\nrules: [{name: a, allow: {methods: [nip44_decrypt]}, require: {attestations: [{name: x, kinds: [9], authors: [" + approver + "], max_age: 1h, binds: [{tag: e, from: id}]}]}}]", "limited to sign_event"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := LoadPolicy(writePolicy(t, "%s", c.body), LoadOptions{})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

func TestLoadPolicyAllowCatchAll(t *testing.T) {
	path := writePolicy(t, "kind: signer-policy\nrules: [{name: all, allow: {}}]")
	if _, err := LoadPolicy(path, LoadOptions{AllowCatchAll: true}); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPolicyRejectsSignerAsAuthor(t *testing.T) {
	_, signerPub := testKey(t, "signer")
	path := writePolicy(t, "kind: signer-policy\nrules: [{name: a, allow: {kinds: [1]}, require: {attestations: [{name: x, kinds: [9], authors: [%s], max_age: 1h, binds: [{tag: e, from: id}]}]}}]", npub(t, signerPub))
	_, err := LoadPolicy(path, LoadOptions{SignerPub: signerPub})
	if err == nil || !strings.Contains(err.Error(), "signer's own key") {
		t.Fatalf("err = %v", err)
	}
}

func TestAuthorsFile(t *testing.T) {
	_, a := testKey(t, "a")
	_, b := testKey(t, "b")
	dir := t.TempDir()
	policy := filepath.Join(dir, "p.yaml")
	authors := filepath.Join(dir, "approvers.txt")
	body := "kind: signer-policy\nrules: [{name: r, allow: {kinds: [1]}, require: {attestations: [{name: x, kinds: [9], authors_file: approvers.txt, max_age: 1h, binds: [{tag: e, from: id}]}]}}]"
	if err := os.WriteFile(policy, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(authors, []byte("# maintainers\n\n"+npub(t, a)+"\n  "+b+"  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPolicy(policy, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	spec := &p.Rules[0].Require.Attestations[0]
	if !spec.authors[a] || !spec.authors[b] || len(spec.authors) != 2 {
		t.Fatalf("authors = %v", spec.authors)
	}
	if len(p.Files) != 2 || p.Files[1] != authors {
		t.Fatalf("Files = %v", p.Files)
	}

	if err := os.WriteFile(authors, []byte(npub(t, a)+"\nnot-a-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicy(policy, LoadOptions{}); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("junk line: err = %v", err)
	}
}

func TestParseRate(t *testing.T) {
	r, err := ParseRate("60/m")
	if err != nil || r.N != 60 || r.Per != time.Minute {
		t.Fatalf("ParseRate = %+v, %v", r, err)
	}
	for _, bad := range []string{"", "60", "0/m", "-1/s", "x/s", "1/d"} {
		if _, err := ParseRate(bad); err == nil {
			t.Errorf("ParseRate(%q) succeeded", bad)
		}
	}
}

func TestExamplePolicyLoads(t *testing.T) {
	p, err := LoadPolicy("../examples/signer/agent-policy.yaml", LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !p.UsesAttestations() {
		t.Fatal("example policy should require attestations")
	}
}
