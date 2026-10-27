package climatrix

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dmPolicy lets the agent seal, open and prove messages with alice only,
// plus copies to itself.
func dmPolicy(t *testing.T, e *Env) string {
	t.Helper()
	agent, alice := A(t, "agent"), A(t, "alice")
	return e.WriteFile("dm-policy.yaml", fmt.Sprintf(`kind: signer-policy
rules:
  - name: seals
    allow: {kinds: [13]}
  - name: alice
    allow: {methods: [nip44_encrypt, nip44_decrypt], counterparts: [%[1]s, %[2]s]}
  - name: proofs
    allow: {methods: [nip44_message_keys], counterparts: [%[1]s, %[2]s]}
  - name: inbox
    allow: {unwrap_from: [%[1]s, %[2]s]}
  - name: rest
    deny: {}
`, alice.Npub, agent.Npub))
}

type dmMessage struct {
	ID        string   `json:"id"`
	Channel   string   `json:"channel"`
	Wrap      string   `json:"wrap"`
	From      string   `json:"from"`
	Subject   string   `json:"subject"`
	ReplyTo   string   `json:"reply_to"`
	Mentions  []string `json:"mentions"`
	Content   string   `json:"content"`
	CreatedAt uint64   `json:"created_at"`
}

type dmSent struct {
	ID     string   `json:"id"`
	To     string   `json:"to"`
	Wraps  []string `json:"wraps"`
	Report struct {
		Succeeded int `json:"succeeded"`
	} `json:"report"`
}

func hasContent(ms []dmMessage, content string) bool {
	for _, m := range ms {
		if m.Content == content {
			return true
		}
	}
	return false
}

func dmRead(t *testing.T, e *Env, args ...string) ([]dmMessage, Result) {
	t.Helper()
	res := e.MustOK(t, append([]string{"dm", "read", "--json"}, args...)...)
	var ms []dmMessage
	res.JSON(t, &ms)
	return ms, res
}

