package climatrix

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/nip57/nip57test"
	"github.com/ohstr/nmilat/relay"
	bolt "go.etcd.io/bbolt"
)

// The scenarios an operator meets: features turned off and on across
// restarts, a store written while the relay was stopped, a relay killed
// mid-rebuild, scoring changed, Meilisearch replaced. In each the relay
// must bring its indexes current on its own, as jobs that say what they
// did, without holding writes up.

func cacheOf(zaps, stats bool) map[string]any {
	return map[string]any{"cache": map[string]any{
		"zaps":     map[string]any{"enabled": zaps},
		"profiles": map[string]any{"stats": map[string]any{"enabled": stats}},
	}}
}

type jobView struct {
	ID            string `json:"id"`
	Index         string `json:"index"`
	Kind          string `json:"kind"`
	Trigger       string `json:"trigger"`
	Reason        string `json:"reason"`
	State         string `json:"state"`
	Done          int64  `json:"done"`
	EntriesBefore int64  `json:"entries_before"`
	EntriesAfter  int64  `json:"entries_after"`
	Summary       string `json:"summary"`
	Noop          bool   `json:"noop"`
}

func jobsOf(t *testing.T, e *Env, r *Relay, index string) []jobView {
	t.Helper()
	var out struct{ Jobs []jobView }
	e.MustOK(t, "relay", "jobs", "list", "--index", index, "--config", r.ConfigPath, "--json").JSON(t, &out)
	return out.Jobs
}

// settled waits until index is ready with no job running.
func settled(t *testing.T, e *Env, r *Relay, index string) indexState {
	t.Helper()
	return waitIndex(t, e, r, index, func(ix indexState) bool { return ix.State == "ready" && ix.Running == nil && ix.Action == "" })
}

func follows(t *testing.T, r *Relay, author Actor, at int64, pubkeys ...string) *nip01.Event {
	t.Helper()
	var tags [][]string
	for _, p := range pubkeys {
		tags = append(tags, []string{"p", p})
	}
	ev := &nip01.Event{PubKey: author.PubHex, Kind: 3, CreatedAt: uint64(at), Tags: tags}
	if err := ev.Sign(author.PrivHex); err != nil {
		t.Fatal(err)
	}
	if ok, msg := Dial(t, r.URL).Publish(ev); !ok {
		t.Fatalf("publish contacts: %s", msg)
	}
	return ev
}

func followers(t *testing.T, r *Relay, pubkey string) string {
	t.Helper()
	evs, closed := Dial(t, r.URL).Req(F{"kinds": []int{30382}, "#d": []string{pubkey}})
	if closed != "" || len(evs) != 1 {
		t.Fatalf("NIP-85 for %s: %d events, CLOSED %q", pubkey[:8], len(evs), closed)
	}
	return tagValue(evs[0], "followers")
}

func zapTotal(t *testing.T, r *Relay, pubkey string) uint64 {
	t.Helper()
	_, totals, closed := ranking(t, Dial(t, r.URL), nil)
	if closed != "" {
		t.Fatalf("ranking: %s", closed)
	}
	for _, z := range totals {
		if z.Pubkey == pubkey {
			return z.Total
		}
	}
	return 0
}

