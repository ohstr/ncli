package climatrix

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// Every examples/relay/*.yaml boots as shipped (the harness only moves its
// port, store and logs) and enforces what its NIP-11 document advertises.
func TestRelayExamples(t *testing.T) {
	needsRelay(t)
	dir := examplePath("relay")
	if d := os.Getenv("CLIMATRIX_EXAMPLES"); d != "" {
		dir = d // check another set, e.g. a branch's examples, without merging it
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if len(files) == 0 {
		t.Fatal("no examples/relay/*.yaml")
	}
	eve := A(t, "eve")
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			cfg := map[string]any{}
			if err := yaml.Unmarshal(raw, &cfg); err != nil {
				t.Fatalf("example doesn't parse: %v", err)
			}
			r := StartRelay(t, string(raw), nil)

			var doc struct {
				Limitation struct {
					AuthRequired       bool `json:"auth_required"`
					MembershipRequired bool `json:"membership_required"`
					MinPowDifficulty   int  `json:"min_pow_difficulty"`
				} `json:"limitation"`
				SupportedNIPs []any `json:"supported_nips"`
			}
			req, _ := http.NewRequest("GET", r.httpURL("/"), nil)
			req.Header.Set("Accept", "application/nostr+json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("NIP-11: %v", err)
			}
			_ = json.NewDecoder(resp.Body).Decode(&doc)
			_ = resp.Body.Close()

			want := limitsOf(cfg)
			lim := doc.Limitation
			if lim.AuthRequired != want.auth || lim.MembershipRequired != want.membership {
				t.Errorf("NIP-11 advertises auth=%v membership=%v; config says auth=%v membership=%v",
					lim.AuthRequired, lim.MembershipRequired, want.auth, want.membership)
			}
			if want.strictPow && lim.MinPowDifficulty != want.pow {
				t.Errorf("NIP-11 min_pow_difficulty=%d, config pow.min=%d", lim.MinPowDifficulty, want.pow)
			}

			// Enforcement matches the advertisement.
			anon := Dial(t, r.URL)
			_, closed := anon.Req(F{"kinds": []int{1}, "limit": 1})
			if want.auth != (closed != "") {
				t.Errorf("anonymous REQ closed=%q with auth_required=%v", closed, want.auth)
			}
			c := Dial(t, r.URL)
			authOK, why := c.Auth(eve.PrivHex)
			if want.agentAuth {
				// NIP-AA: a non-member with no credential fails AUTH itself.
				if authOK {
					t.Errorf("agent_auth relay accepted a non-member's AUTH")
				}
				return
			}
			if !authOK {
				t.Fatalf("AUTH as a non-member refused: %s", why)
			}
			ev := Ev(t, eve.PrivHex, 1, "hello "+filepath.Base(f))
			ok, msg := c.Publish(ev)
			wantOK := !want.membership && !want.strictPow
			if ok != wantOK {
				t.Errorf("unmined post by a non-member: accepted=%v (%s), want %v", ok, msg, wantOK)
			}
			if strings.Contains(r.Log(), "panic") {
				t.Errorf("relay log has a panic\n%s", r.Log())
			}
		})
	}
}

type exampleLimits struct {
	auth, membership, strictPow, agentAuth bool
	pow                                    int
}

func limitsOf(cfg map[string]any) exampleLimits {
	get := func(m any, keys ...string) any {
		for _, k := range keys {
			mm, ok := m.(map[string]any)
			if !ok {
				return nil
			}
			m = mm[k]
		}
		return m
	}
	b := func(v any) bool { x, _ := v.(bool); return x }
	n := func(v any) int {
		switch x := v.(type) {
		case float64:
			return int(x)
		case int:
			return x
		}
		var i int
		_, _ = fmt.Sscan(fmt.Sprint(v), &i)
		return i
	}
	return exampleLimits{
		auth:       b(get(cfg, "nip11", "limitation", "auth_required")),
		membership: b(get(cfg, "nip11", "limitation", "membership_required")),
		strictPow:  b(get(cfg, "pow", "strict")) && n(get(cfg, "pow", "min")) > 0,
		pow:        n(get(cfg, "pow", "min")),
		agentAuth:  b(get(cfg, "agent_auth", "enabled")),
	}
}
