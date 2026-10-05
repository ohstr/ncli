package keyresolve

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

// addSoleVaultEntry creates a vault and saves one entry under label,
// setting vault.password so ResolveSigningKey's unlock doesn't try to
// prompt. Returns the entry's own private key, for asserting against.
func addSoleVaultEntry(t *testing.T, label string) string {
	t.Helper()
	_, vaultPrivHex, err := client.CreateVaultIdentity("hunter2")
	if err != nil {
		t.Fatalf("CreateVaultIdentity() error = %v", err)
	}
	id, err := client.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity() error = %v", err)
	}
	if _, err := client.AddVaultEntry(vaultPrivHex, label, id.PrivKeyHex); err != nil {
		t.Fatalf("AddVaultEntry() error = %v", err)
	}
	viper.Set("vault.password", "hunter2")
	t.Cleanup(viper.Reset)
	return id.PrivKeyHex
}

func TestResolveIdentity_ExplicitNsec(t *testing.T) {
	withTempConfigDir(t)
	id, err := client.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity() error = %v", err)
	}

	got, err := ResolveIdentity(newTestCmd(), id.Nsec, "test.identity")
	if err != nil {
		t.Fatalf("ResolveIdentity() error = %v", err)
	}
	if got != id.PrivKeyHex {
		t.Fatalf("ResolveIdentity() = %q, want %q", got, id.PrivKeyHex)
	}
}

func TestResolveIdentity_NoIdentityNoVault(t *testing.T) {
	withTempConfigDir(t)

	if _, err := ResolveIdentity(newTestCmd(), "", "test.identity"); err == nil {
		t.Fatal("ResolveIdentity() with no flag/config/vault = nil error, want an error")
	}
}

func TestResolveIdentity_SoleVaultEntryAutoDetected(t *testing.T) {
	withTempConfigDir(t)
	wantPriv := addSoleVaultEntry(t, "alice")

	got, err := ResolveIdentity(newTestCmd(), "", "test.identity")
	if err != nil {
		t.Fatalf("ResolveIdentity() error = %v", err)
	}
	if got != wantPriv {
		t.Fatalf("ResolveIdentity() = %q, want %q", got, wantPriv)
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

	if _, err := ResolveIdentity(newTestCmd(), "", "test.identity"); err == nil {
		t.Fatal("ResolveIdentity() with 2 vault entries and no flag/config = nil error, want an error")
	}
}

func TestResolveIdentityOptional_NoIdentityNoVault(t *testing.T) {
	withTempConfigDir(t)

	got, err := ResolveIdentityOptional(newTestCmd(), "", "test.identity", false)
	if err != nil {
		t.Fatalf("ResolveIdentityOptional() error = %v, want nil", err)
	}
	if got != "" {
		t.Fatalf("ResolveIdentityOptional() = %q, want \"\"", got)
	}
}

func TestResolveIdentityOptional_AmbiguousVaultEntriesSkipped(t *testing.T) {
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

	got, err := ResolveIdentityOptional(newTestCmd(), "", "test.identity", false)
	if err != nil {
		t.Fatalf("ResolveIdentityOptional() with ambiguous vault error = %v, want nil (skip, don't block)", err)
	}
	if got != "" {
		t.Fatalf("ResolveIdentityOptional() = %q, want \"\" (ambiguous vault should be skipped, not guessed)", got)
	}
}

func TestResolveIdentityOptional_SoleVaultEntryAutoDetected(t *testing.T) {
	withTempConfigDir(t)
	wantPriv := addSoleVaultEntry(t, "alice")

	got, err := ResolveIdentityOptional(newTestCmd(), "", "test.identity", false)
	if err != nil {
		t.Fatalf("ResolveIdentityOptional() error = %v", err)
	}
	if got != wantPriv {
		t.Fatalf("ResolveIdentityOptional() = %q, want %q", got, wantPriv)
	}
}

func TestResolveIdentityOptional_ExplicitBadIdentityStillErrors(t *testing.T) {
	withTempConfigDir(t)

	if _, err := ResolveIdentityOptional(newTestCmd(), "not-a-real-identity-at-all", "test.identity", false); err == nil {
		t.Fatal("ResolveIdentityOptional() with an explicit but unresolvable --identity = nil error, want an error (typos must not be swallowed)")
	}
}

func TestResolveIdentityOptional_ExplicitViaConfigKeyStillErrors(t *testing.T) {
	withTempConfigDir(t)
	viper.Set("test.identity", "not-a-real-identity-at-all")
	t.Cleanup(viper.Reset)

	if _, err := ResolveIdentityOptional(newTestCmd(), "", "test.identity", false); err == nil {
		t.Fatal("ResolveIdentityOptional() with an explicit but unresolvable config identity = nil error, want an error")
	}
}
