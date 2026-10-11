package climatrix

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// loginPolicy lets the agent sign notes and log in to exactly these relays.
func loginPolicy(t *testing.T, e *Env, relays ...string) string {
	t.Helper()
	return e.WriteFile("login-policy.yaml", fmt.Sprintf(`kind: signer-policy
rules:
  - name: notes
    allow: {kinds: [1]}
  - name: relay-login
    allow: {kinds: [22242], tags: {relay: [%s]}}
  - name: rest
    deny: {}
`, strings.Join(relays, ", ")))
}

// decision is one line of the signer's decision log.
type decision struct {
	Method   string `json:"method"`
	Kind     *int   `json:"kind"`
	Decision string `json:"decision"`
	Rule     string `json:"rule"`
}

// decisionsSince reads the signer's decision log past the first skip lines.
func decisionsSince(t *testing.T, s *signerProc, skip int) []decision {
	t.Helper()
	b, err := os.ReadFile(s.Decisions)
	if err != nil {
		t.Fatal(err)
	}
	var out []decision
	for i, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" || i < skip {
			continue
		}
		var d decision
		if err := json.Unmarshal([]byte(line), &d); err != nil {
			t.Fatalf("decision line %q: %v", line, err)
		}
		out = append(out, d)
	}
	return out
}

func decisionCount(t *testing.T, s *signerProc) int {
	t.Helper()
	b, _ := os.ReadFile(s.Decisions)
	return strings.Count(string(b), "\n")
}

// onlyLogins asserts ds is exactly n allowed 22242 signatures.
func onlyLogins(t *testing.T, ds []decision, n int) {
	t.Helper()
	if len(ds) != n {
		t.Fatalf("signer decisions = %+v, want %d logins", ds, n)
	}
	for _, d := range ds {
		if d.Method != "sign_event" || d.Kind == nil || *d.Kind != 22242 || d.Decision != "allow" {
			t.Fatalf("signer decision %+v, want only allowed 22242s", d)
		}
	}
}