func TestDM(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", nil)
	e := NewEnv(t)
	agent, alice, mallory := A(t, "agent"), A(t, "alice"), A(t, "mallory")
	s := startSigner(t, e, dmPolicy(t, e))
	relay := []string{"-s", r.URL}

	var sent dmSent
	t.Run("send through the socket", func(t *testing.T) {
		res := e.MustOK(t, append([]string{"dm", "send", "--to", alice.Npub, "hello alice", "--subject", "plans", "--signer", s.URI, "--json"}, relay...)...)
		res.JSON(t, &sent)
		if sent.ID == "" || sent.To != alice.PubHex || len(sent.Wraps) != 2 || sent.Report.Succeeded != 2 {
			t.Fatalf("sent %+v\n%s", sent, res)
		}
	})

	var reply dmSent
	t.Run("alice reads it and replies", func(t *testing.T) {
		ms, _ := dmRead(t, e, append([]string{"--from", agent.Npub, "--signer", alice.Nsec}, relay...)...)
		if len(ms) != 1 || ms[0].Content != "hello alice" || ms[0].From != agent.PubHex || ms[0].Subject != "plans" || ms[0].ID != sent.ID || ms[0].Channel != "dm" {
			t.Fatalf("alice read %+v", ms)
		}
		e.MustOK(t, append([]string{"dm", "send", "--to", agent.Npub, "-r", sent.ID, "--signer", alice.Nsec, "--json", "hi agent"}, relay...)...).JSON(t, &reply)
		// Mallory writes too; the agent never asked for her.
		e.MustOK(t, append([]string{"dm", "send", "--to", agent.Npub, "MALLORY-SECRET", "--signer", mallory.Nsec}, relay...)...)
	})

	var fromAlice dmMessage
	t.Run("the agent opens only alice's message", func(t *testing.T) {
		before := decisionCount(t, s)
		ms, res := dmRead(t, e, append([]string{"--from", alice.Npub, "--signer", s.URI}, relay...)...)
		if len(ms) != 1 || ms[0].Content != "hi agent" || ms[0].ReplyTo != sent.ID {
			t.Fatalf("agent read %+v\n%s", ms, res)
		}
		fromAlice = ms[0]
		if strings.Contains(res.Stdout+res.Stderr, "MALLORY-SECRET") {
			t.Fatal("mallory's message leaked")
		}
		if !strings.Contains(res.Stderr, "skipped") {
			t.Fatalf("no skipped count\n%s", res.Stderr)
		}
		// The signer never released a seal from mallory.
		for _, d := range decisionsSince(t, s, before) {
			if d.Method == "nip44_decrypt" && d.Decision == "allow" && d.Rule != "alice" && d.Rule != "inbox" {
				t.Fatalf("unexpected decrypt %+v", d)
			}
		}
		b, _ := os.ReadFile(s.Decisions)
		if strings.Contains(string(b), mallory.PubHex) && strings.Contains(string(b), `"counterpart":"`+mallory.PubHex+`","decision":"allow"`) {
			t.Fatal("signer decrypted for mallory")
		}
	})

	t.Run("own copy, both sides", func(t *testing.T) {
		ms, _ := dmRead(t, e, append([]string{"--from", alice.Npub, "--from", agent.Npub, "--signer", s.URI}, relay...)...)
		if len(ms) != 2 || !hasContent(ms, "hello alice") || !hasContent(ms, "hi agent") {
			t.Fatalf("conversation %+v", ms)
		}
	})

	t.Run("prove to a third party", func(t *testing.T) {
		proof := filepath.Join(e.Dir, "proof.json")
		e.MustOK(t, append([]string{"dm", "prove", fromAlice.Wrap, "--from", alice.Npub, "--signer", s.URI, "-o", proof}, relay...)...)
		raw, _ := os.ReadFile(proof)
		if strings.Contains(string(raw), "hi agent") {
			t.Fatal("proof carries plaintext")
		}

		// The verifier has no keys at all.
		judge := NewEnv(t)
		path := judge.WriteFile("proof.json", string(raw))
		var v struct {
			Verified bool      `json:"verified"`
			Message  dmMessage `json:"message"`
		}
		judge.MustOK(t, "dm", "verify", path, "--from", alice.Npub, "--json").JSON(t, &v)
		if !v.Verified || v.Message.Content != "hi agent" || v.Message.From != alice.PubHex || len(v.Message.Mentions) != 1 || v.Message.Mentions[0] != agent.PubHex {
			t.Fatalf("verify %+v", v)
		}
		if res := judge.Run(t, "dm", "verify", path, "--from", mallory.Npub, "--json"); res.Code != 1 {
			t.Fatalf("wrong sender verified\n%s", res)
		}
		// Change one character of the ciphertext.
		var p map[string]json.RawMessage
		_ = json.Unmarshal(raw, &p)
		var seal map[string]any
		_ = json.Unmarshal(p["seal"], &seal)
		c := []byte(seal["content"].(string))
		c[len(c)/2] ^= 0x01
		seal["content"] = string(c)
		p["seal"], _ = json.Marshal(seal)
		bad, _ := json.Marshal(p)
		if res := judge.RunStdin(t, string(bad), "dm", "verify", "-", "--from", alice.Npub); res.Code != 1 {
			t.Fatalf("tampered proof verified\n%s", res)
		}

		// The agent proves its own sent message from its copy.
		var own []dmMessage
		own, _ = dmRead(t, e, append([]string{"--from", agent.Npub, "--signer", s.URI}, relay...)...)
		if len(own) != 1 {
			t.Fatalf("own copy %+v", own)
		}
		mine := e.MustOK(t, append([]string{"dm", "prove", own[0].Wrap, "--from", agent.Npub, "--signer", s.URI}, relay...)...)
		judge.RunStdin(t, mine.Stdout, "dm", "verify", "-", "--from", agent.Npub).ExpectOK(t)
	})

	t.Run("text output", func(t *testing.T) {
		read := e.MustOK(t, append([]string{"dm", "read", "--from", alice.Npub, "--signer", s.URI}, relay...)...)
		if !strings.Contains(read.Stdout, "hi agent") || !strings.Contains(read.Stdout, alice.Npub) {
			t.Fatalf("dm read text\n%s", read)
		}
		send := e.MustOK(t, append([]string{"dm", "send", "--to", alice.Npub, "text mode", "--signer", s.URI}, relay...)...)
		if !strings.HasPrefix(send.Stdout, "sent ") || !strings.Contains(send.Stdout, "published ") {
			t.Fatalf("dm send text\n%s", send)
		}
		proof := e.MustOK(t, append([]string{"dm", "prove", fromAlice.Wrap, "--from", alice.Npub, "--signer", s.URI}, relay...)...)
		v := NewEnv(t).RunStdin(t, proof.Stdout, "dm", "verify", "-", "--from", alice.Npub).ExpectOK(t)
		if !strings.HasPrefix(v.Stdout, "verified: "+alice.Npub) || !strings.Contains(v.Stdout, "to "+agent.Npub) {
			t.Fatalf("dm verify text\n%s", v)
		}
	})

	t.Run("prove refuses another sender", func(t *testing.T) {
		e.Run(t, append([]string{"dm", "prove", fromAlice.Wrap, "--from", mallory.Npub, "--signer", s.URI, "--json"}, relay...)...).ExpectErr(t, "invalid_input")
	})

	t.Run("state prints only new messages", func(t *testing.T) {
		state := filepath.Join(e.Dir, "dm.state")
		args := append([]string{"--from", alice.Npub, "--signer", s.URI, "--state", state}, relay...)
		if ms, _ := dmRead(t, e, args...); len(ms) != 1 {
			t.Fatalf("first run %+v", ms)
		}
		if ms, res := dmRead(t, e, args...); len(ms) != 0 || strings.TrimSpace(res.Stdout) != "[]" {
			t.Fatalf("second run %+v", ms)
		}
	})

	t.Run("follow streams new messages", func(t *testing.T) {
		p := e.StartStream(t, append([]string{"dm", "read", "-f", "--from", alice.Npub, "--signer", s.URI, "--json"}, relay...)...)
		var m dmMessage
		p.Next(t, &m)
		if m.Content != "hi agent" {
			t.Fatalf("history %+v", m)
		}
		e.MustOK(t, append([]string{"dm", "send", "--to", agent.Npub, "MALLORY-AGAIN", "--signer", mallory.Nsec}, relay...)...)
		e.MustOK(t, append([]string{"dm", "send", "--to", agent.Npub, "live one", "--signer", alice.Nsec}, relay...)...)
		p.Next(t, &m)
		if m.Content != "live one" {
			t.Fatalf("live %+v", m)
		}
		if code := p.ExitCode(); code != 0 {
			t.Fatalf("follow exit %d\n%s", code, p.Stderr())
		}
	})

	t.Run("nip-05 recipient", func(t *testing.T) {
		n := StartNip05(t, map[string]string{"alice": alice.PubHex})
		e.TrustNip05(n)
		var out dmSent
		e.MustOK(t, append([]string{"dm", "send", "--to", n.ID("alice"), "via nip-05", "--signer", s.URI, "--json"}, relay...)...).JSON(t, &out)
		if out.To != alice.PubHex {
			t.Fatalf("to %s", out.To)
		}
	})

	t.Run("text from stdin", func(t *testing.T) {
		e.RunStdin(t, "line one\nline two\n", append([]string{"dm", "send", "--to", alice.Npub, "-", "--signer", s.URI}, relay...)...).ExpectOK(t)
		ms, _ := dmRead(t, e, append([]string{"--from", agent.Npub, "--signer", alice.Nsec}, relay...)...)
		if !hasContent(ms, "line one\nline two") {
			t.Fatalf("stdin text missing: %+v", ms)
		}
	})

	t.Run("signer refuses an unlisted recipient", func(t *testing.T) {
		e.Run(t, append([]string{"dm", "send", "--to", mallory.Npub, "hey", "--signer", s.URI, "--json"}, relay...)...).ExpectErr(t, "auth")
	})
}

