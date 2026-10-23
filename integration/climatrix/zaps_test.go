package climatrix

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip57/nip57test"
	"github.com/ohstr/nmilat/nipAZ"
)

// zapper publishes valid zap receipts to a relay.
type zapper struct {
	t        *testing.T
	raw      *Raw
	sender   nip57test.Key
	provider nip57test.Key
	n        int
}

func newZapper(t *testing.T, r *Relay) *zapper {
	return &zapper{t: t, raw: Dial(t, r.URL), sender: nip57test.NewKey(0x7000), provider: nip57test.NewKey(0x7001)}
}

// zap pays receiver amount whole units of coin ("sats" or "loki"), ago before now.
func (z *zapper) zap(receiver, coin string, amount int64, ago time.Duration) {
	z.t.Helper()
	z.n++
	s := nip57test.Spec{Sender: z.sender, Provider: z.provider, Recipient: receiver, Amount: amount * 1000,
		CreatedAt: uint64(time.Now().Add(-ago).Unix()) - uint64(z.n)}
	if coin == "loki" {
		s.Kind = nipAZ.KindAltZapReceipt
	}
	ev, err := nip57test.Receipt(s)
	if err != nil {
		z.t.Fatal(err)
	}
	if ok, msg := z.raw.Publish(ev); !ok {
		z.t.Fatalf("publish receipt: %s", msg)
	}
}

type rankedZap struct {
	Pubkey string `json:"pubkey"`
	Total  uint64 `json:"total"`
}

func tagValue(ev *nip01.Event, name string) string {
	for _, tag := range ev.Tags {
		if len(tag) > 1 && tag[0] == name {
			return tag[1]
		}
	}
	return ""
}

func tagsNamed(ev *nip01.Event, name string) [][]string {
	var out [][]string
	for _, tag := range ev.Tags {
		if len(tag) > 0 && tag[0] == name {
			out = append(out, tag)
		}
	}
	return out
}

// ranking asks for a zap ranking and returns its totals, or the CLOSED reason.
func ranking(t *testing.T, raw *Raw, opts map[string]any) (*nip01.Event, []rankedZap, string) {
	t.Helper()
	cache := []any{"zap-ranking"}
	if opts != nil {
		cache = append(cache, opts)
	}
	evs, closed := raw.Req(F{"cache": cache})
	if closed != "" && closed != "one-shot cache request completed" {
		return nil, nil, closed
	}
	if len(evs) != 1 || evs[0].Kind != 25521 {
		t.Fatalf("zap-ranking answered %d events: %+v", len(evs), evs)
	}
	if err := evs[0].Verify(); err != nil {
		t.Fatalf("ranking not signed: %v", err)
	}
	var out []rankedZap
	if err := json.Unmarshal([]byte(evs[0].Content), &out); err != nil {
		t.Fatal(err)
	}
	return evs[0], out, ""
}

