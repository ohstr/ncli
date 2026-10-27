package climatrix

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

type groupMessage struct {
	ID       string   `json:"id"`
	Channel  string   `json:"channel"`
	Group    string   `json:"group"`
	From     string   `json:"from"`
	ReplyTo  string   `json:"reply_to"`
	Mentions []string `json:"mentions"`
	Content  string   `json:"content"`
}

type groupReport struct {
	Results []struct {
		ID       string `json:"id"`
		Accepted bool   `json:"accepted"`
	} `json:"results"`
}

func TestGroupsChat(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", nil)
	e := NewEnv(t)
	alice, agent, carol := A(t, "alice"), A(t, "agent"), A(t, "carol")
	policy := e.WriteFile("groups-policy.yaml", fmt.Sprintf(`kind: signer-policy
rules:
  - name: chat
    allow: {kinds: [9]}
  - name: relay-login
    allow: {kinds: [22242], tags: {relay: [%s]}}
  - name: rest
    deny: {}
`, r.URL))
	s := startSigner(t, e, policy)
	g := func(args ...string) []string {
		return append(append([]string{"groups"}, args...), "--relay", r.URL, "--json")
	}

	e.MustOK(t, g("create", "chat", "--identity", alice.Nsec)...)
	// The member is named by npub, not only hex.
	e.MustOK(t, g("members", "add", "chat", agent.Npub, "--identity", alice.Nsec)...)

	var hello groupReport
	e.MustOK(t, g("send", "chat", "standup at 10", "-m", agent.Npub, "--identity", alice.Nsec)...).JSON(t, &hello)
	helloID := hello.Results[0].ID
	e.MustOK(t, g("send", "chat", "unrelated", "--identity", alice.Nsec)...)

	t.Run("send through a socket signer", func(t *testing.T) {
		var rep groupReport
		e.MustOK(t, g("send", "chat", "on it", "-r", helloID, "--signer", s.URI)...).JSON(t, &rep)
		if len(rep.Results) != 1 || !rep.Results[0].Accepted {
			t.Fatalf("report %+v", rep)
		}
	})

	t.Run("read", func(t *testing.T) {
		var ms []groupMessage
		e.MustOK(t, g("read", "chat", "--signer", s.URI)...).JSON(t, &ms)
		if len(ms) != 3 {
			t.Fatalf("messages %+v", ms)
		}
		var reply *groupMessage
		for i := range ms {
			if ms[i].Channel != "group" || ms[i].Group != "chat" {
				t.Fatalf("message %+v", ms[i])
			}
			if ms[i].Content == "on it" {
				reply = &ms[i]
			}
		}
		if reply == nil || reply.From != agent.PubHex || reply.ReplyTo != helloID {
			t.Fatalf("reply %+v", reply)
		}
	})

	t.Run("thread", func(t *testing.T) {
		var ms []groupMessage
		e.MustOK(t, g("read", "chat", "--thread", helloID, "--signer", s.URI)...).JSON(t, &ms)
		if len(ms) != 2 {
			t.Fatalf("thread %+v", ms)
		}
	})

	t.Run("feed: mentions across my groups", func(t *testing.T) {
		var ms []groupMessage
		e.MustOK(t, g("feed", "--signer", s.URI)...).JSON(t, &ms)
		if len(ms) != 1 || ms[0].ID != helloID {
			t.Fatalf("feed %+v", ms)
		}
		// Carol is in no group: an empty feed, never null.
		res := e.MustOK(t, g("feed", "--identity", carol.Nsec)...)
		if strings.TrimSpace(res.Stdout) != "[]" {
			t.Fatalf("carol's feed %q", res.Stdout)
		}
	})

	t.Run("follow", func(t *testing.T) {
		p := e.StartStream(t, g("read", "chat", "-f", "--signer", s.URI)...)
		var m groupMessage
		for i := 0; i < 3; i++ {
			p.Next(t, &m)
		}
		e.MustOK(t, g("send", "chat", "live", "--identity", alice.Nsec)...)
		p.Next(t, &m)
		if m.Content != "live" {
			t.Fatalf("live %+v", m)
		}
		p.Quiet(t, 500*time.Millisecond)
	})

	t.Run("NCLI_SIGNER stands in for --signer", func(t *testing.T) {
		env := NewEnv(t)
		env.Setenv("NCLI_SIGNER", s.URI)
		var rep groupReport
		env.MustOK(t, g("send", "chat", "via env")...).JSON(t, &rep)
		var ms []groupMessage
		e.MustOK(t, g("read", "chat", "--identity", alice.Nsec)...).JSON(t, &ms)
		found := false
		for _, m := range ms {
			found = found || (m.Content == "via env" && m.From == agent.PubHex)
		}
		if !found {
			t.Fatalf("message via NCLI_SIGNER missing: %+v", ms)
		}
	})

	t.Run("text output", func(t *testing.T) {
		res := e.MustOK(t, "groups", "read", "chat", "--relay", r.URL, "--signer", s.URI)
		if !strings.Contains(res.Stdout, "standup at 10") || !strings.Contains(res.Stdout, "in chat") {
			t.Fatalf("text\n%s", res)
		}
	})

	t.Run("errors", func(t *testing.T) {
		e.Run(t, g("send", "chat", "x", "--identity", alice.Nsec, "--signer", s.URI)...).ExpectErr(t, "usage")
		e.Run(t, g("feed")...).ExpectErr(t, "usage")
		e.Run(t, g("send", "chat", " ", "--identity", alice.Nsec)...).ExpectErr(t, "invalid_input")
		e.Run(t, g("members", "add", "chat", "no-such-label", "--identity", alice.Nsec)...).ExpectErr(t, "invalid_input")
		// The agent's policy has no rule for a group edit.
		e.Run(t, g("edit", "chat", "--name", "x", "--signer", s.URI)...).ExpectErr(t, "auth")
	})
}
