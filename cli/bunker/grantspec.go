// grantspec.go implements the `kind: bunker` YAML spec: a declarative list
// of grants to apply automatically to whichever app completes one specific
// pairing attempt (`ncli bunker connect --grants <file>`), instead of
// clicking through openApprovalDialog/openDurationDialog once per method
// the first time each one is used. Same envelope shape (`kind:`/`spec:`)
// and loading conventions (sigs.k8s.io/yaml, UnmarshalJSON-driven
// validation, strict-unknown-field rejection) as client/spec.go's own
// stream/inspect/targets/sync kinds -- not wired into that package's
// RootSpec union, since a bunker spec isn't something `ncli apply` ever
// runs, but deliberately readable the same way by a human editing it by
// hand or an agent generating one from a NIP-46 method list it already
// knows.
package bunker

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip46"
	"sigs.k8s.io/yaml"
)

// grantableMethods is every NIP-46 method a GrantEntrySpec may name.
// MethodConnect is deliberately excluded -- see GrantEntrySpec.UnmarshalJSON's
// own doc comment for why granting it would be a no-op at best.
var grantableMethods = map[string]bool{
	nip46.MethodSignEvent:    true,
	nip46.MethodPing:         true,
	nip46.MethodGetPublicKey: true,
	nip46.MethodGetRelays:    true,
	nip46.MethodNIP04Encrypt: true,
	nip46.MethodNIP04Decrypt: true,
	nip46.MethodNIP44Encrypt: true,
	nip46.MethodNIP44Decrypt: true,
}

// KindsSpec is a sign_event grant entry's kind scope: either every kind
// (except the always-sensitive ones -- see policy.go's sensitiveKinds,
// which Store.Decide enforces regardless of what's granted here) via the
// literal string "any", or an explicit, non-empty list of exact kind
// numbers. Two shapes over one YAML field (a bare string or a list),
// unmarshaled the same way FlowSpec's own bare-string-or-object shorthand
// is in client/spec.go.
type KindsSpec struct {
	Any   bool
	Kinds []int
}

// MarshalJSON is the inverse of UnmarshalJSON's own two shapes --
// required for a real round-trip, not just load-from-file: ipc_client.go
// sends a *GrantSpec (unresolved -- see GrantSpec.Resolve's own doc
// comment) to the daemon as plain JSON, and default struct marshaling
// would produce `{"Any":...,"Kinds":...}`, a shape UnmarshalJSON's own
// bare-string-or-list parser rejects on the server side's re-unmarshal.
func (k KindsSpec) MarshalJSON() ([]byte, error) {
	if k.Any {
		return json.Marshal("any")
	}
	return json.Marshal(k.Kinds)
}

func (k *KindsSpec) UnmarshalJSON(data []byte) error {
	// "null" is what MarshalJSON's own json.Marshal(k.Kinds) produces for
	// a zero-value KindsSpec (every non-sign_event grant entry) -- a
	// no-op, not an error: it means "not set," the same as the field
	// being absent from the source YAML entirely (the only shape a real
	// spec file ever has; this one only shows up coming back from
	// GrantSpec's own IPC round trip -- see MarshalJSON's doc comment).
	if string(data) == "null" {
		*k = KindsSpec{}
		return nil
	}

	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		if !strings.EqualFold(s, "any") {
			return fmt.Errorf(`invalid kinds %q -- must be "any" or a list of kind numbers`, s)
		}
		k.Any = true
		return nil
	}

	var list []int
	if err := json.Unmarshal(data, &list); err != nil {
		return errors.New(`invalid kinds -- must be "any" or a list of kind numbers`)
	}
	if len(list) == 0 {
		return errors.New("kinds must not be empty -- use \"any\" for every non-sensitive kind")
	}
	k.Kinds = list
	return nil
}

// isZero reports whether k was never set at all -- the state every
// non-sign_event GrantEntrySpec's Kinds field must stay in (kind is
// meaningless for any other method), distinct from an explicit but empty
// value, which UnmarshalJSON above already refuses to produce.
func (k KindsSpec) isZero() bool {
	return !k.Any && k.Kinds == nil
}