func TestZapRanking(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", map[string]any{"cache": map[string]any{
		"zaps": map[string]any{"enabled": true, "windows": []string{"1h", "7d", "all"}, "maxLimit": 3},
	}})
	z := newZapper(t, r)
	alice, bob, carol := A(t, "alice").PubHex, A(t, "bob").PubHex, A(t, "carol").PubHex
	z.zap(alice, "sats", 21, 10*time.Minute)
	z.zap(bob, "sats", 50, 3*24*time.Hour)
	z.zap(alice, "loki", 900, 20*time.Minute)
	z.zap(carol, "loki", 5, 40*24*time.Hour)
	raw := Dial(t, r.URL)

	ev, got, closed := ranking(t, raw, nil)
	if closed != "" {
		t.Fatalf("default ranking refused: %s", closed)
	}
	if want := []rankedZap{{alice, 21}}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("default (1h, sats) = %v, want %v", got, want)
	}
	if tagValue(ev, "window") != "1h" || tagValue(ev, "coin") != "sats" || tagValue(ev, "limit") != "3" {
		t.Errorf("tags = %v", ev.Tags)
	}
	if _, got, _ = ranking(t, raw, map[string]any{"window": "7d"}); fmt.Sprint(got) != fmt.Sprint([]rankedZap{{bob, 50}, {alice, 21}}) {
		t.Errorf("7d sats = %v", got)
	}
	if _, got, _ = ranking(t, raw, map[string]any{"window": "all", "coin": "loki"}); fmt.Sprint(got) != fmt.Sprint([]rankedZap{{alice, 900}, {carol, 5}}) {
		t.Errorf("all loki = %v", got)
	}
	if _, got, _ = ranking(t, raw, map[string]any{"window": "all", "coin": "loki", "limit": 1}); len(got) != 1 {
		t.Errorf("limit 1 = %v", got)
	}

	for opts, want := range map[string]string{
		`{"window":"3d"}`: "invalid: window must be one of 1h, 7d, all",
		`{"coin":"btc"}`:  "invalid: coin must be one of sats, loki",
	} {
		var o map[string]any
		_ = json.Unmarshal([]byte(opts), &o)
		if _, _, closed := ranking(t, raw, o); closed != want {
			t.Errorf("%s: CLOSED %q, want %q", opts, closed, want)
		}
	}
	for _, old := range []string{"top-zapped", "get_top_zapped"} {
		evs, closed := raw.Req(F{"cache": []any{old, map[string]any{}}})
		if len(evs) != 0 || closed != fmt.Sprintf("invalid: unknown cache action %q", old) {
			t.Errorf("%s: %d events, CLOSED %q", old, len(evs), closed)
		}
	}

	// The same request through ncli, as examples/filters.yaml documents it.
	e := NewEnv(t)
	targets := e.WriteFile("ranking.yaml", fmt.Sprintf(`kind: targets
spec:
  relays: [%s]
  filters:
    - cache: ["zap-ranking", {window: all, coin: loki, limit: 5}]
`, r.URL))
	var found []nip01.Event
	e.MustOK(t, "find", "-t", targets, "--json").JSON(t, &found)
	if len(found) != 1 || found[0].Kind != 25521 || !strings.Contains(found[0].Content, alice) {
		t.Errorf("find -t ranking = %+v", found)
	}
}

func TestZapRankingOff(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", nil)
	newZapper(t, r).zap(A(t, "alice").PubHex, "sats", 21, time.Hour)
	if _, _, closed := ranking(t, Dial(t, r.URL), nil); closed != "restricted: zap ranking is not enabled on this relay" {
		t.Errorf("CLOSED %q", closed)
	}
	if ix := indexStates(t, NewEnv(t), r)["zaps"]; ix.State != "disabled" || ix.HasData {
		t.Errorf("zap index with zap ranking off = %+v, want disabled and empty", ix)
	}
}

type indexState struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	State   string `json:"state"`
	Action  string `json:"action"`
	Reason  string `json:"reason"`
	Hint    string `json:"hint"`
	HasData bool   `json:"has_data"`
	Entries int64  `json:"entries"`
	Behind  int64  `json:"behind"`
	Running *struct {
		ID, Kind, Trigger string
	} `json:"running"`
}

func indexStates(t *testing.T, e *Env, r *Relay) map[string]indexState {
	t.Helper()
	var list struct{ Indexes []indexState }
	e.MustOK(t, "relay", "index", "list", "--config", r.ConfigPath, "--json").JSON(t, &list)
	out := map[string]indexState{}
	for _, ix := range list.Indexes {
		out[ix.Name] = ix
	}
	return out
}

