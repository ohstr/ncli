package ncli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ohstr/ncli/signer"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/utils"
)

// startTestSigner runs an in-process signer allowing kind 1 and, with an
// approval from approverPub, kind 30618. It returns the socket URI.
func startTestSigner(t *testing.T, priv, approverPub string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sg")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	policy := filepath.Join(dir, "policy.yaml")
	body := fmt.Sprintf(`kind: signer-policy
rules:
  - name: notes
    allow: {kinds: [1]}
  - name: state
    allow: {kinds: [30618]}
    require:
      attestations:
        - {name: approval, kinds: [9], authors: [%s], max_age: 15m, binds: [{content: "approve {id}"}]}
`, approverPub)
	if err := os.WriteFile(policy, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, err := signer.New(signer.Config{PrivKeyHex: priv, PolicyPath: policy, StateDir: filepath.Join(dir, "state")})
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "s.sock")
	l, err := signer.Listen(sock, 0o600, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx, l); close(done) }()
	t.Cleanup(func() { cancel(); <-done; _ = srv.Close() })
	return signer.URIScheme + "://" + sock
}

func hexKey(t *testing.T, name string) (priv, pub string) {
	t.Helper()
	sum := sha256.Sum256([]byte(name))
	priv = hex.EncodeToString(sum[:])
	pub, err := utils.GetPublicKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return priv, pub
}

func exitCodeOf(err error) int {
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	if err != nil {
		return -1
	}
	return 0
}

func TestIDSignThroughSocketSigner(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	priv, pub := hexKey(t, "socket-signer")
	approverPriv, approverPub := hexKey(t, "approver")
	uri := startTestSigner(t, priv, approverPub)
	dir := t.TempDir()
	now := time.Now().Unix()
	out := filepath.Join(dir, "signed.json")

	note := writeSignTestFile(t, dir, "note.json", fmt.Sprintf(`{"kind":1,"created_at":%d,"tags":[],"content":"via socket"}`, now))
	stdout, err := exec.Command(bin, "id", "sign", "--signer", uri, "-e", note, "-o", out, "--json").Output()
	if err != nil {
		t.Fatalf("id sign --signer: %v\nstderr: %s", err, exitErrStderr(err))
	}
	var res struct {
		Pubkey string `json:"pubkey"`
	}
	_ = json.Unmarshal(stdout, &res)
	var ev nip01.Event
	raw, _ := os.ReadFile(out)
	if err := json.Unmarshal(raw, &ev); err != nil || ev.Verify() != nil || ev.PubKey != pub || res.Pubkey != pub {
		t.Fatalf("signed event = %s, report %s", raw, stdout)
	}

	denied := writeSignTestFile(t, dir, "dm.json", fmt.Sprintf(`{"kind":4,"created_at":%d,"tags":[],"content":"x"}`, now))
	if code := exitCodeOf(exec.Command(bin, "id", "sign", "--signer", uri, "-e", denied, "-o", out, "--json").Run()); code != 7 {
		t.Errorf("denied kind: exit %d, want 7", code)
	}

	state := &nip01.Event{CreatedAt: uint64(now), Kind: 30618, Tags: [][]string{{"d", "repo"}}}
	stateJSON, _ := json.Marshal(state)
	statePath := writeSignTestFile(t, dir, "state.json", string(stateJSON))
	if code := exitCodeOf(exec.Command(bin, "id", "sign", "--signer", uri, "-e", statePath, "-o", out).Run()); code != 7 {
		t.Errorf("state without approval: exit %d, want 7", code)
	}
	if err := signer.PrepareTarget(state, pub); err != nil {
		t.Fatal(err)
	}
	approval := &nip01.Event{CreatedAt: uint64(now), Kind: 9, Tags: [][]string{}, Content: "approve " + state.ID}
	if err := approval.Sign(approverPriv); err != nil {
		t.Fatal(err)
	}
	approvalJSON, _ := json.Marshal(approval)
	approvalPath := writeSignTestFile(t, dir, "approval.json", string(approvalJSON))
	if err := exec.Command(bin, "id", "sign", "--signer", uri, "-e", statePath, "-o", out, "--attestations", approvalPath).Run(); err != nil {
		t.Fatalf("state with approval: %v\nstderr: %s", err, exitErrStderr(err))
	}
	if code := exitCodeOf(exec.Command(bin, "id", "sign", "--signer", uri, "-e", statePath, "-o", out, "--attestations", approvalPath).Run()); code != 7 {
		t.Errorf("replayed approval: exit %d, want 7", code)
	}
}

func TestIDSignSignerFlagErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	nsec, _ := generateTestIdentity(t, bin)
	dir := t.TempDir()
	draft := writeSignTestFile(t, dir, "draft.json", `{"kind":1,"created_at":1719759720,"tags":[],"content":"x"}`)
	out := filepath.Join(dir, "signed.json")

	cases := map[string]struct {
		args []string
		code int
	}{
		"both --identity and --signer":  {[]string{"--identity", nsec, "--signer", "bunker+unix:///tmp/x.sock"}, 2},
		"--attestations without socket": {[]string{"--identity", nsec, "--attestations", draft}, 2},
		"relative socket path":          {[]string{"--signer", "bunker+unix://x.sock"}, 3},
		"no signer listening":           {[]string{"--signer", "bunker+unix:///nonexistent/ncli-test.sock"}, 6},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			args := append([]string{"id", "sign", "-e", draft, "-o", out, "--json"}, c.args...)
			if code := exitCodeOf(exec.Command(bin, args...).Run()); code != c.code {
				t.Errorf("exit %d, want %d", code, c.code)
			}
		})
	}
}
