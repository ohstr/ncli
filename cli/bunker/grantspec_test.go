package bunker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip46"
)

func writeSpecFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spec.yaml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadGrantSpec_Valid(t *testing.T) {
	path := writeSpecFile(t, `
kind: bunker
spec:
  nickname: "My App"
  grants:
    - method: ping
    - method: sign_event
      kinds: [1, 7]
      expires: 24h
    - method: sign_event
      kinds: any
      uses: 10
    - method: sign_event
      kinds: [3]
      verdict: deny
`)
	spec, err := LoadGrantSpec(path)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Nickname != "My App" {
		t.Errorf("Nickname = %q, want %q", spec.Nickname, "My App")
	}
	if len(spec.Grants) != 4 {
		t.Fatalf("len(Grants) = %d, want 4", len(spec.Grants))
	}
}

func TestLoadGrantSpec_WrongKindRejected(t *testing.T) {
	path := writeSpecFile(t, `
kind: stream
spec:
  grants:
    - method: ping
`)
	if _, err := LoadGrantSpec(path); err == nil {
		t.Error("expected an error for kind != bunker")
	}
}

func TestLoadGrantSpec_MissingFileRejected(t *testing.T) {
	if _, err := LoadGrantSpec(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Error("expected an error for a missing file")
	}
}

func TestLoadGrantSpec_EmptyGrantsRejected(t *testing.T) {
	path := writeSpecFile(t, `
kind: bunker
spec:
  grants: []
`)
	if _, err := LoadGrantSpec(path); err == nil {
		t.Error("expected an error for an empty grants list")
	}
}

func TestLoadGrantSpec_ConnectMethodRejected(t *testing.T) {
	path := writeSpecFile(t, `
kind: bunker
spec:
  grants:
    - method: connect
`)
	if _, err := LoadGrantSpec(path); err == nil {
		t.Error("expected an error for method: connect")
	}
}

func TestLoadGrantSpec_UnknownMethodRejected(t *testing.T) {
	path := writeSpecFile(t, `
kind: bunker
spec:
  grants:
    - method: delete_everything
`)
	if _, err := LoadGrantSpec(path); err == nil {
		t.Error("expected an error for an unknown method")
	}
}

func TestLoadGrantSpec_SignEventMissingKindsRejected(t *testing.T) {
	path := writeSpecFile(t, `
kind: bunker
spec:
  grants:
    - method: sign_event
`)
	if _, err := LoadGrantSpec(path); err == nil {
		t.Error("expected an error for sign_event with no kinds")
	}
}

func TestLoadGrantSpec_KindsOnNonSignEventRejected(t *testing.T) {
	path := writeSpecFile(t, `
kind: bunker
spec:
  grants:
    - method: ping
      kinds: [1]
`)
	if _, err := LoadGrantSpec(path); err == nil {
		t.Error("expected an error for kinds on a non-sign_event method")
	}
}

func TestLoadGrantSpec_KindsEmptyListRejected(t *testing.T) {
	path := writeSpecFile(t, `
kind: bunker
spec:
  grants:
    - method: sign_event
      kinds: []
`)
	if _, err := LoadGrantSpec(path); err == nil {
		t.Error("expected an error for an empty kinds list")
	}
}

func TestLoadGrantSpec_KindsInvalidStringRejected(t *testing.T) {
	path := writeSpecFile(t, `
kind: bunker
spec:
  grants:
    - method: sign_event
      kinds: sometimes
`)
	if _, err := LoadGrantSpec(path); err == nil {
		t.Error(`expected an error for kinds: "sometimes" (must be "any" or a list)`)
	}
}

func TestLoadGrantSpec_InvalidVerdictRejected(t *testing.T) {
	path := writeSpecFile(t, `
kind: bunker
spec:
  grants:
    - method: ping
      verdict: maybe
`)
	if _, err := LoadGrantSpec(path); err == nil {
		t.Error("expected an error for an invalid verdict")
	}
}

func TestLoadGrantSpec_ExpiresAndUsesMutuallyExclusive(t *testing.T) {
	path := writeSpecFile(t, `
kind: bunker
spec:
  grants:
    - method: ping
      expires: 24h
      uses: 5
`)
	if _, err := LoadGrantSpec(path); err == nil {
		t.Error("expected an error for expires+uses on the same entry")
	}
}

func TestLoadGrantSpec_InvalidExpiresRejected(t *testing.T) {
	path := writeSpecFile(t, `
kind: bunker
spec:
  grants:
    - method: ping
      expires: "not a duration"
`)
	if _, err := LoadGrantSpec(path); err == nil {
		t.Error("expected an error for an unparseable expires duration")
	}
}

