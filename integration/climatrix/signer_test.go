package climatrix

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip49"
)

// signerProc is a running "ncli signer serve".
type signerProc struct {
	Socket, URI        string
	Decisions, Denials string
	cmd                *exec.Cmd
}

func (s *signerProc) stop() {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Signal(os.Interrupt)
		_ = s.cmd.Wait()
		s.cmd = nil
	}
}

// startSigner runs "signer serve" with the agent's key (as an ncryptsec
// file) and waits for its socket.
func startSigner(t *testing.T, e *Env, policy string, extra ...string) *signerProc {
	t.Helper()
	agent := A(t, "agent")
	enc, err := nip49.Encrypt(agent.PrivHex, "signer-pw")
	if err != nil {
		t.Fatal(err)
	}
	keyFile := e.WriteFile("agent.ncryptsec", enc+"\n")
	pwFile := e.WriteFile("signer.pw", "signer-pw\n")

	dir, err := os.MkdirTemp("", "sg")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	s := &signerProc{
		Socket:    filepath.Join(dir, "agent.sock"),
		Decisions: filepath.Join(e.Dir, "decisions.ndjson"),
		Denials:   filepath.Join(e.Dir, "denials.ndjson"),
	}
	s.URI = "bunker+unix://" + s.Socket
	out, err := os.Create(s.Decisions)
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(e.Dir, "signer.log")
	logf, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"signer", "serve", "--socket", s.Socket, "--policy", policy,
		"--identity", "file:" + keyFile, "--password-file", pwFile,
		"--state-dir", filepath.Join(e.Dir, "signer-state"), "--denials-file", s.Denials}, extra...)
	s.cmd = exec.Command(bin(t), args...)
	s.cmd.Dir = e.Dir
	s.cmd.Env = e.environ()
	s.cmd.Stdout, s.cmd.Stderr = out, logf
	if err := s.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.stop(); _ = out.Close(); _ = logf.Close() })

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(s.Socket); err == nil {
			return s
		}
		time.Sleep(50 * time.Millisecond)
	}
	b, _ := os.ReadFile(logPath)
	t.Fatalf("signer never created its socket\n%s", b)
	return nil
}

func signerPolicy(t *testing.T, e *Env) string {
	t.Helper()
	return e.WriteFile("policy.yaml", fmt.Sprintf(`kind: signer-policy
rules:
  - name: notes
    allow: {kinds: [1]}
    deny_matching: ['nsec1[02-9ac-hj-np-z]{58}']
  - name: repo-state
    allow: {kinds: [30618]}
    max_clock_skew: 2m
    require:
      attestations:
        - {name: approval, kinds: [9], authors: [%s], max_age: 15m, binds: [{content: "approve {id}"}]}
  - name: rest
    deny: {}
`, A(t, "alice").Npub))
}

func TestSignerCLI(t *testing.T) {
	e := NewEnv(t)
	agent, alice := A(t, "agent"), A(t, "alice")
	s := startSigner(t, e, signerPolicy(t, e))
	now := time.Now().Unix()

	var st struct {
		OK     bool   `json:"ok"`
		Pubkey string `json:"pubkey"`
		Rules  int    `json:"rules"`
	}
	e.MustOK(t, "signer", "status", "--socket", s.URI, "--json").JSON(t, &st)
	if !st.OK || st.Pubkey != agent.PubHex || st.Rules != 3 {
		t.Fatalf("status = %+v", st)
	}
	if r := e.MustOK(t, "signer", "status", "--socket", s.Socket); !strings.Contains(r.Stdout, agent.Npub) {
		t.Errorf("text status:\n%s", r)
	}
	e.Run(t, "signer", "status", "--socket", filepath.Join(e.Dir, "none.sock"), "--json").ExpectErr(t, "network")

	note := e.WriteFile("note.json", fmt.Sprintf(`{"kind":1,"created_at":%d,"tags":[],"content":"from the sidecar"}`, now))
	out := filepath.Join(e.Dir, "signed.json")
	e.MustOK(t, "id", "sign", "--signer", s.URI, "-e", note, "-o", out, "--json")
	var signed nip01.Event
	b, _ := os.ReadFile(out)
	if err := json.Unmarshal(b, &signed); err != nil || signed.Verify() != nil || signed.PubKey != agent.PubHex {
		t.Fatalf("signed = %s", b)
	}

	leak := e.WriteFile("leak.json", fmt.Sprintf(`{"kind":1,"created_at":%d,"tags":[],"content":"my key %s"}`, now, agent.Nsec))
	er := e.Run(t, "id", "sign", "--signer", s.URI, "-e", leak, "-o", out, "--json").ExpectErr(t, "auth")
	if !strings.Contains(er.Error, "signer's key") {
		t.Errorf("leak denial: %+v", er)
	}
	dm := e.WriteFile("dm.json", fmt.Sprintf(`{"kind":4,"created_at":%d,"tags":[],"content":"private words"}`, now))
	e.Run(t, "id", "sign", "--signer", s.URI, "-e", dm, "-o", out, "--json").ExpectErr(t, "auth")

	// Repo state needs alice's approval, once.
	state := &nip01.Event{PubKey: agent.PubHex, CreatedAt: uint64(now), Kind: 30618, Tags: [][]string{{"d", "repo"}, {"refs/heads/main", "abc"}}}
	id, _ := state.HashID()
	stateID := fmt.Sprintf("%x", id)
	stateJSON, _ := json.Marshal(map[string]any{"kind": state.Kind, "created_at": state.CreatedAt, "tags": state.Tags, "content": ""})
	statePath := e.WriteFile("state.json", string(stateJSON))
	e.Run(t, "id", "sign", "--signer", s.URI, "-e", statePath, "-o", out, "--json").ExpectErr(t, "auth")

	approval := Ev(t, alice.PrivHex, 9, "approve "+stateID)
	approvalJSON, _ := json.Marshal(approval)
	approvalPath := e.WriteFile("approval.json", string(approvalJSON))
	e.MustOK(t, "id", "sign", "--signer", s.URI, "-e", statePath, "-o", out, "--attestations", approvalPath, "--json")
	er = e.Run(t, "id", "sign", "--signer", s.URI, "-e", statePath, "-o", out, "--attestations", approvalPath, "--json").ExpectErr(t, "auth")
	if !strings.Contains(er.Error, "already used") {
		t.Errorf("replay: %+v", er)
	}

	// Offline dry run agrees with the live signer.
	var res struct{ Decision, Rule string }
	e.MustOK(t, "signer", "check", "--policy", signerPolicy(t, e), "-e", note, "--pubkey", agent.Npub, "--json").JSON(t, &res)
	if res.Decision != "allow" || res.Rule != "notes" {
		t.Errorf("check allow: %+v", res)
	}
	r := e.Run(t, "signer", "check", "--policy", signerPolicy(t, e), "-e", dm, "--pubkey", agent.Npub, "--json")
	if r.Code != 7 || json.Unmarshal([]byte(r.Stdout), &res) != nil || res.Decision != "deny" || res.Rule != "rest" {
		t.Errorf("check deny:\n%s", r)
	}

	// Logs: every decision on stdout, denials alone in the file, no content.
	s.stop()
	decisions, _ := os.ReadFile(s.Decisions)
	denials, _ := os.ReadFile(s.Denials)
	if n := strings.Count(string(decisions), "\n"); n != 6 {
		t.Errorf("decision lines = %d, want 6\n%s", n, decisions)
	}
	if n := strings.Count(string(denials), "\n"); n != 4 || strings.Contains(string(denials), `"allow"`) {
		t.Errorf("denial lines = %d\n%s", n, denials)
	}
	for _, secret := range []string{agent.Nsec, agent.PrivHex, "private words", "from the sidecar"} {
		if strings.Contains(string(decisions), secret) {
			t.Errorf("decision log leaks %.12q", secret)
		}
	}
}