func TestDM_Errors(t *testing.T) {
	e := NewEnv(t)
	alice := A(t, "alice")
	dead := DeadRelayURL(t)
	e.Run(t, "dm", "send", "--to", alice.Npub, "x", "--signer", alice.Nsec, "--json").ExpectErr(t, "usage")
	e.Run(t, "dm", "send", "--to", alice.Npub, "x", "-s", dead, "--json").ExpectErr(t, "usage")
	e.Run(t, "dm", "send", "x", "-s", dead, "--signer", alice.Nsec, "--json").ExpectErr(t, "usage")
	e.Run(t, "dm", "send", "--to", "npub1nope", "x", "-s", dead, "--signer", alice.Nsec, "--json").ExpectErr(t, "invalid_input")
	e.Run(t, "dm", "send", "--to", "no-such-label", "x", "-s", dead, "--signer", alice.Nsec, "--json").ExpectErr(t, "not_found")
	e.Run(t, "dm", "send", "--to", alice.Npub, "   ", "-s", dead, "--signer", alice.Nsec, "--json").ExpectErr(t, "invalid_input")
	e.Run(t, "dm", "send", "--to", alice.Npub, "x", "-s", dead, "--signer", alice.Nsec, "--json").ExpectErr(t, "network")
	e.Run(t, "dm", "read", "-s", dead, "--signer", alice.Nsec, "--json").ExpectErr(t, "usage")
	e.Run(t, "dm", "read", "--from", alice.Npub, "-s", dead, "--signer", alice.Nsec, "--json").ExpectErr(t, "network")
	e.Run(t, "dm", "verify", "--from", alice.Npub, "--json").ExpectErr(t, "usage")
	// Like event verify, a failed check still prints its verdict.
	e.RunStdin(t, "{}", "dm", "verify", "-", "--from", alice.Npub, "--json").expectErr(t, "internal", true)
	e.RunStdin(t, "not json", "dm", "verify", "-", "--from", alice.Npub, "--json").ExpectErr(t, "invalid_input")
	e.Run(t, "dm", "--json").ExpectErr(t, "usage")
}
