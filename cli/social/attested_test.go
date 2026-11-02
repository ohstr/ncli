package social

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/ohstr/nmilat/nip01"
)

// signedBy is a kind-kind event by agentPriv carrying tags.
func signedBy(t *testing.T, kind int, tags ...[]string) *nip01.Event {
	t.Helper()
	ev := nip01.NewEvent(kind, "hi")
	ev.CreatedAt = 1800000000
	ev.Tags = append(ev.Tags, tags...)
	if err := ev.Sign(agentPriv); err != nil {
		t.Fatal(err)
	}
	return ev
}

func TestCheckAttestation(t *testing.T) {
	agent, owner := localSigner(t, agentPriv), localSigner(t, ownerPriv)
	tag := attest(t, ownerPriv, agent.PubKey(), "kind=1&created_at<1900000000")
	if a := CheckAttestation(signedBy(t, 1)); a != nil {
		t.Fatalf("no tag: %+v", a)
	}
	if a := CheckAttestation(signedBy(t, 1, tag)); a == nil || !a.Valid || a.Owner != owner.PubKey() {
		t.Fatalf("valid: %+v", a)
	}
	// the owner's tag, but the event breaks its conditions
	if a := CheckAttestation(signedBy(t, 7, tag)); a == nil || a.Valid || a.Owner != owner.PubKey() || !strings.Contains(a.Problem, "conditions") {
		t.Fatalf("kind 7: %+v", a)
	}
	bad := append([]string(nil), tag...)
	bad[3] = strings.Repeat("0", 128)
	if a := CheckAttestation(signedBy(t, 1, bad)); a == nil || a.Valid || a.Owner != owner.PubKey() {
		t.Fatalf("bad signature: %+v", a)
	}
	if a := CheckAttestation(signedBy(t, 1, tag, tag)); a == nil || a.Valid {
		t.Fatalf("two tags: %+v", a)
	}
	if a := CheckAttestation(signedBy(t, 1, []string{"auth", "npub1nope", "", "x"})); a == nil || a.Valid || a.Owner != "" {
		t.Fatalf("no owner key: %+v", a)
	}
}

func TestAttestViewOf(t *testing.T) {
	owner := localSigner(t, ownerPriv)
	cmd := &cobra.Command{Use: "x"}
	AddAttestFlags(cmd)
	if v, err := AttestViewOf(cmd); err != nil || len(v.Owners) != 0 || v.Word != "agent" {
		t.Fatalf("defaults: %+v %v", v, err)
	}
	_ = cmd.Flags().Set("trust-owners", Npub(owner.PubKey()))
	_ = cmd.Flags().Set("attest-label", "bot-2")
	if v, err := AttestViewOf(cmd); err != nil || !v.Owners[owner.PubKey()] || v.Word != "bot-2" {
		t.Fatalf("flags: %+v %v", v, err)
	}
	viper.Set("trust_owners", owner.PubKey())
	viper.Set("attest_label", "helper")
	t.Cleanup(func() { viper.Set("trust_owners", ""); viper.Set("attest_label", "") })
	env := &cobra.Command{Use: "y"}
	AddAttestFlags(env)
	if v, err := AttestViewOf(env); err != nil || !v.Owners[owner.PubKey()] || v.Word != "helper" {
		t.Fatalf("env: %+v %v", v, err)
	}
	for _, w := range []string{"Bot", "a b", "", "x" + strings.Repeat("y", 24), "agеnt", "[bot]"} {
		if w == "" {
			continue
		}
		c := &cobra.Command{Use: "z"}
		AddAttestFlags(c)
		_ = c.Flags().Set("attest-label", w)
		if _, err := AttestViewOf(c); err == nil {
			t.Errorf("--attest-label %q taken", w)
		}
	}
	c := &cobra.Command{Use: "z"}
	AddAttestFlags(c)
	_ = c.Flags().Set("trust-owners", "nope")
	if _, err := AttestViewOf(c); err == nil || !strings.Contains(err.Error(), "--trust-owners") {
		t.Fatalf("bad owner: %v", err)
	}
}

// The attestation prints in the client's column right after the author,
// before the labels and anything the author wrote; --json carries it.
func TestPrintAttestation(t *testing.T) {
	agent, owner := localSigner(t, agentPriv), localSigner(t, ownerPriv)
	tag := attest(t, ownerPriv, agent.PubKey(), "kind=14")
	rumor := nip01.NewEvent(14, "done")
	rumor.CreatedAt, rumor.PubKey, rumor.ID = 1800000000, agent.PubKey(), strings.Repeat("e", 64)
	rumor.Tags = [][]string{tag, {"subject", "[agent of " + Npub(owner.PubKey()) + "]"}}
	head := func(v AttestView) string {
		m := FromEvent(rumor, "dm")
		v.Apply(&m)
		var b bytes.Buffer
		PrintMessage(&b, m)
		return strings.Split(b.String(), "\n")[0]
	}
	who := Npub(agent.PubKey()) + "  "
	trusted := AttestView{Owners: map[string]bool{owner.PubKey(): true}, Word: "agent"}
	if h := head(trusted); !strings.Contains(h, who+"[agent of "+Npub(owner.PubKey())+"]  labels: -  [") {
		t.Fatalf("trusted: %q", h)
	}
	if h := head(AttestView{Word: "agent"}); !strings.Contains(h, who+"[attested by "+Npub(owner.PubKey())+", owner not trusted]  labels: -  [") {
		t.Fatalf("untrusted: %q", h)
	}
	// the subject spelling the column prints after "labels:", never where the column stands
	if h := head(AttestView{}); strings.Index(h, "labels: -") > strings.Index(h, "[agent of") {
		t.Fatalf("subject before the labels: %q", h)
	}
	rumor.Kind = 1 // breaks kind=14
	if h := head(trusted); !strings.Contains(h, who+"[invalid attestation]  labels: -") {
		t.Fatalf("invalid: %q", h)
	}
	rumor.Kind = 14
	m := FromEvent(rumor, "dm")
	trusted.Apply(&m)
	b, _ := json.Marshal(m)
	if !strings.Contains(string(b), `"attested_by":"`+owner.PubKey()+`","attest_valid":true,"attest_trusted":true`) {
		t.Fatalf("json: %s", b)
	}
	plain := FromEvent(signedBy(t, 1), "dm")
	if b, _ := json.Marshal(plain); strings.Contains(string(b), "attest") {
		t.Fatalf("json without a tag: %s", b)
	}
}
