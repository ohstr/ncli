package event

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/nip59"
	"github.com/ohstr/nmilat/wire"
)

const (
	alicePriv = "1111111111111111111111111111111111111111111111111111111111111111"
	bobPriv   = "2222222222222222222222222222222222222222222222222222222222222222"
)

func pubOf(t *testing.T, priv string) string {
	t.Helper()
	pub, err := client.GetPublicKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

func signedAt(t *testing.T, priv string, kind int, createdAt uint64, content string, tags ...[]string) *nip01.Event {
	t.Helper()
	ev := nip01.NewEvent(kind, content, tags...)
	if ev.Tags == nil {
		ev.Tags = [][]string{}
	}
	if createdAt != 0 {
		ev.CreatedAt = createdAt
	}
	if err := ev.Sign(priv); err != nil {
		t.Fatal(err)
	}
	return ev
}

func signed(t *testing.T, priv string, kind int, content string, tags ...[]string) *nip01.Event {
	return signedAt(t, priv, kind, 0, content, tags...)
}

// fakeRelay answers REQs from its stored events by filter, and stores
// EVENTs it is sent.
type fakeRelay struct {
	URL string
	mu  sync.Mutex
	evs []*nip01.Event
}

func (f *fakeRelay) stored() []*nip01.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*nip01.Event{}, f.evs...)
}

func newFakeRelay(t *testing.T, evs ...*nip01.Event) *fakeRelay {
	t.Helper()
	f := &fakeRelay{evs: evs}
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var payload wire.RelayPayload
			if err := json.Unmarshal(data, &payload); err != nil {
				return
			}
			switch p := payload.Packet.(type) {
			case *wire.RequestPacket:
				for _, ev := range f.stored() {
					if p.Filters.Match(ev) {
						b, _ := json.Marshal([]any{"EVENT", p.SubscriptionID, ev})
						_ = conn.WriteMessage(websocket.TextMessage, b)
					}
				}
				b, _ := (&wire.EOSESubscriptionResponse{SubscriptionID: p.SubscriptionID}).MarshalJSON()
				_ = conn.WriteMessage(websocket.TextMessage, b)
			case *wire.EventPacket:
				f.mu.Lock()
				f.evs = append(f.evs, p.Event)
				f.mu.Unlock()
				b, _ := (&wire.OkSubscriptionResponse{EventID: p.Event.ID, Accepted: true}).MarshalJSON()
				_ = conn.WriteMessage(websocket.TextMessage, b)
			}
		}
	}))
	t.Cleanup(srv.Close)
	f.URL = "ws://" + srv.Listener.Addr().String()
	return f
}

// run executes `event <args>` with stdin and returns stdout.
func run(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	cmd := NewEventCommand()
	cmd.PersistentFlags().Bool("json", true, "")
	cmd.PersistentFlags().BoolP("quiet", "q", true, "")
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(append(args, "--timeout", "5s"))
	err := cmd.Execute()
	return out.String(), err
}

func codeOf(err error) common.ErrorCode {
	var ce *common.CLIError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}

func nevent(t *testing.T, ev *nip01.Event, relays ...string) string {
	t.Helper()
	s, err := nip19.EncodeEvent(nip19.EventPointer{ID: ev.ID, Relays: relays, Author: ev.PubKey, Kind: ev.Kind})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDecodeEvents_Shapes(t *testing.T) {
	ev := &nip01.Event{ID: "a", Kind: 1, Tags: [][]string{}}
	obj := mustJSON(t, ev)
	cases := []struct {
		name  string
		in    string
		count int
		list  bool
	}{
		{"object", obj, 1, false},
		{"array", "[" + obj + "," + obj + "]", 2, true},
		{"ndjson", obj + "\n" + obj + "\n", 2, true},
		{"relay message", `["EVENT","sub",` + obj + `]`, 1, false},
		{"one-item array", "[" + obj + "]", 1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, list, err := decodeEvents([]byte(c.in))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != c.count || list != c.list {
				t.Fatalf("got %d events list=%v, want %d list=%v", len(got), list, c.count, c.list)
			}
		})
	}
	if _, _, err := decodeEvents([]byte(`["NOTICE","hi"]`)); err == nil {
		t.Fatal("a NOTICE carries no event and should be rejected")
	}
}