// loginErr is ErrReport plus the login fields.
type loginErr struct {
	Code   string `json:"code"`
	Input  string `json:"input"`
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

func lastLoginErr(t *testing.T, r Result) loginErr {
	t.Helper()
	lines := r.stderrLines()
	var le loginErr
	if len(lines) == 0 || json.Unmarshal([]byte(lines[len(lines)-1]), &le) != nil {
		t.Fatalf("no JSON error report\n%s", r)
	}
	return le
}

func draftNote(t *testing.T, e *Env, name, content string) string {
	t.Helper()
	return e.WriteFile(name, fmt.Sprintf(`{"kind":1,"created_at":%d,"tags":[],"content":%q}`, time.Now().Unix(), content))
}

func TestRelayLogin_AuthRequired(t *testing.T) {
	needsRelay(t)
	e := NewEnv(t)
	agent, alice := A(t, "agent"), A(t, "alice")
	r1, r2, other := authRelay(t), authRelay(t), authRelay(t)
	s := startSigner(t, e, loginPolicy(t, e, r1.URL, r2.URL))

	var rep struct {
		Succeeded int `json:"succeeded"`
	}
	t.Run("publish logs in with the socket signer", func(t *testing.T) {
		e.MustOK(t, "publish", "-e", draftNote(t, e, "a.json", "login one"), "--signer", s.URI, "-s", r1.URL+","+r2.URL, "--json").JSON(t, &rep)
		if rep.Succeeded != 2 {
			t.Fatalf("report = %+v", rep)
		}
	})

	t.Run("find signs exactly one login and nothing else", func(t *testing.T) {
		before := decisionCount(t, s)
		var evs []map[string]any
		res := e.MustOK(t, "find", "--kinds", "1", "--authors", agent.PubHex, "-s", r1.URL, "--auth-identity", s.URI, "--json")
		res.JSON(t, &evs)
		if len(evs) != 1 || evs[0]["content"] != "login one" {
			t.Fatalf("find = %v\n%s", evs, res)
		}
		onlyLogins(t, decisionsSince(t, s, before), 1)
	})

	t.Run("several relays, one login each", func(t *testing.T) {
		before := decisionCount(t, s)
		out := filepath.Join(e.Dir, "dump.json")
		e.MustOK(t, "dump", "--kinds", "1", "--authors", agent.PubHex, "-s", r1.URL+","+r2.URL, "--signer", s.URI, "-o", out, "--json")
		onlyLogins(t, decisionsSince(t, s, before), 2)
		var evs []map[string]any
		b, _ := os.ReadFile(out)
		if json.Unmarshal(b, &evs) != nil || len(evs) != 1 {
			t.Fatalf("dump = %s", b)
		}
	})

	t.Run("raw key", func(t *testing.T) {
		var evs []map[string]any
		e.MustOK(t, "find", "--kinds", "1", "-s", r1.URL, "--signer", alice.Nsec, "--json").JSON(t, &evs)
		if len(evs) != 1 {
			t.Fatalf("find = %v", evs)
		}
		e.MustOK(t, "publish", "-e", draftNote(t, e, "b.json", "by alice"), "--signer", alice.Nsec, "-s", r1.URL, "--json")
	})

	t.Run("no signer", func(t *testing.T) {
		r := NewEnv(t).Run(t, "find", "--kinds", "1", "-s", r1.URL, "--json")
		r.ExpectErr(t, "auth")
		if le := lastLoginErr(t, r); le.Reason != "login_required" || le.Input != r1.URL || !strings.HasPrefix(le.Detail, "auth-required:") {
			t.Errorf("error = %+v", le)
		}
	})

	t.Run("signer refused", func(t *testing.T) {
		r := e.Run(t, "find", "--kinds", "1", "-s", other.URL, "--auth-identity", s.URI, "--json")
		r.ExpectErr(t, "auth")
		if le := lastLoginErr(t, r); le.Reason != "signer_refused" || !strings.Contains(le.Detail, `rule "rest"`) {
			t.Errorf("error = %+v", le)
		}
		r = e.Run(t, "publish", "-e", draftNote(t, e, "c.json", "x"), "--signer", s.URI, "-s", other.URL, "--json")
		r.expectErr(t, "auth", true)
		if le := lastLoginErr(t, r); le.Reason != "signer_refused" {
			t.Errorf("publish error = %+v", le)
		}
	})

	t.Run("events signed elsewhere, login with --auth-signer", func(t *testing.T) {
		signed := filepath.Join(e.Dir, "signed.json")
		e.MustOK(t, "id", "sign", "-e", draftNote(t, e, "d.json", "alice via agent login"), "-o", signed, "--signer", alice.Nsec, "--json")
		e.MustOK(t, "publish", "-e", signed, "--auth-signer", s.URI, "-s", r2.URL, "--json").JSON(t, &rep)
		if rep.Succeeded != 1 {
			t.Fatalf("report = %+v", rep)
		}
	})
}

func TestRelayLogin_MembershipRequired(t *testing.T) {
	needsRelay(t)
	e := NewEnv(t)
	agent := A(t, "agent")
	m := membershipRelay(t)
	s := startSigner(t, e, loginPolicy(t, e, m.URL))

	t.Run("relay refused a non-member", func(t *testing.T) {
		r := e.Run(t, "publish", "-e", draftNote(t, e, "a.json", "too early"), "--signer", s.URI, "-s", m.URL, "--json")
		r.expectErr(t, "auth", true)
		if le := lastLoginErr(t, r); le.Reason != "relay_refused" || !strings.HasPrefix(le.Detail, "restricted:") {
			t.Errorf("error = %+v", le)
		}
	})

	addMember(t, m, agent.PubHex)
	t.Run("a member publishes and reads through the signer", func(t *testing.T) {
		e.MustOK(t, "publish", "-e", draftNote(t, e, "b.json", "member note"), "--signer", s.URI, "-s", m.URL, "--json")
		before := decisionCount(t, s)
		var evs []map[string]any
		e.MustOK(t, "find", "--kinds", "1", "-s", m.URL, "--auth-identity", s.URI, "--json").JSON(t, &evs)
		if len(evs) != 1 || evs[0]["content"] != "member note" {
			t.Fatalf("find = %v", evs)
		}
		onlyLogins(t, decisionsSince(t, s, before), 1)
	})
}

// TestRelayLogin_ApplyStream streams between two auth-required relays
// with --auth-signer.
func TestRelayLogin_ApplyStream(t *testing.T) {
	needsRelay(t)
	e := NewEnv(t)
	agent := A(t, "agent")
	src, dst := authRelay(t), authRelay(t)
	s := startSigner(t, e, loginPolicy(t, e, src.URL, dst.URL))
	e.MustOK(t, "publish", "-e", draftNote(t, e, "a.json", "streamed"), "--signer", s.URI, "-s", src.URL, "--json")

	spec := e.WriteFile("stream.yaml", fmt.Sprintf(`kind: stream
spec:
  from:
    - relay: %s
  to:
    - relay: %s
  filters:
    - kinds: [1]
  recovery:
    store_path: %s
  raw: true
`, src.URL, dst.URL, filepath.Join(e.Dir, "recovery.db")))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin(t), "apply", "-f", spec, "--auth-signer", s.URI)
	cmd.Dir, cmd.Env = e.Dir, e.environ()
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := DialAs(t, dst.URL, agent.PrivHex).Req(F{"kinds": []int{1}})
		if len(got) == 1 && got[0].Content == "streamed" {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("event never reached the destination\n%s", out.String())
}

// TestRelayLogin_NIP46Bunker logs in through a bunker:// remote signer,
// approving each request headlessly.
func TestRelayLogin_NIP46Bunker(t *testing.T) {
	needsRelay(t)
	transport, gated := StartRelay(t, "", nil), authRelay(t)
	e := NewEnv(t)
	alice := A(t, "alice")
	e.MustOK(t, "publish", "-e", draftNote(t, e, "a.json", "for alice"), "--signer", alice.Nsec, "-s", gated.URL, "--json")

	e.MustOK(t, "bunker", "--identity", alice.Nsec, "--relay", transport.URL, "--json")
	t.Cleanup(func() { e.Run(t, "bunker", "stop", "--json") })
	var pair struct {
		Result string `json:"result"`
	}
	e.MustOK(t, "bunker", "connect", "--json").JSON(t, &pair)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin(t), "find", "--kinds", "1", "-s", gated.URL, "--auth-identity", pair.Result, "--json")
	cmd.Dir, cmd.Env = e.Dir, e.environ()
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var methods []string
	for {
		select {
		case err := <-done:
			var evs []map[string]any
			if err != nil || json.Unmarshal([]byte(stdout.String()), &evs) != nil || len(evs) != 1 {
				t.Fatalf("find: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
			}
			if !slices.Contains(methods, "sign_event") {
				t.Fatalf("no sign_event approved; methods = %v", methods)
			}
			return
		case <-time.After(100 * time.Millisecond):
		}
		var pending []struct {
			ID     string `json:"id"`
			Method string `json:"method"`
		}
		e.MustOK(t, "bunker", "pending", "list", "--json").JSON(t, &pending)
		for _, p := range pending {
			methods = append(methods, p.Method)
			e.Run(t, "bunker", "pending", "approve", p.ID, "--json")
		}
	}
}
