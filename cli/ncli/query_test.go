package ncli

import (
	"testing"

	"github.com/ohstr/ncli/client"
	"github.com/spf13/cobra"
)

func newTestQueryAuthCmd(t *testing.T) *cobra.Command {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cmd := &cobra.Command{Use: "test"}
	registerQueryAuthFlag(cmd)
	cmd.Flags().Bool("json", false, "")
	return cmd
}

// TestResolveQueryIdentity_NoneGivenIsOptional covers find/dump's "no
// behavior change without --auth-identity" contract at the flag-wiring
// layer: no flag, no config, no vault entry to fall back to must resolve
// to "" with no error, not the required-identity error
// keyresolve.ResolveIdentity itself would return.
func TestResolveQueryIdentity_NoneGivenIsOptional(t *testing.T) {
	cmd := newTestQueryAuthCmd(t)

	got, err := resolveQueryIdentity(cmd)
	if err != nil {
		t.Fatalf("resolveQueryIdentity() error = %v, want nil", err)
	}
	if got != "" {
		t.Fatalf("resolveQueryIdentity() = %q, want \"\"", got)
	}
}

// TestResolveQueryIdentity_ExplicitFlagResolves covers the flag actually
// reaching keyresolve.ResolveIdentityOptional under the name this package
// uses for it (auth-identity, not identity -- miner check already owns
// --identity for an unrelated purpose).
func TestResolveQueryIdentity_ExplicitFlagResolves(t *testing.T) {
	cmd := newTestQueryAuthCmd(t)

	id, err := client.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity() error = %v", err)
	}
	mustSet(t, cmd, "auth-identity", id.Nsec)

	got, err := resolveQueryIdentity(cmd)
	if err != nil {
		t.Fatalf("resolveQueryIdentity() error = %v, want nil", err)
	}
	if got != id.PrivKeyHex {
		t.Fatalf("resolveQueryIdentity() = %q, want %q", got, id.PrivKeyHex)
	}
}

// TestResolveQueryIdentity_ExplicitBadFlagErrors covers the other half:
// unlike "nothing given," an explicitly-given identity that fails to
// resolve must still surface as an error, not be silently swallowed into
// "".
func TestResolveQueryIdentity_ExplicitBadFlagErrors(t *testing.T) {
	cmd := newTestQueryAuthCmd(t)
	mustSet(t, cmd, "auth-identity", "not-a-real-identity")

	if _, err := resolveQueryIdentity(cmd); err == nil {
		t.Fatal("resolveQueryIdentity() with a malformed --auth-identity error = nil, want an error")
	}
}

// TestRegisterQueryAuthFlag_DoesNotCollideWithMinerChecksIdentityFlag is a
// regression guard: registerQueryFlags (shared by find/dump/miner check)
// must never itself register "identity" -- miner check already owns that
// name for an unrelated purpose (an author to restrict a live check to,
// a public key only), registered separately in its own init(). Registering
// --auth-identity on find/dump's own flag sets, outside registerQueryFlags,
// is what keeps the two from colliding; this locks that shape in so it
// can't regress back to a flag-redefinition panic at startup.
func TestRegisterQueryAuthFlag_DoesNotCollideWithMinerChecksIdentityFlag(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	registerQueryFlags(cmd, "")
	if cmd.Flags().Lookup("identity") != nil {
		t.Fatal("registerQueryFlags registered \"identity\" -- this collides with miner check's own --identity flag when both are registered on the same command")
	}

	registerQueryAuthFlag(cmd)
	if cmd.Flags().Lookup("auth-identity") == nil {
		t.Fatal("registerQueryAuthFlag did not register --auth-identity")
	}
}