// waitIndex polls until index matches want.
func waitIndex(t *testing.T, e *Env, r *Relay, index string, want func(indexState) bool) indexState {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		ix := indexStates(t, e, r)[index]
		if want(ix) {
			return ix
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s index stuck at %+v", index, ix)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestDerivedIndexLifecycle turns zap ranking and profile stats off and on
// again on one store: off keeps the indexes' data until dropped, on
// rebuilds them from the stored events.
func TestDerivedIndexLifecycle(t *testing.T) {
	needsRelay(t)
	on := map[string]any{"cache": map[string]any{
		"zaps":     map[string]any{"enabled": true},
		"profiles": map[string]any{"stats": map[string]any{"enabled": true}},
	}}
	off := map[string]any{"cache": map[string]any{
		"zaps":     map[string]any{"enabled": false},
		"profiles": map[string]any{"stats": map[string]any{"enabled": false}},
	}}
	r := StartRelay(t, "", on)
	e := NewEnv(t)
	alice := A(t, "alice").PubHex
	newZapper(t, r).zap(alice, "sats", 21, time.Hour)
	for _, name := range []string{"zaps", "stats"} {
		waitIndex(t, e, r, name, func(ix indexState) bool { return ix.State == "ready" && ix.HasData })
	}

	r.Reconfigure(t, off)
	r.Restart(t)
	newZapper(t, r).zap(alice, "sats", 5, time.Hour) // missed by both indexes
	if ix := indexStates(t, e, r)["zaps"]; ix.Entries != 1 {
		t.Errorf("disabled zap index entries = %d, want the 1 it holds", ix.Entries)
	}
	for _, name := range []string{"zaps", "stats"} {
		ix := indexStates(t, e, r)[name]
		if ix.Enabled || ix.State != "disabled" || !ix.HasData || !strings.Contains(ix.Hint, "ncli relay index drop "+name) {
			t.Errorf("%s with its feature off = %+v, want disabled with its data and the drop that removes it", name, ix)
		}
	}
	e.Run(t, "relay", "index", "rebuild", "zaps", "--config", r.ConfigPath, "--json").ExpectErr(t, "usage")
	e.MustOK(t, "relay", "index", "drop", "zaps", "--yes", "--config", r.ConfigPath, "--json")
	waitIndex(t, e, r, "zaps", func(ix indexState) bool { return !ix.HasData })

	r.Reconfigure(t, on)
	r.Restart(t)
	for _, name := range []string{"zaps", "stats"} {
		waitIndex(t, e, r, name, func(ix indexState) bool { return ix.State == "ready" })
	}
	_, totals, closed := ranking(t, Dial(t, r.URL), nil)
	if closed != "" || len(totals) != 1 || totals[0].Pubkey != alice || totals[0].Total != 26 {
		t.Errorf("ranking after re-enabling = %+v %q, want both zaps", totals, closed)
	}
	evs, _ := Dial(t, r.URL).Req(F{"kinds": []int{30382}, "#d": []string{alice}})
	if len(evs) != 1 || !containsTag(evs[0], []string{"zap_cnt_recd", "2"}) {
		t.Errorf("NIP-85 after re-enabling = %+v, want both zaps counted", evs)
	}
}

// TestDumpOfStoppedRelayStoreNeedsNoRebuild reads a stopped relay's store
// with dump, which opens it with no indexes: that changes nothing, so the
// relay starts with its indexes still current.
func TestDumpOfStoppedRelayStoreNeedsNoRebuild(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", map[string]any{"cache": map[string]any{"zaps": map[string]any{"enabled": true}}})
	e := NewEnv(t)
	newZapper(t, r).zap(A(t, "alice").PubHex, "sats", 21, time.Hour)
	waitIndex(t, e, r, "zaps", func(ix indexState) bool { return ix.State == "ready" && ix.HasData })

	r.Stop()
	out := filepath.Join(t.TempDir(), "dump.json")
	e.MustOK(t, "dump", "-s", filepath.Join(r.Dir, "db", "notes.db"), "-o", out)
	r.Restart(t)
	if ix := indexStates(t, e, r)["zaps"]; ix.State != "ready" {
		t.Errorf("zap index after a dump of the stopped store = %+v, want ready", ix)
	}
	if strings.Contains(r.Log(), "building the index") {
		t.Errorf("relay rebuilt an index after a read-only dump:\n%s", r.Log())
	}
}

func TestProfileStatsPerCoin(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", map[string]any{"cache": map[string]any{"profiles": map[string]any{"stats": map[string]any{"enabled": true}}}})
	z := newZapper(t, r)
	bob := A(t, "bob").PubHex
	z.zap(bob, "sats", 21, time.Hour)
	z.zap(bob, "loki", 980, time.Hour)

	evs, closed := Dial(t, r.URL).Req(F{"kinds": []int{30382}, "#d": []string{bob}})
	if closed != "" || len(evs) != 1 {
		t.Fatalf("assertions: %d events, CLOSED %q", len(evs), closed)
	}
	for tag, want := range map[string]string{"zap_cnt_recd": "2", "zap_amt_recd": "21", "zap_amt_recd_loki": "980", "zappers_recd": "1"} {
		if got := tagValue(evs[0], tag); got != want {
			t.Errorf("%s = %q, want %q", tag, got, want)
		}
	}
}

func TestCountExactAndApproximate(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", map[string]any{"nip11": map[string]any{"limitation": map[string]any{"max_limit": 5}}})
	raw := Dial(t, r.URL)
	alice := A(t, "alice")
	now := time.Now().Unix()
	for i := 0; i < 12; i++ {
		ev := &nip01.Event{PubKey: alice.PubHex, Kind: 1, CreatedAt: uint64(now) - uint64(i), Content: fmt.Sprint(i)}
		if err := ev.Sign(alice.PrivHex); err != nil {
			t.Fatal(err)
		}
		if ok, msg := raw.Publish(ev); !ok {
			t.Fatal(msg)
		}
	}
	if n, approx, closed := raw.CountApprox(F{"kinds": []int{1}}); n != 12 || approx || closed != "" {
		t.Errorf("kind-only COUNT = %d approx=%v %q, want exactly 12 past max_limit 5", n, approx, closed)
	}
	if n, approx, _ := raw.CountApprox(F{"authors": []string{alice.PubHex}}); n != 5 || !approx {
		t.Errorf("capped COUNT = %d approx=%v, want 5 approximate", n, approx)
	}
	if n, approx, _ := raw.CountApprox(F{"authors": []string{alice.PubHex}, "since": now - 1}); n != 2 || approx {
		t.Errorf("small COUNT = %d approx=%v, want exactly 2", n, approx)
	}
}

func TestRelayStatsEvent(t *testing.T) {
	needsRelay(t)
	operator := A(t, "operator").PubHex
	statsReq := F{"kinds": []int{30166}, "authors": []string{operator}}

	t.Run("live, with every feature", func(t *testing.T) {
		r := StartRelay(t, "", map[string]any{"cache": map[string]any{
			"profiles": map[string]any{"stats": map[string]any{"enabled": true}},
			// Totals come from the ranking cache, reused for refresh.
			"zaps":  map[string]any{"enabled": true, "windows": []string{"24h", "all"}, "refresh": "200ms"},
			"relay": map[string]any{"stats": map[string]any{"enabled": true, "interval": "200ms"}},
		}})
		raw := Dial(t, r.URL)
		var first *nip01.Event
		deadline := time.Now().Add(10 * time.Second)
		for first == nil && time.Now().Before(deadline) {
			if evs, _ := raw.Req(statsReq); len(evs) == 1 {
				first = evs[0]
			} else {
				time.Sleep(100 * time.Millisecond)
			}
		}
		if first == nil {
			t.Fatal("no kind 30166 published")
		}
		if err := first.Verify(); err != nil {
			t.Fatal(err)
		}
		if d := tagValue(first, "d"); d != r.URL+"/" {
			t.Errorf("d = %q, want %q", d, r.URL+"/")
		}
		for _, want := range [][]string{{"zap-ranking", "windows", "24h", "all"}, {"zap-ranking", "coins", "sats", "loki"}} {
			if !containsTag(first, want) {
				t.Errorf("missing %v in %v", want, first.Tags)
			}
		}
		for _, name := range []string{"events", "authors", "profiles", "zap-receipts"} {
			if countOf(first, name) == "" {
				t.Errorf("no count %q in %v", name, first.Tags)
			}
		}
		if len(tagsNamed(first, "total")) != 2 || len(tagsNamed(first, "rtt-open")) != 0 {
			t.Errorf("totals/rtt = %v", first.Tags)
		}

		live := raw.Live(statsReq)
		newZapper(t, r).zap(A(t, "bob").PubHex, "loki", 7, time.Minute)
		deadline = time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			ev := live.Next(time.Until(deadline))
			if ev == nil {
				break
			}
			if ev.CreatedAt < first.CreatedAt {
				t.Errorf("an older version arrived live")
			}
			if containsTag(ev, []string{"total", "zaps", "7", "loki"}) {
				return
			}
		}
		t.Fatal("no live 30166 with the new loki total")
	})

	t.Run("only base counts with features off", func(t *testing.T) {
		r := StartRelay(t, "", map[string]any{"cache": map[string]any{"relay": map[string]any{"stats": map[string]any{"enabled": true, "interval": "200ms"}}}})
		raw := Dial(t, r.URL)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if evs, _ := raw.Req(statsReq); len(evs) == 1 {
				counts := tagsNamed(evs[0], "count")
				if len(counts) != 2 || countOf(evs[0], "events") == "" || countOf(evs[0], "authors") == "" ||
					len(tagsNamed(evs[0], "total")) != 0 || len(tagsNamed(evs[0], "zap-ranking")) != 0 {
					t.Errorf("tags = %v", evs[0].Tags)
				}
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("no kind 30166 published")
	})

	t.Run("none when off", func(t *testing.T) {
		r := StartRelay(t, "", nil)
		time.Sleep(time.Second)
		if evs, _ := Dial(t, r.URL).Req(statsReq); len(evs) != 0 {
			t.Errorf("published %d with cache.relay.stats off", len(evs))
		}
	})
}

func containsTag(ev *nip01.Event, want []string) bool {
	for _, tag := range ev.Tags {
		if fmt.Sprint(tag) == fmt.Sprint(want) {
			return true
		}
	}
	return false
}

func countOf(ev *nip01.Event, name string) string {
	for _, tag := range tagsNamed(ev, "count") {
		if len(tag) > 2 && tag[1] == name {
			return tag[2]
		}
	}
	return ""
}

func TestStatsIndexUnavailableWithProfileStatsOff(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", map[string]any{"cache": map[string]any{"zaps": map[string]any{"enabled": true}}})
	e := NewEnv(t)
	var list struct {
		Indexes []struct {
			Name        string `json:"name"`
			Enabled     bool   `json:"enabled"`
			Unavailable string `json:"unavailable"`
		} `json:"indexes"`
	}
	e.MustOK(t, "relay", "index", "list", "--config", r.ConfigPath, "--json").JSON(t, &list)
	seen := map[string]bool{}
	for _, ix := range list.Indexes {
		seen[ix.Name] = ix.Enabled
		if ix.Name == "stats" && !strings.Contains(ix.Unavailable, "cache.profiles.stats.enabled") {
			t.Errorf("stats reason = %q", ix.Unavailable)
		}
	}
	if seen["stats"] || seen["search"] || !seen["zaps"] {
		t.Errorf("availability = %v, want only zaps", seen)
	}
	e.Run(t, "relay", "index", "rebuild", "stats", "--config", r.ConfigPath, "--json").ExpectErr(t, "usage")
}