func TestLoadGrantSpec_NegativeUsesRejected(t *testing.T) {
	path := writeSpecFile(t, `
kind: bunker
spec:
  grants:
    - method: ping
      uses: -1
`)
	if _, err := LoadGrantSpec(path); err == nil {
		t.Error("expected an error for negative uses")
	}
}

func TestLoadGrantSpec_DuplicateMethodRejected(t *testing.T) {
	path := writeSpecFile(t, `
kind: bunker
spec:
  grants:
    - method: ping
    - method: ping
      uses: 5
`)
	if _, err := LoadGrantSpec(path); err == nil {
		t.Error("expected an error for a method granted twice")
	}
}

func TestLoadGrantSpec_DuplicateKindRejected(t *testing.T) {
	path := writeSpecFile(t, `
kind: bunker
spec:
  grants:
    - method: sign_event
      kinds: [1, 7]
    - method: sign_event
      kinds: [7]
`)
	if _, err := LoadGrantSpec(path); err == nil {
		t.Error("expected an error for kind 7 named in two different entries")
	}
}

func TestLoadGrantSpec_AnyKindAndExactKindCoexist(t *testing.T) {
	// Different scopes -- not a duplicate, even though both cover
	// sign_event. See summarizeGrants/Store.Decide's own exact-before-any
	// precedence for why this is a meaningful, supported combination.
	path := writeSpecFile(t, `
kind: bunker
spec:
  grants:
    - method: sign_event
      kinds: [1]
      expires: 1h
    - method: sign_event
      kinds: any
      uses: 10
`)
	spec, err := LoadGrantSpec(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Grants) != 2 {
		t.Fatalf("len(Grants) = %d, want 2", len(spec.Grants))
	}
}

func TestLoadGrantSpec_UnknownFieldRejected(t *testing.T) {
	path := writeSpecFile(t, `
kind: bunker
spec:
  grants:
    - method: ping
      unknownField: true
`)
	if _, err := LoadGrantSpec(path); err == nil {
		t.Error("expected an error for an unknown field (strict unmarshal)")
	}
}

func TestGrantSpec_Resolve_Forever(t *testing.T) {
	path := writeSpecFile(t, `
kind: bunker
spec:
  grants:
    - method: ping
`)
	spec, err := LoadGrantSpec(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	grants := spec.Resolve(now)
	if len(grants) != 1 {
		t.Fatalf("len(grants) = %d, want 1", len(grants))
	}
	g := grants[0]
	if g.Method != nip46.MethodPing || g.Verdict != Allow || g.Kind != nil || g.ExpiresAt != nil || g.RemainingUses != nil {
		t.Errorf("grant = %+v, want a plain forever ping allow", g)
	}
	if !g.CreatedAt.Equal(now) {
		t.Errorf("CreatedAt = %v, want %v", g.CreatedAt, now)
	}
}

func TestGrantSpec_Resolve_DurationComputedAgainstResolveTime(t *testing.T) {
	path := writeSpecFile(t, `
kind: bunker
spec:
  grants:
    - method: ping
      expires: 24h
`)
	spec, err := LoadGrantSpec(path)
	if err != nil {
		t.Fatal(err)
	}
	// Resolve at a time far removed from load time -- the whole point of
	// deferring resolution (see GrantSpec.Resolve's own doc comment): the
	// clock starts here, not when the file was loaded.
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	grants := spec.Resolve(now)
	if len(grants) != 1 {
		t.Fatalf("len(grants) = %d, want 1", len(grants))
	}
	want := now.Add(24 * time.Hour)
	if grants[0].ExpiresAt == nil || !grants[0].ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v", grants[0].ExpiresAt, want)
	}
}

func TestGrantSpec_Resolve_Uses(t *testing.T) {
	path := writeSpecFile(t, `
kind: bunker
spec:
  grants:
    - method: ping
      uses: 7
`)
	spec, err := LoadGrantSpec(path)
	if err != nil {
		t.Fatal(err)
	}
	grants := spec.Resolve(time.Now())
	if len(grants) != 1 || grants[0].RemainingUses == nil || *grants[0].RemainingUses != 7 {
		t.Fatalf("grants = %+v, want one grant with RemainingUses=7", grants)
	}
}