// GrantEntrySpec is one entry in a GrantSpec's grants list -- the YAML
// counterpart of one Grant (policy.go), before CreatedAt/ExpiresAt are
// resolved against a real clock (see GrantSpec.Resolve).
type GrantEntrySpec struct {
	Method  string    `json:"method" yaml:"method"`
	Kinds   KindsSpec `json:"kinds" yaml:"kinds"`
	Verdict string    `json:"verdict,omitempty" yaml:"verdict,omitempty"`
	// Expires and Uses are mutually exclusive; neither set means "until
	// revoked" (GrantForever's own default). Expires is a duration string
	// in the same unit vocabulary as every other ncli spec's duration
	// fields (client.ParseDuration -- "24h", "7d", "2w", ...), not a
	// fixed timestamp: it's resolved against the moment the app actually
	// pairs (see GrantSpec.Resolve), not against whenever this file was
	// written or `ncli bunker connect` was run, so a bunker:// URI that
	// sits unused for a while doesn't eat into its own eventual grant's
	// clock.
	Expires string `json:"expires,omitempty" yaml:"expires,omitempty"`
	Uses    int    `json:"uses,omitempty" yaml:"uses,omitempty"`

	// expiresDur is Expires, pre-parsed at load time so Resolve never
	// needs to re-validate/re-report a parse error at pairing time --
	// which, for the bunker:// direction, can be minutes after Connect
	// actually loaded and validated this file.
	expiresDur time.Duration
}

func (e *GrantEntrySpec) UnmarshalJSON(data []byte) error {
	type Alias GrantEntrySpec
	var temp Alias
	if err := yaml.UnmarshalStrict(data, &temp); err != nil {
		return err
	}

	if temp.Method == "" {
		return errors.New("grant entry missing `method`")
	}
	if temp.Method == nip46.MethodConnect {
		return errors.New(`granting "connect" has no effect -- pairing is already gated by the bunker://` +
			" URI's own single-use secret (or the nostrconnect:// handshake), not by a remembered grant")
	}
	if !grantableMethods[temp.Method] {
		return fmt.Errorf("unknown method %q", temp.Method)
	}

	if temp.Method == nip46.MethodSignEvent {
		if temp.Kinds.isZero() {
			return errors.New(`sign_event grant missing "kinds" -- name exact kinds, or "any" for every non-sensitive kind`)
		}
	} else if !temp.Kinds.isZero() {
		return fmt.Errorf("kinds is only meaningful for method: sign_event, not %q", temp.Method)
	}

	switch strings.ToLower(temp.Verdict) {
	case "", "allow":
		temp.Verdict = "allow"
	case "deny":
		temp.Verdict = "deny"
	default:
		return fmt.Errorf(`invalid verdict %q -- must be "allow" or "deny"`, temp.Verdict)
	}

	if temp.Expires != "" && temp.Uses != 0 {
		return errors.New("expires and uses are mutually exclusive on one grant entry")
	}
	if temp.Expires != "" {
		dur, err := client.ParseDuration(temp.Expires)
		if err != nil {
			return fmt.Errorf("invalid expires %q: %w", temp.Expires, err)
		}
		if dur <= 0 {
			return fmt.Errorf("expires %q must be positive", temp.Expires)
		}
		temp.expiresDur = dur
	}
	if temp.Uses < 0 {
		return fmt.Errorf("uses must be positive, got %d", temp.Uses)
	}

	*e = GrantEntrySpec(temp)
	return nil
}

// scopeKeys returns the distinct (method, kind) scope this entry covers --
// one key for every method, plus one per named kind (or a single "any" key)
// for sign_event -- the unit GrantSpec's own duplicate-scope check
// operates on, and Resolve's own expansion into one Grant per key.
func (e *GrantEntrySpec) scopeKeys() []string {
	if e.Method != nip46.MethodSignEvent {
		return []string{e.Method}
	}
	if e.Kinds.Any {
		return []string{e.Method + ":any"}
	}
	keys := make([]string, len(e.Kinds.Kinds))
	for i, k := range e.Kinds.Kinds {
		keys[i] = fmt.Sprintf("%s:kind:%d", e.Method, k)
	}
	return keys
}

