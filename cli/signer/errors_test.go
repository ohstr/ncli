package signer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip49"
)

func TestCheckErrors(t *testing.T) {
	_, signerPub := key(t, "signer")
	dir := t.TempDir()
	policy := filepath.Join(dir, "policy.yaml")
	_ = os.WriteFile(policy, []byte("kind: signer-policy\nrules: [{name: notes, allow: {kinds: [1]}}]"), 0o600)
	unsigned := filepath.Join(dir, "e.json")
	_ = os.WriteFile(unsigned, []byte(`{"kind":1,"created_at":1,"tags":[],"content":"x"}`), 0o600)
	other := writeJSON(t, dir, "other.json", map[string]any{"kind": 1, "created_at": 1, "tags": []any{}, "content": "", "pubkey": strings.Repeat("ab", 32)})

	cases := map[string]struct {
		args []string
		code int
	}{
		"policy missing":              {[]string{"check", "--policy", filepath.Join(dir, "nope.yaml"), "-e", unsigned, "--pubkey", signerPub}, 4},
		"policy invalid":              {[]string{"check", "--policy", unsigned, "-e", unsigned, "--pubkey", signerPub}, 3},
		"events missing":              {[]string{"check", "--policy", policy, "-e", filepath.Join(dir, "nope.json"), "--pubkey", signerPub}, 4},
		"no events for sign_event":    {[]string{"check", "--policy", policy, "--pubkey", signerPub}, 2},
		"no pubkey anywhere":          {[]string{"check", "--policy", policy, "-e", unsigned}, 2},
		"bad pubkey":                  {[]string{"check", "--policy", policy, "-e", unsigned, "--pubkey", "npub1nope"}, 3},
		"decrypt without counterpart": {[]string{"check", "--policy", policy, "--method", "nip44_decrypt", "--pubkey", signerPub}, 2},
		"events with decrypt":         {[]string{"check", "--policy", policy, "--method", "nip44_decrypt", "--counterpart", signerPub, "-e", unsigned, "--pubkey", signerPub}, 2},
		"bad --now":                   {[]string{"check", "--policy", policy, "-e", unsigned, "--pubkey", signerPub, "--now", "soon"}, 3},
		"event pubkey differs":        {[]string{"check", "--policy", policy, "-e", other, "--pubkey", signerPub, "--now", "1"}, 7},
	}
	for name, c := range cases {
		if _, err := execSigner(t, c.args...); common.ExitCode(err) != c.code {
			t.Errorf("%s: exit %d (%v), want %d", name, common.ExitCode(err), err, c.code)
		}
	}
}

func TestCheckArrayOutput(t *testing.T) {
	_, signerPub := key(t, "signer")
	dir := t.TempDir()
	policy := filepath.Join(dir, "policy.yaml")
	_ = os.WriteFile(policy, []byte("kind: signer-policy\nrules: [{name: notes, allow: {kinds: [1]}}]"), 0o600)
	events := writeJSON(t, dir, "events.json", []map[string]any{
		{"kind": 1, "created_at": 100, "tags": []any{}, "content": "a"},
		{"kind": 4, "created_at": 100, "tags": []any{}, "content": "b"},
	})
	out, err := execSigner(t, "check", "--policy", policy, "-e", events, "--pubkey", signerPub, "--now", "100")
	if common.ExitCode(err) != 7 {
		t.Fatalf("one denial should exit 7: %v", err)
	}
	var res []CheckResult
	if err := json.Unmarshal([]byte(out), &res); err != nil || len(res) != 2 || res[0].Decision != "allow" || res[1].Decision != "deny" || res[1].Reason != "no rule matches" {
		t.Fatalf("results = %+v (%s)", res, out)
	}
}

func TestServeFlagErrors(t *testing.T) {
	priv, _ := key(t, "signer")
	enc, _ := nip49.Encrypt(priv, "pw")
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key")
	_ = os.WriteFile(keyFile, []byte(enc), 0o600)
	policy := filepath.Join(dir, "policy.yaml")
	_ = os.WriteFile(policy, []byte("kind: signer-policy\nrules: [{name: n, allow: {kinds: [1]}}]"), 0o600)
	sock := filepath.Join(dir, "s.sock")
	base := func(extra ...string) []string {
		return append([]string{"serve", "--socket", sock, "--policy", policy, "--identity", "file:" + keyFile}, extra...)
	}

	cases := map[string]struct {
		args []string
		code int
	}{
		"bad socket mode":       {base("--socket-mode", "rwx"), 3},
		"socket mode too wide":  {base("--socket-mode", "1777"), 3},
		"relative socket":       {[]string{"serve", "--socket", "s.sock", "--policy", policy}, 3},
		"zero watch interval":   {base("--watch", "--watch-interval", "0s"), 3},
		"missing password file": {base("--password-file", filepath.Join(dir, "nope")), 4},
		"no password":           {base(), 2},
		"missing policy":        {[]string{"serve", "--socket", sock, "--policy", filepath.Join(dir, "nope.yaml")}, 4},
		"missing key file":      {[]string{"serve", "--socket", sock, "--policy", policy, "--identity", "file:" + filepath.Join(dir, "nope")}, 4},
		"not an ncryptsec":      {[]string{"serve", "--socket", sock, "--policy", policy, "--identity", "file:" + policy}, 3},
		"pubkey-only identity":  {[]string{"serve", "--socket", sock, "--policy", policy, "--identity", strings.Repeat("ab", 32)}, 3},
	}
	t.Setenv("NCLI_VAULT_PASSWORD", "")
	for name, c := range cases {
		if _, err := execSigner(t, c.args...); common.ExitCode(err) != c.code {
			t.Errorf("%s: exit %d (%v), want %d", name, common.ExitCode(err), err, c.code)
		}
	}
	if _, err := os.Stat(sock); err == nil {
		t.Error("a failed serve left a socket behind")
	}
}