func TestParseArg_References(t *testing.T) {
	alice := pubOf(t, alicePriv)
	refs, _, err := parseArg("30023:" + alice + ":my-post")
	if err != nil || len(refs) != 1 || !refs[0].Addr || refs[0].Kind != 30023 || refs[0].D != "my-post" {
		t.Fatalf("coordinate: %+v %v", refs, err)
	}
	ev := signed(t, alicePriv, 1, "hi")
	refs, _, err = parseArg("nostr:" + nevent(t, ev, "wss://hint.example"))
	if err != nil || refs[0].ID != ev.ID || refs[0].Author != ev.PubKey || refs[0].Kind != 1 || refs[0].Hints[0] != "wss://hint.example" {
		t.Fatalf("nevent: %+v %v", refs, err)
	}
	if _, _, err := parseArg("garbage"); err == nil {
		t.Fatal("garbage should not parse")
	}
}

func TestVerify_FromNeventHintWithoutRelays(t *testing.T) {
	ev := signed(t, alicePriv, 1, "hello")
	relay := newFakeRelay(t, ev)

	out, err := run(t, "", "verify", nevent(t, ev, relay.URL))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	var rep verifyReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if rep.Status != "valid" || rep.Source != relay.URL {
		t.Fatalf("got status %q from %q", rep.Status, rep.Source)
	}
}

func TestVerify_SkipsTamperedCopyForAValidOne(t *testing.T) {
	ev := signed(t, alicePriv, 1, "original")
	tampered := *ev
	tampered.Content = "forged"
	liar := newFakeRelay(t, &tampered)
	honest := newFakeRelay(t, ev)

	out, err := run(t, "", "verify", ev.ID, "-s", liar.URL+","+honest.URL)
	if err != nil {
		t.Fatalf("verify: %v\n%s", err, out)
	}
	var rep verifyReport
	_ = json.Unmarshal([]byte(out), &rep)
	if rep.Status != "valid" || rep.Source != honest.URL {
		t.Fatalf("want the honest copy, got %q from %q", rep.Status, rep.Source)
	}

	_, err = run(t, "", "verify", ev.ID, "-s", liar.URL)
	if codeOf(err) != common.CodeInternal {
		t.Fatalf("a tampered copy alone must fail verification, got %v", err)
	}

	_, err = run(t, "", "verify", ev.ID, "-s", liar.URL+","+honest.URL, "--cross-relays")
	if codeOf(err) != common.CodeInternal {
		t.Fatalf("--cross-relays must fail on the tampered copy, got %v", err)
	}
}

func TestVerify_AuthorAndKind(t *testing.T) {
	ev := signed(t, alicePriv, 1, "hi")
	in := mustJSON(t, ev)
	bob, _ := nip19.EncodePublicKey(pubOf(t, bobPriv))
	if _, err := run(t, in, "verify", "--offline", "--author", bob); codeOf(err) != common.CodeInternal {
		t.Fatalf("wrong author must fail, got %v", err)
	}
	if _, err := run(t, in, "verify", "--offline", "--kind", "7"); codeOf(err) != common.CodeInternal {
		t.Fatalf("wrong kind must fail, got %v", err)
	}
	if _, err := run(t, in, "verify", "--offline", "--author", ev.PubKey, "--kind", "1"); err != nil {
		t.Fatalf("matching expectations must pass, got %v", err)
	}
}

func TestVerify_Deleted(t *testing.T) {
	ev := signed(t, alicePriv, 1, "regret")
	del := signed(t, alicePriv, 5, "", []string{"e", ev.ID})
	notMine := signed(t, bobPriv, 5, "", []string{"e", ev.ID})

	relay := newFakeRelay(t, ev, notMine)
	if _, err := run(t, "", "verify", ev.ID, "-s", relay.URL, "--deleted"); err != nil {
		t.Fatalf("someone else's deletion must not count: %v", err)
	}
	relay = newFakeRelay(t, ev, del)
	if _, err := run(t, "", "verify", ev.ID, "-s", relay.URL, "--deleted"); codeOf(err) != common.CodeInternal {
		t.Fatalf("the author's deletion must fail, got %v", err)
	}
}