func TestGrantSpec_Resolve_Deny(t *testing.T) {
	path := writeSpecFile(t, `
kind: bunker
spec:
  grants:
    - method: sign_event
      kinds: [3]
      verdict: deny
`)
	spec, err := LoadGrantSpec(path)
	if err != nil {
		t.Fatal(err)
	}
	grants := spec.Resolve(time.Now())
	if len(grants) != 1 || grants[0].Verdict != Deny {
		t.Fatalf("grants = %+v, want one Deny grant", grants)
	}
	if grants[0].Kind == nil || *grants[0].Kind != 3 {
		t.Fatalf("Kind = %v, want *3", grants[0].Kind)
	}
}

// TestGrantSpec_Resolve_KindsListExpandsToOneGrantPerKind guards the one
// piece of real translation Resolve does: a single YAML entry naming
// several kinds becomes several Grants, since Store's own model is
// per-(method, kind), not per-entry.
func TestGrantSpec_Resolve_KindsListExpandsToOneGrantPerKind(t *testing.T) {
	path := writeSpecFile(t, `
kind: bunker
spec:
  grants:
    - method: sign_event
      kinds: [1, 6, 7]
      expires: 30d
`)
	spec, err := LoadGrantSpec(path)
	if err != nil {
		t.Fatal(err)
	}
	grants := spec.Resolve(time.Now())
	if len(grants) != 3 {
		t.Fatalf("len(grants) = %d, want 3 (one per kind)", len(grants))
	}
	seen := map[int]bool{}
	for _, g := range grants {
		if g.Method != nip46.MethodSignEvent || g.Kind == nil || g.ExpiresAt == nil {
			t.Fatalf("grant = %+v, want a sign_event kind grant with an expiry", g)
		}
		seen[*g.Kind] = true
	}
	for _, want := range []int{1, 6, 7} {
		if !seen[want] {
			t.Errorf("kind %d missing from resolved grants %+v", want, grants)
		}
	}
}

func TestGrantSpec_Resolve_AnyKindHasNilKind(t *testing.T) {
	path := writeSpecFile(t, `
kind: bunker
spec:
  grants:
    - method: sign_event
      kinds: any
`)
	spec, err := LoadGrantSpec(path)
	if err != nil {
		t.Fatal(err)
	}
	grants := spec.Resolve(time.Now())
	if len(grants) != 1 || grants[0].Kind != nil {
		t.Fatalf("grants = %+v, want one grant with a nil Kind (any-kind)", grants)
	}
}

// TestGrantSpec_IPCRoundTrip guards KindsSpec's Marshal/Unmarshal
// symmetry -- ipc_client.go sends a *GrantSpec as plain JSON over the
// control socket, and the server re-unmarshals it through the same
// validating UnmarshalJSON a file load uses (see ipc_server.go's
// GrantSpec field). Default struct marshaling of KindsSpec's own
// (unexported-shape) fields would produce an object UnmarshalJSON's
// bare-string-or-list parser rejects -- this is what MarshalJSON exists
// to prevent.
func TestGrantSpec_IPCRoundTrip(t *testing.T) {
	path := writeSpecFile(t, `
kind: bunker
spec:
  nickname: "Round Trip App"
  grants:
    - method: ping
    - method: sign_event
      kinds: [1, 7]
      expires: 24h
    - method: sign_event
      kinds: any
      uses: 3
    - method: sign_event
      kinds: [3]
      verdict: deny
`)
	spec, err := LoadGrantSpec(path)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var roundTripped GrantSpec
	if err := json.Unmarshal(raw, &roundTripped); err != nil {
		t.Fatalf("Unmarshal() error = %v (raw = %s)", err, raw)
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	want := spec.Resolve(now)
	got := roundTripped.Resolve(now)
	if len(got) != len(want) {
		t.Fatalf("resolved grant count after round trip = %d, want %d", len(got), len(want))
	}
	if roundTripped.Nickname != spec.Nickname {
		t.Errorf("Nickname after round trip = %q, want %q", roundTripped.Nickname, spec.Nickname)
	}
}

// TestLoadGrantSpec_Examples guards the example files under examples/
// bunker/ against ever silently drifting out of sync with grantspec.go's
// own validation rules -- these are the first thing a human or an agent
// reading this feature's docs will copy.
func TestLoadGrantSpec_Examples(t *testing.T) {
	for _, name := range []string{"note-client.yaml", "agent.yaml"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("..", "..", "examples", "bunker", name)
			spec, err := LoadGrantSpec(path)
			if err != nil {
				t.Fatalf("LoadGrantSpec(%s) error = %v", path, err)
			}
			if len(spec.Grants) == 0 {
				t.Errorf("%s: parsed to zero grants", name)
			}
			if grants := spec.Resolve(time.Now()); len(grants) == 0 {
				t.Errorf("%s: Resolve() produced zero grants", name)
			}
		})
	}
}