// I1: events stored while the features were off are taken in by a
// catch-up the relay starts on its own, not a rebuild.
func TestIndexLifecycle_CatchUpAfterTheFeaturesWereOff(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", cacheOf(true, true))
	e := NewEnv(t)
	alice, bob, carol := A(t, "alice"), A(t, "bob"), A(t, "carol")
	now := time.Now().Unix()
	newZapper(t, r).zap(alice.PubHex, "sats", 21, time.Hour)
	follows(t, r, bob, now-100, alice.PubHex)
	settled(t, e, r, "zaps")
	settled(t, e, r, "stats")

	r.Reconfigure(t, cacheOf(false, false))
	r.Restart(t)
	newZapper(t, r).zap(alice.PubHex, "sats", 5, time.Hour)
	// bob's newer list replaces the one the index holds.
	follows(t, r, bob, now-50, carol.PubHex)
	// A list stored and deleted while off.
	gone := follows(t, r, alice, now-40, carol.PubHex)
	del := Ev(t, alice.PrivHex, 5, "", []string{"e", gone.ID})
	if ok, msg := Dial(t, r.URL).Publish(del); !ok {
		t.Fatalf("delete: %s", msg)
	}
	for _, name := range []string{"zaps", "stats"} {
		ix := indexStates(t, e, r)[name]
		if ix.State != "disabled" || !strings.Contains(ix.Hint, "ncli relay index drop "+name) {
			t.Errorf("%s while off = %+v", name, ix)
		}
	}

	r.Reconfigure(t, cacheOf(true, true))
	r.Restart(t)
	for _, name := range []string{"zaps", "stats"} {
		settled(t, e, r, name)
		var kinds []string
		for _, j := range jobsOf(t, e, r, name) {
			kinds = append(kinds, j.Kind+"/"+j.Trigger+"/"+j.State)
			if j.Kind == "rebuild" {
				t.Errorf("%s was rebuilt, want a catch-up: %+v", name, j)
			}
		}
		if !contains(kinds, "catch_up/reconcile/succeeded") {
			t.Errorf("%s jobs = %v, want a catch-up the relay started", name, kinds)
		}
	}
	if got := zapTotal(t, r, alice.PubHex); got != 26 {
		t.Errorf("alice's zaps = %d, want both (26)", got)
	}
	if got := followers(t, r, alice.PubHex); got != "0" {
		t.Errorf("alice followers = %s, want 0 (bob's list moved on)", got)
	}
	if got := followers(t, r, carol.PubHex); got != "1" {
		t.Errorf("carol followers = %s, want 1 (bob; alice's list was deleted)", got)
	}
	if !strings.Contains(r.Log(), "caught up in") {
		t.Errorf("the relay log doesn't say the catch-up happened:\n%s", r.Log())
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// I2: another ncli (sync, stream: a store opened without the relay's
// indexes) wrote into the store while the relay was stopped; the relay
// catches up on restart.
func TestIndexLifecycle_StoreWrittenWhileTheRelayWasStopped(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", cacheOf(true, false))
	e := NewEnv(t)
	alice := A(t, "alice")
	newZapper(t, r).zap(alice.PubHex, "sats", 21, time.Hour)
	settled(t, e, r, "zaps")
	r.Stop()

	store, err := relay.NewEventStore(filepath.Join(r.Dir, "db", "notes.db"), &nip11.Limitation{MaxLimit: 500})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := nip57test.Receipt(nip57test.Spec{Sender: nip57test.NewKey(0x7200), Provider: nip57test.NewKey(0x7201),
		Recipient: alice.PubHex, Amount: 7000, CreatedAt: uint64(time.Now().Add(-2 * time.Hour).Unix())})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertEvents(context.Background(), []*nip01.Event{receipt}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	r.Restart(t)
	settled(t, e, r, "zaps")
	if got := zapTotal(t, r, alice.PubHex); got != 28 {
		t.Errorf("alice's zaps = %d, want the one stored meanwhile too (28)", got)
	}
	var kinds []string
	for _, j := range jobsOf(t, e, r, "zaps") {
		kinds = append(kinds, j.Kind)
	}
	if !contains(kinds, "catch_up") || contains(kinds, "rebuild") {
		t.Errorf("zaps jobs = %v, want a catch-up and no rebuild", kinds)
	}
}

// fillStore writes n filler notes and a zap receipt every hundredth into
// a stopped relay's store, in-process, so a rebuild has real work.
func fillStore(t *testing.T, path string, n int, receiver string) {
	t.Helper()
	store, err := relay.NewEventStore(path, &nip11.Limitation{MaxLimit: 500}, relay.WithEventStoreIndexMaintenance(relay.MaintenanceManual))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sender, provider := nip57test.NewKey(0x7100), nip57test.NewKey(0x7101)
	pub := make([]byte, 32)
	_, _ = rand.Read(pub)
	at := uint64(time.Now().Add(-time.Hour).Unix())
	batch := make([]*nip01.Event, 0, 5000)
	for i := range n {
		if i%100 == 0 {
			// Within the ranking's default window.
			ev, err := nip57test.Receipt(nip57test.Spec{Sender: sender, Provider: provider, Recipient: receiver, Amount: 1000, CreatedAt: at - uint64(i/100)})
			if err != nil {
				t.Fatal(err)
			}
			batch = append(batch, ev)
		} else {
			id := make([]byte, 32)
			binary.BigEndian.PutUint64(id, uint64(i))
			_, _ = rand.Read(id[8:])
			batch = append(batch, &nip01.Event{ID: hex.EncodeToString(id), PubKey: hex.EncodeToString(pub), Kind: 1, CreatedAt: at - uint64(i), Content: "filler", Sig: strings.Repeat("0", 128)})
		}
		if len(batch) == cap(batch) || i == n-1 {
			if err := store.InsertEvents(context.Background(), batch); err != nil {
				t.Fatal(err)
			}
			batch = batch[:0]
		}
	}
}

// metricsText scrapes the relay's /metrics.
func metricsText(t *testing.T, r *Relay, token string) string {
	t.Helper()
	code, body, _ := adminReq(t, nil, "GET", "http://"+r.AdminAddr+"/metrics", "", map[string]string{"Authorization": "Bearer " + token}, nil)
	if code != http.StatusOK {
		t.Fatalf("/metrics = %d", code)
	}
	return body
}

// histogram reads a histogram's per-bucket counts for one label set.
func histogram(text, name, labels string) map[string]float64 {
	out := map[string]float64{}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, name+"_bucket{") || !strings.Contains(line, labels) {
			continue
		}
		i := strings.Index(line, `le="`)
		j := strings.Index(line[i+4:], `"`)
		v, _ := strconv.ParseFloat(line[strings.LastIndex(line, " ")+1:], 64)
		out[line[i+4:i+4+j]] = v
	}
	return out
}

// I4, I9, I11: a relay killed mid-rebuild resumes it on restart; an admin
// asking for the same rebuild joins it; and writes during it are never
// held up by it.
func TestIndexLifecycle_KilledMidRebuildResumes(t *testing.T) {
	needsRelay(t)
	tokens := filepath.Join(t.TempDir(), "metrics.tokens")
	token := addToken(t, tokens, "scrape")
	metrics := map[string]any{"admin": map[string]any{"metrics": map[string]any{"enabled": true, "tokensFile": tokens, "scrapeCacheTTL": "0s"}}}
	r := StartRelay(t, "", cacheOf(false, false))
	r.Reconfigure(t, metrics)
	e := NewEnv(t)
	alice := A(t, "alice")
	r.Stop()
	const n = 400_000
	fillStore(t, filepath.Join(r.Dir, "db", "notes.db"), n, alice.PubHex)

	// Zap ranking turned on over a full store: the relay rebuilds it.
	r.Reconfigure(t, cacheOf(true, false))
	r.Restart(t)
	var first jobView
	deadline := time.Now().Add(60 * time.Second)
	for first.ID == "" {
		for _, j := range jobsOf(t, e, r, "zaps") {
			if j.Kind == "rebuild" && j.State == "running" && j.Done > 0 {
				first = j
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no rebuild seen running: %+v", jobsOf(t, e, r, "zaps"))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if first.Trigger != "reconcile" || first.Reason != "never built" {
		t.Errorf("startup rebuild = %+v, want one the relay started because the index was never built", first)
	}
	r.Stop() // SIGKILL, mid-rebuild

	r.Restart(t)
	var resumed jobView
	deadline = time.Now().Add(30 * time.Second)
	for resumed.ID == "" && time.Now().Before(deadline) {
		for _, j := range jobsOf(t, e, r, "zaps") {
			if j.ID != first.ID && j.Kind == "rebuild" {
				resumed = j
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.HasPrefix(resumed.Reason, "resuming the rebuild at event ") {
		t.Errorf("after a restart = %+v, want the rebuild resumed", resumed)
	}
	for _, j := range jobsOf(t, e, r, "zaps") {
		if j.ID == first.ID && (j.State != "interrupted" || !strings.Contains(j.Summary, "resumes on its own")) {
			t.Errorf("the killed job = %+v, want interrupted", j)
		}
	}

	// An admin asking for the same rebuild gets the running one.
	if resumed.State == "running" {
		var again jobView
		e.MustOK(t, "relay", "index", "rebuild", "zaps", "--config", r.ConfigPath, "--json").JSON(t, &again)
		if again.ID != resumed.ID && !again.Noop {
			t.Errorf("a second rebuild = %+v, want the running %s", again, resumed.ID)
		}
	}

	// Writes while it runs are acknowledged promptly.
	raw := Dial(t, r.URL)
	var slowest time.Duration
	for i := range 50 {
		start := time.Now()
		if ok, msg := raw.Publish(Ev(t, alice.PrivHex, 1, fmt.Sprintf("during the rebuild %d", i))); !ok {
			t.Fatalf("publish during the rebuild: %s", msg)
		}
		slowest = max(slowest, time.Since(start))
	}

	ix := settled(t, e, r, "zaps")
	if ix.Entries != n/100 {
		t.Errorf("zap entries = %d, want %d", ix.Entries, n/100)
	}
	if got := zapTotal(t, r, alice.PubHex); got != uint64(n/100) {
		t.Errorf("alice's zaps = %d, want %d", got, n/100)
	}
	if slowest > time.Second {
		t.Errorf("a publish during the rebuild took %s", slowest)
	}
	buckets := histogram(metricsText(t, r, token), "ncli_relay_index_txn_seconds", `index="zaps"`)
	if buckets["+Inf"] == 0 {
		t.Fatalf("no index transactions recorded")
	}
	if buckets["0.05"] != buckets["+Inf"] {
		t.Errorf("index transactions over 50ms: %v of %v", buckets["+Inf"]-buckets["0.05"], buckets["+Inf"])
	}
}

// I5: a store written behind the relay's back (an older version, a raw
// tool) can't be trusted: the relay rebuilds and says why.
func TestIndexLifecycle_ForeignWriteMeansRebuild(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", cacheOf(true, false))
	e := NewEnv(t)
	alice := A(t, "alice")
	newZapper(t, r).zap(alice.PubHex, "sats", 21, time.Hour)
	settled(t, e, r, "zaps")
	r.Stop()

	db, err := bolt.Open(filepath.Join(r.Dir, "db", "notes.db"), 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ev := Ev(t, alice.PrivHex, 1, "written without nmilat")
	raw, _ := json.Marshal(ev)
	if err := db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte{1})
		seq, err := b.NextSequence()
		if err != nil {
			return err
		}
		return b.Put(binary.BigEndian.AppendUint64(nil, seq), raw)
	}); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	r.Restart(t)
	settled(t, e, r, "zaps")
	found := false
	for _, j := range jobsOf(t, e, r, "zaps") {
		if j.Kind == "rebuild" && j.Trigger == "reconcile" && strings.Contains(j.Reason, "another program") {
			found = true
		}
	}
	if !found {
		t.Errorf("no rebuild for the foreign write: %+v", jobsOf(t, e, r, "zaps"))
	}
	if got := zapTotal(t, r, alice.PubHex); got != 21 {
		t.Errorf("alice's zaps = %d, want 21", got)
	}
}

// I6: new scoring weights rescore every profile, as a job.
func TestIndexLifecycle_ScoringChangeRescores(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", cacheOf(false, true))
	e := NewEnv(t)
	bob := A(t, "bob")
	if ok, msg := Dial(t, r.URL).Publish(Ev(t, bob.PrivHex, 0, `{"name":"bob"}`)); !ok {
		t.Fatal(msg)
	}
	follows(t, r, A(t, "alice"), time.Now().Unix(), bob.PubHex)
	settled(t, e, r, "stats")

	r.Reconfigure(t, map[string]any{"cache": map[string]any{"profiles": map[string]any{"scoring": map[string]any{"social": map[string]any{"followers_weight": 77}}}}})
	r.Restart(t)
	settled(t, e, r, "stats")
	found := false
	for _, j := range jobsOf(t, e, r, "stats") {
		if j.Kind == "rescore" && j.Reason == "the scoring settings changed" && j.State == "succeeded" {
			found = true
		}
	}
	if !found {
		t.Errorf("no rescore after the scoring change: %+v", jobsOf(t, e, r, "stats"))
	}

	// The same settings again: nothing to do.
	r.Restart(t)
	settled(t, e, r, "stats")
	n := 0
	for _, j := range jobsOf(t, e, r, "stats") {
		if j.Kind == "rescore" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("rescores = %d after an unchanged restart, want 1", n)
	}
}

// I8: a profile stored while the profile checks were off is checked once
// they are on.
func TestIndexLifecycle_ProfileStoredWhileChecksWereOff(t *testing.T) {
	needsRelay(t)
	bob := A(t, "bob")
	nip05 := StartNip05(t, map[string]string{"bob": bob.PubHex})
	r := StartRelay(t, "", cacheOf(false, false))
	e := NewEnv(t)
	if ok, msg := Dial(t, r.URL).Publish(Ev(t, bob.PrivHex, 0, `{"name":"bob","nip05":"`+nip05.ID("bob")+`"}`)); !ok {
		t.Fatal(msg)
	}
	r.env.TrustNip05(nip05)
	r.Reconfigure(t, cacheOf(false, true))
	r.Restart(t)

	deadline := time.Now().Add(30 * time.Second)
	for {
		var out struct {
			Profiles []struct {
				Nip05 struct{ Value, Status string }
			}
		}
		e.MustOK(t, "relay", "verify", bob.PubHex, "--config", r.ConfigPath, "--json").JSON(t, &out)
		// Checked at all: before, a profile stored while checks were off never was.
		if len(out.Profiles) == 1 && out.Profiles[0].Nip05.Status != "" && out.Profiles[0].Nip05.Value != "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("bob's NIP-05 never checked: %+v", out)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// I7: search follows the store: built on first start, kept live, a
// deleted profile leaves it, one stored while search was off reaches it,
// and a Meilisearch index recreated empty is rebuilt.
func TestIndexLifecycle_SearchFollowsTheStore(t *testing.T) {
	needsRelay(t)
	meili := StartMeili(t)
	r := searchRelay(t, meili)
	e := NewEnv(t)
	alice, bob, carol := A(t, "alice"), A(t, "bob"), A(t, "carol")
	raw := Dial(t, r.URL)
	for _, a := range []Actor{alice, bob} {
		if ok, msg := raw.Publish(Ev(t, a.PrivHex, 0, `{"name":"`+a.Name+`"}`)); !ok {
			t.Fatal(msg)
		}
	}
	entries := func(want int64) {
		t.Helper()
		waitIndex(t, e, r, "search", func(ix indexState) bool { return ix.State == "ready" && ix.Entries == want })
	}
	entries(2)

	p := Ev(t, carol.PrivHex, 0, `{"name":"carol"}`)
	raw.Publish(p)
	entries(3)
	raw.Publish(Ev(t, carol.PrivHex, 5, "", []string{"e", p.ID}))
	entries(2)

	// Stored while search was off.
	r.Reconfigure(t, map[string]any{"cache": map[string]any{"profiles": map[string]any{"search": map[string]any{"enabled": false}}}})
	r.Restart(t)
	if ok, msg := Dial(t, r.URL).Publish(Ev(t, carol.PrivHex, 0, `{"name":"carol again"}`)); !ok {
		t.Fatal(msg)
	}
	r.Reconfigure(t, map[string]any{"cache": map[string]any{"profiles": map[string]any{"search": map[string]any{"enabled": true}}}})
	r.Restart(t)
	entries(3)

	// The index recreated empty behind the relay's back.
	req, _ := http.NewRequest(http.MethodDelete, meili+"/indexes/profiles", nil)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		_ = resp.Body.Close()
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		var reasons []string
		for _, j := range jobsOf(t, e, r, "search") {
			reasons = append(reasons, j.Kind+": "+j.Reason)
		}
		sort.Strings(reasons)
		if contains(reasons, "rebuild: the Meilisearch index doesn't exist") || contains(reasons, "rebuild: the Meilisearch index was replaced") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("search jobs = %v, want a rebuild for the lost index", reasons)
		}
		time.Sleep(250 * time.Millisecond)
	}
	entries(3)
}

// I12: in a terminal, a rebuild says its plan, shows progress, then what
// it did.
func TestIndexLifecycle_RebuildInATerminal(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", cacheOf(true, false))
	r.Seed(t)
	e := NewEnv(t)
	newZapper(t, r).zap(A(t, "alice").PubHex, "sats", 21, time.Hour)
	settled(t, e, r, "zaps")
	out := e.RunTTY(t, "relay", "index", "rebuild", "zaps", "--config", r.ConfigPath)
	if out.Code != 0 {
		t.Fatalf("rebuild in a terminal: %s", out)
	}
	for _, want := range []string{"zaps: rebuild — reads", "keeps answering", "zaps: rebuilt in", " entries"} {
		if !strings.Contains(out.Stdout, want) {
			t.Errorf("terminal output lacks %q:\n%s", want, out.Stdout)
		}
	}
	list := e.RunTTY(t, "relay", "index", "list", "--config", r.ConfigPath)
	if !strings.Contains(list.Stdout, "rebuild succeeded") {
		t.Errorf("index list lacks the last job:\n%s", list.Stdout)
	}
}

// I15: Prometheus sees each index's state and backlog.
func TestIndexLifecycle_Metrics(t *testing.T) {
	needsRelay(t)
	tokens := filepath.Join(t.TempDir(), "metrics.tokens")
	token := addToken(t, tokens, "scrape")
	over := cacheOf(true, false)
	deepMerge(over, map[string]any{"admin": map[string]any{"metrics": map[string]any{"enabled": true, "tokensFile": tokens, "scrapeCacheTTL": "0s"}}})
	r := StartRelay(t, "", over)
	e := NewEnv(t)
	settled(t, e, r, "zaps")
	text := metricsText(t, r, token)
	for _, want := range []string{
		`ncli_relay_index_state{index="zaps",state="ready"} 1`,
		`ncli_relay_index_state{index="stats",state="disabled"} 1`,
		`ncli_relay_index_behind{index="zaps"} 0`,
		`ncli_relay_index_entries{index="zaps"}`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("/metrics lacks %s", want)
		}
	}
	for _, gone := range []string{"verify_dropped_total", "search_index_dropped_total", "search_index_queue_depth"} {
		if strings.Contains(text, gone) {
			t.Errorf("/metrics still has %s", gone)
		}
	}
}
