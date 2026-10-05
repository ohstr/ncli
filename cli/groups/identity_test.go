package groups

import (
	"testing"

	"github.com/ohstr/ncli/client"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func withTempConfigDir(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func newTestCmd() *cobra.Command {
	return &cobra.Command{Use: "test"}
}

func TestResolveIdentity_ExplicitNsec(t *testing.T) {
	withTempConfigDir(t)
	id, err := client.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity() error = %v", err)
	}

	pubKeyHex, privKeyHex, err := resolveIdentity(newTestCmd(), id.Nsec)
	if err != nil {
		t.Fatalf("resolveIdentity() error = %v", err)
	}
	if privKeyHex != id.PrivKeyHex {
		t.Fatalf("resolveIdentity() privKeyHex = %q, want %q", privKeyHex, id.PrivKeyHex)
	}
	if pubKeyHex != id.PubKeyHex {
		t.Fatalf("resolveIdentity() pubKeyHex = %q, want %q", pubKeyHex, id.PubKeyHex)
	}
}

func TestResolveIdentity_NoIdentityNoVault(t *testing.T) {
	withTempConfigDir(t)

	if _, _, err := resolveIdentity(newTestCmd(), ""); err == nil {
		t.Fatal("resolveIdentity() with no flag/config/vault = nil error, want an error")
	}
}

func TestResolveIdentity_SoleVaultEntryAutoDetected(t *testing.T) {
	withTempConfigDir(t)
	_, vaultPrivHex, err := client.CreateVaultIdentity("hunter2")
	if err != nil {
		t.Fatalf("CreateVaultIdentity() error = %v", err)
	}
	id, err := client.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity() error = %v", err)
	}
	if _, err := client.AddVaultEntry(vaultPrivHex, "alice", id.PrivKeyHex); err != nil {
		t.Fatalf("AddVaultEntry() error = %v", err)
	}
	viper.Set("vault.password", "hunter2")
	t.Cleanup(viper.Reset)

	pubKeyHex, privKeyHex, err := resolveIdentity(newTestCmd(), "")
	if err != nil {
		t.Fatalf("resolveIdentity() error = %v", err)
	}
	if privKeyHex != id.PrivKeyHex {
		t.Fatalf("resolveIdentity() privKeyHex = %q, want %q", privKeyHex, id.PrivKeyHex)
	}
	if pubKeyHex != id.PubKeyHex {
		t.Fatalf("resolveIdentity() pubKeyHex = %q, want %q", pubKeyHex, id.PubKeyHex)
	}
}

func TestResolveIdentity_AmbiguousVaultEntries(t *testing.T) {
	withTempConfigDir(t)
	_, vaultPrivHex, err := client.CreateVaultIdentity("hunter2")
	if err != nil {
		t.Fatalf("CreateVaultIdentity() error = %v", err)
	}
	for _, label := range []string{"alice", "bob"} {
		id, err := client.GenerateIdentity()
		if err != nil {
			t.Fatalf("GenerateIdentity() error = %v", err)
		}
		if _, err := client.AddVaultEntry(vaultPrivHex, label, id.PrivKeyHex); err != nil {
			t.Fatalf("AddVaultEntry() error = %v", err)
		}
	}

	if _, _, err := resolveIdentity(newTestCmd(), ""); err == nil {
		t.Fatal("resolveIdentity() with 2 vault entries and no flag = nil error, want an error")
	}
}

// TestResolveIdentityOptional_* cover resolveIdentityOptional, the
// "groups show"/"groups list" counterpart to resolveIdentity above: a
// bonus for reads rather than a requirement for a write.

func TestResolveIdentityOptional_NoneGivenIsOptional(t *testing.T) {
	withTempConfigDir(t)

	got, err := resolveIdentityOptional(newTestCmd(), "")
	if err != nil {
		t.Fatalf("resolveIdentityOptional() error = %v, want nil", err)
	}
	if got != "" {
		t.Fatalf("resolveIdentityOptional() = %q, want \"\"", got)
	}
}

func TestResolveIdentityOptional_ExplicitFlagResolves(t *testing.T) {
	withTempConfigDir(t)
	id, err := client.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity() error = %v", err)
	}

	got, err := resolveIdentityOptional(newTestCmd(), id.Nsec)
	if err != nil {
		t.Fatalf("resolveIdentityOptional() error = %v", err)
	}
	if got != id.PrivKeyHex {
		t.Fatalf("resolveIdentityOptional() = %q, want %q", got, id.PrivKeyHex)
	}
}

func TestResolveIdentityOptional_ExplicitBadFlagErrors(t *testing.T) {
	withTempConfigDir(t)

	if _, err := resolveIdentityOptional(newTestCmd(), "not-a-real-identity"); err == nil {
		t.Fatal("resolveIdentityOptional() with a malformed identity error = nil, want an error")
	}
}