// GrantSpec is a `kind: bunker` file's `spec:` block -- see LoadGrantSpec.
type GrantSpec struct {
	// Nickname, if set, becomes the paired app's Session.Nickname the
	// moment it pairs -- see Store.SetName's own doc comment for what
	// that changes (every trusted-app display, TUI and CLI alike).
	// Optional: omitting it leaves the app to fall back to its own
	// self-reported name (nostrconnect:// only) or pubkey, same as any
	// other pairing.
	Nickname string           `json:"nickname,omitempty" yaml:"nickname,omitempty"`
	Grants   []GrantEntrySpec `json:"grants" yaml:"grants"`
}

func (s *GrantSpec) UnmarshalJSON(data []byte) error {
	type Alias GrantSpec
	var temp Alias
	if err := yaml.UnmarshalStrict(data, &temp); err != nil {
		return err
	}

	if len(temp.Grants) == 0 {
		return errors.New("spec.grants must not be empty")
	}

	seen := map[string]bool{}
	for i := range temp.Grants {
		for _, key := range temp.Grants[i].scopeKeys() {
			if seen[key] {
				return fmt.Errorf("duplicate grant for %s -- each method/kind scope may appear at most once", key)
			}
			seen[key] = true
		}
	}

	*s = GrantSpec(temp)
	return nil
}

// grantSpecEnvelope is the `kind:`/`spec:` wrapper every ncli YAML spec
// uses (see client.RootSpec) -- reimplemented locally rather than folded
// into that type's own kind switch, since a bunker spec is read by
// `ncli bunker connect`, never by `ncli apply`.
type grantSpecEnvelope struct {
	Kind string    `json:"kind" yaml:"kind"`
	Spec GrantSpec `json:"spec" yaml:"spec"`
}

// LoadGrantSpec reads and validates a `kind: bunker` YAML file at path.
// Every validation error (unknown method, "connect" named explicitly,
// missing/misplaced kinds, bad verdict, unparseable or non-mutually-
// exclusive expires/uses, duplicate scopes, unknown YAML fields) is
// caught here, at load time -- Resolve (called once the app this spec is
// for actually pairs, possibly much later for the bunker:// direction)
// never fails.
func LoadGrantSpec(path string) (*GrantSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var env grantSpecEnvelope
	if err := yaml.UnmarshalStrict(data, &env); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", path, err)
	}
	if !strings.EqualFold(env.Kind, "bunker") {
		return nil, fmt.Errorf(`%s: kind must be "bunker", got %q`, path, env.Kind)
	}
	return &env.Spec, nil
}

// Resolve expands s into concrete Grants, with CreatedAt/ExpiresAt
// computed against now -- the moment the app this spec is for actually
// pairs (handler.go's execute/daemon.go's InitiateNostrconnectWithGrants),
// not whenever this spec was loaded or `ncli bunker connect` was run. One
// GrantEntrySpec naming several sign_event kinds expands to one Grant per
// kind, since Store's own model is per-(method, kind).
func (s *GrantSpec) Resolve(now time.Time) []Grant {
	var grants []Grant
	for _, e := range s.Grants {
		verdict := Allow
		if e.Verdict == "deny" {
			verdict = Deny
		}

		kinds := []*int{nil}
		if e.Method == nip46.MethodSignEvent && !e.Kinds.Any {
			kinds = make([]*int, len(e.Kinds.Kinds))
			for i, k := range e.Kinds.Kinds {
				kinds[i] = &k
			}
		}

		for _, kind := range kinds {
			g := newGrant(e.Method, verdict, kind, now)
			switch {
			case e.expiresDur > 0:
				exp := now.Add(e.expiresDur)
				g.ExpiresAt = &exp
			case e.Uses > 0:
				uses := e.Uses
				g.RemainingUses = &uses
			}
			grants = append(grants, g)
		}
	}
	return grants
}
