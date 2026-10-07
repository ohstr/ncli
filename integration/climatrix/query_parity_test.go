package climatrix

import (
	"encoding/json"
	"net/http"
	"sort"
	"testing"
)

// queryIDs runs filter through POST /query signed by priv: the event ids,
// or the HTTP status when refused.
func queryIDs(t *testing.T, r *Relay, priv string, filter F) ([]string, int) {
	t.Helper()
	body, _ := json.Marshal([]any{map[string]any(filter)})
	u := r.httpURL("/query")
	code, resp := do(t, "POST", u, nip98{priv: priv, url: u, method: "POST", body: body}.header(t), body)
	if code != http.StatusOK {
		return nil, code
	}
	var evs []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(resp), &evs); err != nil {
		t.Fatalf("/query body: %v\n%s", err, resp)
	}
	ids := make([]string, len(evs))
	for i, e := range evs {
		ids[i] = e.ID
	}
	sort.Strings(ids)
	return ids, code
}

// reqIDs runs filter as a REQ authenticated as priv: the ids, or whether
// the relay closed it.
func reqIDs(t *testing.T, r *Relay, priv string, filter F) ([]string, bool) {
	t.Helper()
	evs, closed := DialAs(t, r.URL, priv).Req(filter)
	ids := make([]string, len(evs))
	for i, e := range evs {
		ids[i] = e.ID
	}
	sort.Strings(ids)
	return ids, closed != ""
}

// The /query bridge is REQ over HTTP: for the same reader and filter it
// returns exactly what REQ returns, and refuses what REQ refuses.
func TestQueryBridgeMatchesREQ(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", map[string]any{
		"httpBridge": map[string]any{"query": map[string]any{"enabled": true}},
	})
	r.Seed(t)
	alice, bob, eve := A(t, "alice"), A(t, "bob"), A(t, "eve")
	var msgs []string
	for _, shape := range groupShapes {
		id := "q-" + shape.name
		msgs = append(msgs, setupGroup(t, r, id, shape).ID)
	}

	filters := []F{
		{"kinds": []int{1}, "limit": 50},
		{"kinds": []int{9}},
		{"ids": msgs},
		{"authors": []string{alice.PubHex}},
		{"kinds": []int{39000}},
	}
	for _, shape := range groupShapes {
		filters = append(filters, F{"#h": []string{"q-" + shape.name}})
	}

	for _, rd := range []struct{ who, priv string }{
		{"admin", alice.PrivHex}, {"member", bob.PrivHex}, {"outsider", eve.PrivHex},
	} {
		for _, f := range filters {
			viaREQ, refused := reqIDs(t, r, rd.priv, f)
			viaQuery, code := queryIDs(t, r, rd.priv, f)
			if refused {
				if code == http.StatusOK {
					t.Errorf("%s %v: REQ refused, /query answered %d events", rd.who, f, len(viaQuery))
				}
				continue
			}
			if code != http.StatusOK {
				t.Errorf("%s %v: REQ answered, /query refused (%d)", rd.who, f, code)
				continue
			}
			if !equalIDs(viaREQ, viaQuery) {
				t.Errorf("%s %v: REQ %d events, /query %d -- they must match", rd.who, f, len(viaREQ), len(viaQuery))
			}
		}
	}
}

func equalIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