func TestGet_NaddrNewestAcrossRelays(t *testing.T) {
	old := signedAt(t, alicePriv, 30023, 1000, "v1", []string{"d", "post"})
	cur := signedAt(t, alicePriv, 30023, 2000, "v2", []string{"d", "post"})
	a, b := newFakeRelay(t, old), newFakeRelay(t, cur)

	addr, _ := nip19.EncodeAddr(nip19.EntityPointer{Identifier: "post", PublicKey: old.PubKey, Kind: 30023})
	out, err := run(t, "", "get", addr, "-s", a.URL+","+b.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	var got nip01.Event
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if got.Content != "v2" {
		t.Fatalf("want the newest version, got %q", got.Content)
	}
}

func TestGet_OutboxRelay(t *testing.T) {
	ev := signed(t, alicePriv, 1, "only on my outbox")
	outbox := newFakeRelay(t, ev)
	list := signed(t, alicePriv, 10002, "", []string{"r", outbox.URL, "write"})
	index := newFakeRelay(t, list)

	out, err := run(t, "", "get", nevent(t, ev), "-s", index.URL)
	if err != nil {
		t.Fatalf("get via outbox: %v", err)
	}
	if !strings.Contains(out, "only on my outbox") {
		t.Fatalf("got %s", out)
	}
	if _, err := run(t, "", "get", nevent(t, ev), "-s", index.URL, "--no-outbox"); codeOf(err) != common.CodeNotFound {
		t.Fatalf("--no-outbox should not find it, got %v", err)
	}
}

func TestGet_MissingClassification(t *testing.T) {
	ev := signed(t, alicePriv, 1, "nowhere")
	empty := newFakeRelay(t)
	if _, err := run(t, "", "get", ev.ID, "-s", empty.URL); codeOf(err) != common.CodeNotFound {
		t.Fatalf("empty relay: want not_found, got %v", err)
	}
	if _, err := run(t, "", "get", ev.ID, "-s", "ws://127.0.0.1:1"); codeOf(err) != common.CodeNetwork {
		t.Fatalf("unreachable relay: want network, got %v", err)
	}
}

func TestSeen_ReportsEachRelay(t *testing.T) {
	ev := signed(t, alicePriv, 1, "where am i")
	has, hasnt := newFakeRelay(t, ev), newFakeRelay(t)
	out, err := run(t, mustJSON(t, ev), "seen", "-s", has.URL+","+hasnt.URL, "--no-outbox")
	if err != nil {
		t.Fatalf("seen: %v", err)
	}
	var rep seenReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if rep.Found != 1 || rep.Asked != 2 {
		t.Fatalf("want found on 1 of 2, got %d of %d", rep.Found, rep.Asked)
	}
}

func TestRebroadcast_CopiesUnchanged(t *testing.T) {
	ev := signed(t, alicePriv, 1, "spread me")
	src, dst := newFakeRelay(t, ev), newFakeRelay(t)
	if _, err := run(t, "", "rebroadcast", ev.ID, "-s", src.URL, "--to", dst.URL); err != nil {
		t.Fatalf("rebroadcast: %v", err)
	}
	got := dst.stored()
	if len(got) != 1 || got[0].ID != ev.ID || got[0].Sig != ev.Sig {
		t.Fatalf("destination holds %+v", got)
	}

	bad := *ev
	bad.Content = "forged"
	if _, err := run(t, mustJSON(t, &bad), "rebroadcast", "--to", dst.URL); codeOf(err) != common.CodeInvalidInput {
		t.Fatalf("a forged event must be refused, got %v", err)
	}
}

func TestThread_ParentsAndReplies(t *testing.T) {
	root := signedAt(t, alicePriv, 1, 1000, "root")
	mid := signedAt(t, bobPriv, 1, 1001, "mid", []string{"e", root.ID, "", "root"})
	leaf := signedAt(t, alicePriv, 1, 1002, "leaf", []string{"e", root.ID, "", "root"}, []string{"e", mid.ID, "", "reply"})
	relay := newFakeRelay(t, root, mid, leaf)

	out, err := run(t, "", "thread", mid.ID, "-s", relay.URL, "--no-outbox")
	if err != nil {
		t.Fatalf("thread: %v", err)
	}
	var v threadView
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if len(v.Ancestors) != 1 || v.Ancestors[0].ID != root.ID {
		t.Fatalf("ancestors: %+v", v.Ancestors)
	}
	if len(v.Replies) != 1 || v.Replies[0].ID != leaf.ID {
		t.Fatalf("replies: %+v", v.Replies)
	}
}

func TestDiff_Tags(t *testing.T) {
	a := signedAt(t, alicePriv, 1, 1000, "same", []string{"t", "x"})
	b := signedAt(t, alicePriv, 1, 1000, "same", []string{"t", "y"})
	out, err := run(t, "", "diff", mustJSON(t, a), mustJSON(t, b), "--offline")
	if err != nil {
		t.Fatal(err)
	}
	var v diffView
	_ = json.Unmarshal([]byte(out), &v)
	if v.Same || len(v.TagsRemoved) != 1 || v.TagsRemoved[0][1] != "x" || len(v.TagsAdded) != 1 || v.TagsAdded[0][1] != "y" {
		t.Fatalf("got %+v", v)
	}
}

func TestUnwrap_GiftWrap(t *testing.T) {
	rumor := nip01.NewUnsignedEvent(14, pubOf(t, alicePriv), "secret")
	wrap, err := nip59.Wrap(rumor, alicePriv, pubOf(t, bobPriv))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := hex.DecodeString(bobPriv)
	bobNsec, _ := nip19.EncodePrivateKey(hex.EncodeToString(b))

	out, err := run(t, mustJSON(t, wrap), "unwrap", "--offline", "--identity", bobNsec)
	if err != nil {
		t.Fatalf("unwrap: %v", err)
	}
	var u unwrapped
	_ = json.Unmarshal([]byte(out), &u)
	if u.Rumor == nil || u.Rumor.Content != "secret" || u.Sender != pubOf(t, alicePriv) {
		t.Fatalf("got %+v", u)
	}

	alNsec, _ := nip19.EncodePrivateKey(alicePriv)
	if _, err := run(t, mustJSON(t, wrap), "unwrap", "--offline", "--identity", alNsec); codeOf(err) != common.CodeInvalidInput {
		t.Fatalf("the wrong key must not open it, got %v", err)
	}
}

func TestValidate_RelayList(t *testing.T) {
	good := signed(t, alicePriv, 10002, "", []string{"r", "wss://relay.example"})
	bad := signed(t, alicePriv, 10002, "", []string{"r", "https://not-a-relay.example"})
	if _, err := run(t, mustJSON(t, good), "validate", "--offline"); err != nil {
		t.Fatalf("good relay list: %v", err)
	}
	if _, err := run(t, mustJSON(t, bad), "validate", "--offline"); codeOf(err) != common.CodeInternal {
		t.Fatalf("bad relay list must be invalid, got %v", err)
	}
}

func TestIDAndNew(t *testing.T) {
	alice := pubOf(t, alicePriv)
	out, err := run(t, "", "new", "--content", "hi", "--tag", "t=x", "--tag-json", `["p","`+alice+`","","x"]`, "--pubkey", alice, "--created-at", "1700000000")
	if err != nil {
		t.Fatal(err)
	}
	var ev nip01.Event
	if err := json.Unmarshal([]byte(out), &ev); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if ev.PubKey != alice || len(ev.Tags) != 2 || ev.Tags[1][3] != "x" || ev.ID == "" {
		t.Fatalf("got %+v", ev)
	}
	hash, _ := ev.HashID()
	if hex.EncodeToString(hash) != ev.ID {
		t.Fatal("new filled a wrong id")
	}

	ev.Content = "changed"
	out, err = run(t, mustJSON(t, &ev), "id", "--id-only")
	if err != nil {
		t.Fatal(err)
	}
	hash, _ = ev.HashID()
	if strings.TrimSpace(out) != hex.EncodeToString(hash) {
		t.Fatalf("id: got %q", out)
	}
	if _, err := run(t, "", "id", ev.ID); codeOf(err) != common.CodeInvalidInput {
		t.Fatalf("id on a reference must be refused, got %v", err)
	}
}

func TestEncode_Hints(t *testing.T) {
	ev := signed(t, alicePriv, 30023, "post", []string{"d", "slug"})
	relay := newFakeRelay(t, ev)
	out, err := run(t, "", "encode", ev.ID, "-s", relay.URL, "--no-outbox")
	if err != nil {
		t.Fatal(err)
	}
	var r encodeResult
	_ = json.Unmarshal([]byte(out), &r)
	p, err := nip19.DecodeEvent(r.Nevent)
	if err != nil || len(p.Relays) != 1 || p.Relays[0] != relay.URL || p.Author != ev.PubKey {
		t.Fatalf("nevent %+v %v", p, err)
	}
	a, err := nip19.DecodeAddr(r.Naddr)
	if err != nil || a.Identifier != "slug" || a.Kind != 30023 {
		t.Fatalf("naddr %+v %v", a, err)
	}
}