func TestSignerPublish(t *testing.T) {
	needsRelay(t)
	e := NewEnv(t)
	r := StartRelay(t, "", nil)
	s := startSigner(t, e, signerPolicy(t, e))

	draft := e.WriteFile("draft.json", fmt.Sprintf(`{"kind":1,"created_at":%d,"tags":[],"content":"published via signer"}`, time.Now().Unix()))
	var rep struct {
		Succeeded int `json:"succeeded"`
	}
	e.MustOK(t, "publish", "-e", draft, "--signer", s.URI, "-s", r.URL, "--json").JSON(t, &rep)
	if rep.Succeeded != 1 {
		t.Fatalf("publish report = %+v", rep)
	}
	got, _ := Dial(t, r.URL).Req(F{"authors": []string{A(t, "agent").PubHex}, "kinds": []int{1}})
	if len(got) != 1 || got[0].Content != "published via signer" {
		t.Fatalf("relay has %d events", len(got))
	}

	dm := e.WriteFile("dm.json", fmt.Sprintf(`{"kind":4,"created_at":%d,"tags":[],"content":"x"}`, time.Now().Unix()))
	e.Run(t, "publish", "-e", dm, "--signer", s.URI, "-s", r.URL, "--json").ExpectErr(t, "auth")
}

func TestSignerServeErrors(t *testing.T) {
	e := NewEnv(t)
	policy := signerPolicy(t, e)
	s := startSigner(t, e, policy)

	agent := A(t, "agent")
	plain := e.WriteFile("plain.key", agent.Nsec+"\n")
	e.Run(t, "signer", "serve", "--socket", s.Socket+"2", "--policy", policy, "--identity", "file:"+plain, "--json").ExpectErr(t, "invalid_input")
	e.Run(t, "signer", "serve", "--socket", s.Socket+"2", "--policy", policy, "--identity", agent.Nsec, "--json").ExpectErr(t, "invalid_input")

	catchAll := e.WriteFile("all.yaml", "kind: signer-policy\nrules: [{name: all, allow: {}}]")
	e.Run(t, "signer", "serve", "--socket", s.Socket+"2", "--policy", catchAll, "--json").ExpectErr(t, "invalid_input")

	enc, _ := nip49.Encrypt(agent.PrivHex, "pw")
	key := e.WriteFile("k.ncryptsec", enc)
	pw := e.WriteFile("k.pw", "pw")
	e.Run(t, "signer", "serve", "--socket", s.Socket, "--policy", policy, "--identity", "file:"+key, "--password-file", pw,
		"--state-dir", filepath.Join(e.Dir, "st2"), "--json").ExpectErr(t, "conflict")
	e.Run(t, "signer", "serve", "--socket", s.Socket+"2", "--policy", policy, "--identity", "file:"+key, "--password-file", pw, "--json").ExpectErr(t, "usage")
}
