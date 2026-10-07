package climatrix

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/ohstr/nmilat/nip42"
	"github.com/ohstr/nmilat/nipOA"
)

// credential is a NIP-OA auth tag: owner authorizes agentPub under
// conditions.
func credential(t *testing.T, ownerPriv, ownerPub, agentPub, conditions string) []string {
	t.Helper()
	b, err := hex.DecodeString(ownerPriv)
	if err != nil {
		t.Fatal(err)
	}
	priv, _ := btcec.PrivKeyFromBytes(b)
	digest := sha256.Sum256(nipOA.Preimage(agentPub, conditions))
	sig, err := schnorr.Sign(priv, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return []string{"auth", ownerPub, conditions, hex.EncodeToString(sig.Serialize())}
}

// authAgent answers c's challenge as agentPriv carrying tag.
func authAgent(t *testing.T, c *Raw, agentPriv string, tag []string) (bool, string) {
	t.Helper()
	ev := nip42.NewAuthEvent(c.Challenge(), c.url)
	if tag != nil {
		ev.Tags = append(ev.Tags, tag)
	}
	if err := ev.Sign(agentPriv); err != nil {
		t.Fatal(err)
	}
	return c.AuthEvent(ev)
}

func TestAgentAuth(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", map[string]any{
		"nip11":      map[string]any{"limitation": map[string]any{"auth_required": true, "membership_required": true}},
		"membership": map[string]any{"enabled": true},
		"agent_auth": map[string]any{"enabled": true, "kindEnforcement": true},
	})
	alice, agent, eve, bob := A(t, "alice"), A(t, "agent"), A(t, "eve"), A(t, "bob")
	addMember(t, r, alice.PubHex)
	future := "created_at<" + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)

	t.Run("valid credential grants virtual membership", func(t *testing.T) {
		c := Dial(t, r.URL)
		if ok, msg := authAgent(t, c, agent.PrivHex, credential(t, alice.PrivHex, alice.PubHex, agent.PubHex, future)); !ok {
			t.Fatalf("AUTH with a valid credential: %s", msg)
		}
		if ok, msg := c.Publish(Ev(t, agent.PrivHex, 1, "agent post")); !ok {
			t.Errorf("agent publish: %s", msg)
		}
		if _, closed := c.Req(F{"kinds": []int{1}}); closed != "" {
			t.Errorf("agent read refused: %s", closed)
		}
	})

	refused := func(t *testing.T, what string, tag []string) {
		t.Helper()
		c := Dial(t, r.URL)
		if ok, msg := authAgent(t, c, agent.PrivHex, tag); ok {
			t.Errorf("%s accepted (%s)", what, msg)
		}
		if ok, _ := c.Publish(Ev(t, agent.PrivHex, 1, "should not land")); ok {
			t.Errorf("after %s, agent could publish", what)
		}
	}

	t.Run("no credential", func(t *testing.T) { refused(t, "no credential", nil) })

	t.Run("tampered signature", func(t *testing.T) {
		tag := credential(t, alice.PrivHex, alice.PubHex, agent.PubHex, future)
		tag[3] = strings.Repeat("1", 128)
		refused(t, "tampered credential", tag)
	})

	t.Run("credential issued to another key", func(t *testing.T) {
		refused(t, "someone else's credential", credential(t, alice.PrivHex, alice.PubHex, bob.PubHex, future))
	})

	t.Run("conditions edited after signing", func(t *testing.T) {
		tag := credential(t, alice.PrivHex, alice.PubHex, agent.PubHex, "kind=1&"+future)
		tag[2] = future // widen to every kind, keep the old signature
		refused(t, "widened conditions", tag)
	})

	t.Run("expired", func(t *testing.T) {
		past := "created_at<" + strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
		refused(t, "expired credential", credential(t, alice.PrivHex, alice.PubHex, agent.PubHex, past))
	})

	t.Run("owner not a member", func(t *testing.T) {
		refused(t, "non-member owner", credential(t, eve.PrivHex, eve.PubHex, agent.PubHex, future))
	})

	t.Run("owner field swapped to a member", func(t *testing.T) {
		tag := credential(t, eve.PrivHex, eve.PubHex, agent.PubHex, future)
		tag[1] = alice.PubHex // claim alice issued it; signature is eve's
		refused(t, "forged owner", tag)
	})

	t.Run("kind restriction is enforced", func(t *testing.T) {
		c := Dial(t, r.URL)
		if ok, msg := authAgent(t, c, agent.PrivHex, credential(t, alice.PrivHex, alice.PubHex, agent.PubHex, "kind=1&"+future)); !ok {
			t.Fatalf("AUTH: %s", msg)
		}
		if ok, msg := c.Publish(Ev(t, agent.PrivHex, 1, "allowed kind")); !ok {
			t.Errorf("kind 1 refused: %s", msg)
		}
		if ok, _ := c.Publish(Ev(t, agent.PrivHex, 7, "+")); ok {
			t.Errorf("kind 7 accepted under a kind=1 credential")
		}
	})

	t.Run("agent can't sign for its owner", func(t *testing.T) {
		c := Dial(t, r.URL)
		authAgent(t, c, agent.PrivHex, credential(t, alice.PrivHex, alice.PubHex, agent.PubHex, future))
		if ok, _ := c.Publish(Ev(t, alice.PrivHex, 1, "as alice")); ok {
			t.Errorf("an event signed by the owner was accepted over the agent's connection")
		}
	})

	t.Run("owner removal blocks new agent connections", func(t *testing.T) {
		NewEnv(t).MustOK(t, "relay", "members", "remove", alice.PubHex, "--config", r.ConfigPath, "--json")
		refused(t, "credential from a removed owner", credential(t, alice.PrivHex, alice.PubHex, agent.PubHex, future))
	})
}
