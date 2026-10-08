package climatrix

import (
	"strings"
	"testing"
)

// id --save keeps the private key in the vault: it isn't printed unless
// --reveal asks for it, and the vault still has it.
func TestIDSaveHidesKey(t *testing.T) {
	e := NewEnv(t)

	var saved map[string]any
	e.MustOK(t, "id", "--save", "--label", "quiet", "--json").JSON(t, &saved)
	if saved["saved"] != true || saved["npub"] == nil || saved["pub_hex"] == nil {
		t.Fatalf("save result: %v", saved)
	}
	if _, ok := saved["nsec"]; ok {
		t.Errorf("--save printed the nsec: %v", saved)
	}
	if _, ok := saved["priv_hex"]; ok {
		t.Errorf("--save printed priv_hex: %v", saved)
	}

	var revealed struct {
		Nsec string `json:"nsec"`
	}
	e.MustOK(t, "id", "quiet", "--reveal", "--json").JSON(t, &revealed)
	if !strings.HasPrefix(revealed.Nsec, "nsec1") {
		t.Fatalf("vault lost the key: %+v", revealed)
	}

	var both struct {
		Nsec    string `json:"nsec"`
		PrivHex string `json:"priv_hex"`
		Label   string `json:"label"`
	}
	e.MustOK(t, "id", "--save", "--label", "loud", "--reveal", "--json").JSON(t, &both)
	if !strings.HasPrefix(both.Nsec, "nsec1") || len(both.PrivHex) != 64 || both.Label != "loud" {
		t.Fatalf("--save --reveal = %+v", both)
	}
	var again struct {
		Nsec string `json:"nsec"`
	}
	e.MustOK(t, "id", "loud", "--reveal", "--json").JSON(t, &again)
	if again.Nsec != both.Nsec {
		t.Fatal("--save --reveal printed a different key than it saved")
	}

	text := e.MustOK(t, "id", "--save", "--label", "plain")
	if strings.Contains(text.Stdout, "nsec1") || strings.Contains(text.Stdout, "privkey") {
		t.Errorf("text --save printed the key:\n%s", text.Stdout)
	}
	if !strings.Contains(text.Stdout, "npub1") || !strings.Contains(text.Stdout, "label: plain") {
		t.Errorf("text --save output:\n%s", text.Stdout)
	}
	if !strings.Contains(text.Stderr, "ncli id plain --reveal") {
		t.Errorf("no hint on how to reveal:\n%s", text.Stderr)
	}
}
