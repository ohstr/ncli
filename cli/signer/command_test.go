package signer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/nip49"
	"github.com/ohstr/nmilat/nipLS"
	"github.com/ohstr/nmilat/utils"
)

func key(t *testing.T, name string) (priv, pub string) {
	t.Helper()
	sum := sha256.Sum256([]byte(name))
	priv = hex.EncodeToString(sum[:])
	pub, err := utils.GetPublicKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return priv, pub
}

func writeJSON(t *testing.T, dir, name string, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// execSigner runs "ncli signer <args...>" with --json and returns stdout.
func execSigner(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := NewSignerCommand()
	cmd.PersistentFlags().Bool("json", true, "")
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs(args)

	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	out := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		out <- buf.String()
	}()
	err = cmd.Execute()
	_ = w.Close()
	os.Stdout = orig
	return <-out, err
}

func TestCheckStartTime(t *testing.T) {
	_, signerPub := key(t, "signer")
	approverPriv, approverPub := key(t, "approver")
	dir := t.TempDir()
	policy := filepath.Join(dir, "policy.yaml")
	body := fmt.Sprintf(`kind: signer-policy
rules:
  - name: repo-state
    allow: {kinds: [30618]}
    max_clock_skew: 1m
    require:
      attestations:
        - {name: approval, kinds: [9], authors: [%s], max_age: 15m, binds: [{content: "approve {id}"}]}
`, approverPub)
	if err := os.WriteFile(policy, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	at := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	tgt := &nip01.Event{CreatedAt: uint64(at.Unix()), Kind: 30618, Tags: [][]string{{"d", "repo"}}}
	if err := nipLS.PrepareTarget(tgt, signerPub); err != nil {
		t.Fatal(err)
	}
	events := writeJSON(t, dir, "event.json", &nip01.Event{CreatedAt: tgt.CreatedAt, Kind: tgt.Kind, Tags: tgt.Tags})
	att := &nip01.Event{CreatedAt: uint64(at.Unix()), Kind: 9, Tags: [][]string{}, Content: "approve " + tgt.ID}
	if err := att.Sign(approverPriv); err != nil {
		t.Fatal(err)
	}
	atts := writeJSON(t, dir, "att.json", att)

	base := []string{"check", "--policy", policy, "-e", events, "--attestations", atts, "--pubkey", signerPub, "--now", at.Add(time.Minute).Format(time.RFC3339)}

	out, err := execSigner(t, base...)
	if err != nil {
		t.Fatalf("without --start-time: %v\n%s", err, out)
	}
	var res CheckResult
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.Decision != "allow" || res.EventID != tgt.ID || len(res.Attestations) != 1 {
		t.Fatalf("allow result = %+v, %v (%s)", res, err, out)
	}

	for _, start := range []string{at.Add(5 * time.Minute).Format(time.RFC3339), strconv.FormatInt(at.Add(5*time.Minute).Unix(), 10)} {
		out, err = execSigner(t, append(base, "--start-time", start)...)
		if common.ExitCode(err) != 7 {
			t.Fatalf("--start-time %s: exit %d, err %v", start, common.ExitCode(err), err)
		}
		if err := json.Unmarshal([]byte(out), &res); err != nil || res.Decision != "deny" || !strings.Contains(res.Reason, "created before the signer started") {
			t.Fatalf("--start-time %s: %+v (%s)", start, res, out)
		}
	}

	if _, err := execSigner(t, append(base, "--start-time", "yesterday")...); common.ExitCode(err) != 3 {
		t.Fatalf("malformed --start-time: exit %d, %v", common.ExitCode(err), err)
	}
}

func TestCheckCounterpart(t *testing.T) {
	_, signerPub := key(t, "signer")
	_, friend := key(t, "friend")
	_, stranger := key(t, "stranger")
	dir := t.TempDir()
	policy := filepath.Join(dir, "policy.yaml")
	_ = os.WriteFile(policy, []byte("kind: signer-policy\nrules: [{name: dm, allow: {methods: [nip44_decrypt], counterparts: ["+friend+"]}}]"), 0o600)

	if _, err := execSigner(t, "check", "--policy", policy, "--method", "nip44_decrypt", "--counterpart", friend, "--pubkey", signerPub); err != nil {
		t.Fatalf("listed counterpart: %v", err)
	}
	if _, err := execSigner(t, "check", "--policy", policy, "--method", "nip44_decrypt", "--counterpart", stranger, "--pubkey", signerPub); common.ExitCode(err) != 7 {
		t.Fatalf("unlisted counterpart: %v", err)
	}
}

func TestServeRefusesPlaintextKeys(t *testing.T) {
	priv, _ := key(t, "signer")
	nsec, _ := nip19.EncodePrivateKey(priv)
	dir := t.TempDir()
	policy := filepath.Join(dir, "policy.yaml")
	_ = os.WriteFile(policy, []byte("kind: signer-policy\nrules: [{name: n, allow: {kinds: [1]}}]"), 0o600)
	sock := filepath.Join(dir, "s.sock")

	for name, content := range map[string]string{"nsec": nsec, "hex": priv} {
		path := filepath.Join(dir, name)
		_ = os.WriteFile(path, []byte(content+"\n"), 0o600)
		_, err := execSigner(t, "serve", "--socket", sock, "--policy", policy, "--identity", "file:"+path)
		if common.ExitCode(err) != 3 || !strings.Contains(err.Error(), "plaintext") {
			t.Errorf("%s key file: %v", name, err)
		}
	}
	_, err := execSigner(t, "serve", "--socket", sock, "--policy", policy, "--identity", nsec)
	if common.ExitCode(err) != 3 || strings.Contains(err.Error(), nsec) {
		t.Errorf("nsec argument: %v", err)
	}
	if _, err := os.Stat(sock); err == nil {
		t.Error("a refused key still bound the socket")
	}
}

func TestLoadKeyFileNcryptsec(t *testing.T) {
	priv, _ := key(t, "signer")
	enc, err := nip49.Encrypt(priv, "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "key")
	_ = os.WriteFile(path, []byte("\n"+enc+"\n"), 0o600)
	cmd := NewSignerCommand()

	got, extra, err := loadKeyFile(cmd, path, "hunter2")
	if err != nil || got != priv || len(extra) != 1 || extra[0] != enc {
		t.Fatalf("loadKeyFile = %q, %v, %v", got, extra, err)
	}
	if _, _, err := loadKeyFile(cmd, path, "wrong"); common.ExitCode(err) != 7 {
		t.Fatalf("wrong password: %v", err)
	}
}
