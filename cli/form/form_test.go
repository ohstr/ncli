package form

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/ncli/cli/social"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip59"
	"github.com/ohstr/nmilat/nippf"
	"github.com/spf13/cobra"
)

// fakeNet is relays in memory: each relay URL its events.
type fakeNet struct {
	mu     sync.Mutex
	relays map[string][]*nip01.Event
	refuse func(relay string, ev *nip01.Event) string // a refusal's reason, "" to accept
}

func (n *fakeNet) Query(_ context.Context, relays []string, filters *nip01.SubscriptionFilterGroup, _ time.Duration, _ client.Signer) ([]*nip01.Event, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []*nip01.Event
	seen := map[string]bool{}
	for _, r := range relays {
		for _, ev := range n.relays[r] {
			if !seen[ev.ID] && filters.Match(ev) {
				seen[ev.ID] = true
				out = append(out, ev)
			}
		}
	}
	return out, nil
}

func (n *fakeNet) Publish(_ context.Context, relays []string, events []*nip01.Event, _ client.Signer) (*client.PublishReport, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	rep := &client.PublishReport{}
	for _, r := range relays {
		for _, ev := range events {
			rep.Attempted++
			res := client.PublishResult{ID: ev.ID, Relay: r}
			if n.refuse != nil {
				if why := n.refuse(r, ev); why != "" {
					res.Error = why
					rep.Failed++
					rep.Results = append(rep.Results, res)
					continue
				}
			}
			n.relays[r] = append(n.relays[r], ev.Copy())
			res.Accepted = true
			rep.Succeeded++
			rep.Results = append(rep.Results, res)
		}
	}
	return rep, nil
}

func (n *fakeNet) count(relay string, kind int) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := 0
	for _, ev := range n.relays[relay] {
		if ev.Kind == kind {
			c++
		}
	}
	return c
}

type actor struct {
	priv, pub string
}

func newActor(t *testing.T, b byte) actor {
	t.Helper()
	priv := strings.Repeat(string("0123456789abcdef"[b]), 64)
	s, err := client.NewLocalSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	return actor{priv: priv, pub: s.PubKey()}
}

// setup swaps the network, the signer (--signer is a hex key here) and the
// clock.
func setup(t *testing.T) *fakeNet {
	t.Helper()
	n := &fakeNet{relays: map[string][]*nip01.Event{}}
	oldNet, oldSigner, oldClock := network, signerFor, clock
	network = n
	signerFor = func(cmd *cobra.Command) (client.Crypto, func(), error) {
		v, _ := cmd.Flags().GetString("signer")
		s, err := client.NewLocalSigner(v)
		if err != nil {
			return nil, nil, err
		}
		return s, func() {}, nil
	}
	start := time.Unix(1793000000, 0)
	var tick int64
	var mu sync.Mutex
	clock = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		tick++
		return start.Add(time.Duration(tick) * time.Second)
	}
	t.Cleanup(func() { network, signerFor, clock = oldNet, oldSigner, oldClock })
	return n
}

type run struct {
	out, errOut string
	err         error
}

func ncli(t *testing.T, stdin string, args ...string) run {
	t.Helper()
	root := &cobra.Command{Use: "ncli", SilenceErrors: true, SilenceUsage: true}
	root.PersistentFlags().Bool("json", false, "")
	root.PersistentFlags().BoolP("quiet", "q", true, "")
	root.AddCommand(NewFormCommand())
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"form"}, args...))
	err := root.Execute()
	return run{out.String(), errOut.String(), err}
}

func mustOK(t *testing.T, stdin string, args ...string) run {
	t.Helper()
	r := ncli(t, stdin, args...)
	if r.err != nil {
		t.Fatalf("ncli form %s: %v\nstdout: %s\nstderr: %s", strings.Join(args, " "), r.err, r.out, r.errOut)
	}
	return r
}

func mustFail(t *testing.T, args ...string) run {
	t.Helper()
	r := ncli(t, "", args...)
	if r.err == nil {
		t.Fatalf("ncli form %s succeeded\n%s", strings.Join(args, " "), r.out)
	}
	return r
}

func decode[T any](t *testing.T, r run) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(r.out), &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, r.out)
	}
	return v
}

// The three-question example of skills/ncli-forms, with keys of ours.
func specFile(t *testing.T, responders []string, extra string) string {
	t.Helper()
	rs, _ := json.Marshal(responders)
	s := `{"title": "hello crash-loops since 14:02",
 "context": "deploy/hello crash-loops since main's commit 9c1e0f2.",
 "e": ["` + strings.Repeat("ab", 32) + `"],
 "responders": ` + string(rs) + `,
 "quorum": 1,
 "priority": "high",
 "endsAt": 1793460000` + extra + `,
 "questions": [
  {"text": "How do we fix hello?", "type": "singlechoice",
   "why": "The revert brings back what ran for 3 days.",
   "options": [
    {"id": "revert", "label": "Merge my revert PR", "rec": true},
    {"id": "forward", "label": "Fix forward in a new PR", "detail": "Keeps 9c1e0f2; hello stays down until the fix lands."},
    {"id": "other", "label": "Other (say what)", "noteRequired": true}]},
  {"text": "Raise hello's memory limit to (MiB)", "type": "number", "optional": true,
   "min": "256", "max": 2048},
  {"text": "Deploy no later than", "type": "time", "optional": true, "max": "1793480000"}]}`
	p := filepath.Join(t.TempDir(), "spec.json")
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadSpec(t *testing.T) {
	alice := newActor(t, 1)
	fh, err := os.Open(specFile(t, []string{alice.pub}, ""))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fh.Close() }()
	s, err := ReadSpec(fh)
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.Form(func(v string) (string, error) { return v, nil })
	if err != nil {
		t.Fatal(err)
	}
	ev, err := f.Event(alice.pub, 1793000000)
	if err != nil {
		t.Fatal(err)
	}
	back, err := nippf.ParseForm(ev, false)
	if err != nil {
		t.Fatal(err)
	}
	q1 := back.Questions[1]
	if len(back.Questions) != 3 || q1.Max != "2048" || q1.Min != "256" || !q1.Optional || !back.Priority || back.Quorum != 1 ||
		back.Events[0] != strings.Repeat("ab", 32) || back.Alt != "Form: hello crash-loops since 14:02" {
		t.Fatalf("form %+v", back)
	}
	if o, _, _ := back.Option("revert"); !o.Rec {
		t.Fatal("rec lost")
	}
	if o, _, _ := back.Option("other"); !o.NoteRequired {
		t.Fatal("noteRequired lost")
	}

	for name, bad := range map[string]string{
		"unknown key":   `{"title": "t", "questions": [], "respondrs": []}`,
		"bad priority":  `{"title": "t", "priority": "urgent", "questions": [{"text": "q", "type": "text"}]}`,
		"bad results":   `{"title": "t", "results": "some", "questions": [{"text": "q", "type": "text"}]}`,
		"twice":         `{"title": "t"} {"title": "u"}`,
		"default:false": `{"title": "t", "questions": [{"text": "q", "type": "text", "default": false}]}`,
	} {
		s, err := ReadSpec(strings.NewReader(bad))
		if err == nil {
			_, err = s.Form(func(v string) (string, error) { return v, nil })
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// a string where a list goes, and a list default
	s, err = ReadSpec(strings.NewReader(`{"title": "t", "e": "` + strings.Repeat("cd", 32) + `",
	 "questions": [{"text": "q", "type": "multiplechoice", "options": [{"id": "a", "label": "A"}, {"id": "b", "label": "B"}], "default": ["a", "b"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.E) != 1 || len(s.Questions[0].Default) != 2 {
		t.Fatalf("spec %+v", s)
	}
	// nippf's rules apply when the event is built: a default naming no option
	s, _ = ReadSpec(strings.NewReader(`{"title": "t", "questions": [{"text": "q", "type": "singlechoice", "options": [{"id": "a", "label": "A"}], "default": "z"}]}`))
	f, _ = s.Form(func(v string) (string, error) { return v, nil })
	if _, err := f.Event(alice.pub, 1); err == nil {
		t.Fatal("a default naming no option was built")
	}
}

type viewJSON struct {
	Form struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Private  bool   `json:"private"`
		Group    string `json:"group"`
		ReadOnly bool   `json:"read_only"`
	} `json:"form"`
	Where      string `json:"where"`
	State      string `json:"state"`
	Role       string `json:"role"`
	CanRespond bool   `json:"can_respond"`
	Result     *struct {
		Status  string `json:"status"`
		Content string `json:"content"`
	} `json:"result"`
	Responses []struct {
		Author  string `json:"author"`
		Choices []struct {
			Option string `json:"option"`
			Note   string `json:"note"`
		} `json:"choices"`
		Answers []struct {
			Question int    `json:"question"`
			Value    string `json:"value"`
		} `json:"answers"`
	} `json:"responses"`
	Tally struct {
		Responses int  `json:"responses"`
		QuorumMet bool `json:"quorum_met"`
		Questions []struct {
			Counts map[string]int `json:"counts"`
		} `json:"questions"`
	} `json:"tally"`
	Mine    *struct{} `json:"mine"`
	Waiting []string  `json:"waiting"`
}

type deliveryJSON struct {
	ID    string `json:"id"`
	Kind  int    `json:"kind"`
	Form  string `json:"form"`
	Where string `json:"where"`
	Group string `json:"group"`
	To    []string
	Wraps []struct {
		To string `json:"to"`
		ID string `json:"id"`
	} `json:"wraps"`
	Report struct {
		Succeeded int `json:"succeeded"`
		Failed    int `json:"failed"`
	} `json:"report"`
}

const relay = "wss://inbox.example"

func TestPrivateForm(t *testing.T) {
	n := setup(t)
	alice, bob, carol, mallory := newActor(t, 1), newActor(t, 2), newActor(t, 3), newActor(t, 4)
	s := []string{"-s", relay, "--json"}
	as := func(a actor, args ...string) []string { return append(append(args, s...), "--signer", a.priv) }

	sent := decode[deliveryJSON](t, mustOK(t, "", as(alice, "create", "--spec", specFile(t, nil, ", \"expiration\": 1796000000"),
		"--to", social.Npub(bob.pub), "--to", carol.pub)...))
	if sent.Where != "private" || sent.Kind != nippf.KindForm || len(sent.Wraps) != 3 || sent.Report.Succeeded != 3 ||
		!slices.Equal(sent.To, []string{bob.pub, carol.pub}) {
		t.Fatalf("sent %+v", sent)
	}
	if n.count(relay, nippf.KindForm) != 0 || n.count(relay, nip59.KindGiftWrap) != 3 {
		t.Fatal("a private form must travel only in gift wraps")
	}

	// bob: it names him, open, his to answer
	list := decode[[]viewJSON](t, mustOK(t, "", as(bob, "list", "--open")...))
	if len(list) != 1 || list[0].Form.ID != sent.ID || !list[0].Form.Private || list[0].Role != "responder" || !list[0].CanRespond ||
		list[0].State != "open" || len(list[0].Waiting) != 2 {
		t.Fatalf("bob's list %+v", list)
	}
	// mallory sees nothing
	if l := decode[[]viewJSON](t, mustOK(t, "", as(mallory, "list")...)); len(l) != 0 {
		t.Fatalf("mallory's list %+v", l)
	}

	// refused before sending: "other" needs a note; a number out of bounds; an unknown option
	before := n.count(relay, nip59.KindGiftWrap)
	for _, bad := range [][]string{
		{"--choose", "other"},
		{"--choose", "revert", "--answer", "1=4096"},
		{"--choose", "nope"},
		{"--choose", "revert,forward"},
		{"--answer", "1=512"},
	} {
		r := ncli(t, "", as(bob, append([]string{"respond", sent.ID[:10]}, bad...)...)...)
		if r.err == nil {
			t.Fatalf("respond %v accepted\n%s", bad, r.out)
		}
	}
	if n.count(relay, nip59.KindGiftWrap) != before {
		t.Fatal("an invalid response was sent")
	}
	// mallory may not answer: the form isn't hers to read
	mustFail(t, as(mallory, "respond", sent.ID, "--choose", "revert")...)

	r := decode[deliveryJSON](t, mustOK(t, "", as(bob, "respond", sent.ID[:10], "--choose", "revert", "--note", "revert=after lunch",
		"--answer", "1=512", "--answer-note", "1=the cache needs it")...))
	// to alice and to bob himself (results: all not set)
	if r.Where != "private" || r.Kind != nippf.KindResponse || r.Form != sent.ID || len(r.Wraps) != 2 {
		t.Fatalf("response %+v", r)
	}
	// carol can't see bob's response (it went to alice only)
	cv := decode[viewJSON](t, mustOK(t, "", as(carol, "show", sent.ID)...))
	if cv.Tally.Responses != 0 {
		t.Fatalf("carol sees %+v", cv.Tally)
	}

	av := decode[viewJSON](t, mustOK(t, "", as(alice, "show", sent.ID)...))
	if av.Role != "author" || av.Tally.Responses != 1 || !av.Tally.QuorumMet || av.Tally.Questions[0].Counts["revert"] != 1 ||
		!slices.Equal(av.Waiting, []string{carol.pub}) || av.Responses[0].Choices[0].Note != "after lunch" {
		t.Fatalf("alice's view %+v", av)
	}
	text := mustOK(t, "", "show", sent.ID[:8], "-s", relay, "--signer", alice.priv).out
	for _, want := range []string{"[recommended]", "why: The revert brings back", "[note required]", "after lunch", "512 (the cache needs it)", "not answered: " + social.Npub(carol.pub), "priority high"} {
		if !strings.Contains(text, want) {
			t.Errorf("show text lacks %q\n%s", want, text)
		}
	}

	// only the author closes it; then it's decided for everyone
	mustFail(t, as(bob, "result", sent.ID, "--status", "cancelled")...)
	mustFail(t, as(alice, "result", sent.ID, "--status", "default")...) // no default tag
	res := decode[deliveryJSON](t, mustOK(t, "", as(alice, "result", sent.ID, "--status", "decided", "--choose", "revert", "--content", "reverted at 15:10")...))
	if res.Kind != nippf.KindResult || len(res.Wraps) != 3 {
		t.Fatalf("result %+v", res)
	}
	mustFail(t, as(alice, "result", sent.ID, "--status", "cancelled")...) // already closed
	bv := decode[viewJSON](t, mustOK(t, "", as(bob, "show", sent.ID)...))
	if bv.State != "decided" || bv.Result == nil || bv.Result.Content != "reverted at 15:10" || bv.CanRespond || bv.Mine == nil {
		t.Fatalf("bob's view after the result %+v", bv)
	}
	if l := decode[[]viewJSON](t, mustOK(t, "", as(bob, "list", "--open")...)); len(l) != 0 {
		t.Fatalf("a decided form listed as open %+v", l)
	}
	if l := decode[[]viewJSON](t, mustOK(t, "", as(carol, "list", "--state", "decided")...)); len(l) != 1 {
		t.Fatalf("carol's decided %+v", l)
	}
	// a response after the result is refused
	mustFail(t, as(carol, "respond", sent.ID, "--choose", "forward")...)
}

func TestPrivateResultsAllAndInbox(t *testing.T) {
	n := setup(t)
	alice, bob, carol := newActor(t, 1), newActor(t, 2), newActor(t, 3)
	// carol's DM relays (kind 10050) are elsewhere
	other := "wss://carol.example"
	dm := nip01.NewEvent(10050, "")
	dm.Tags = [][]string{{"relay", other}}
	dm.CreatedAt = 1792000000
	if err := dm.Sign(carol.priv); err != nil {
		t.Fatal(err)
	}
	n.relays[relay] = append(n.relays[relay], dm)

	// by default: -s only, never a recipient's DM relays
	mustOK(t, "", "create", "--spec", specFile(t, nil, ""), "--to", carol.pub, "-s", relay, "--signer", alice.priv)
	if n.count(other, nip59.KindGiftWrap) != 0 || n.count(relay, nip59.KindGiftWrap) != 2 {
		t.Fatalf("without --inbox: %d wraps on carol's DM relay, %d on -s", n.count(other, nip59.KindGiftWrap), n.count(relay, nip59.KindGiftWrap))
	}
	// --inbox: carol's wrap also goes to her DM relays (found on -s)
	sent := decode[deliveryJSON](t, mustOK(t, "", "create", "--spec", specFile(t, nil, ", \"results\": \"all\""), "--to", bob.pub, "--to", carol.pub,
		"-s", relay, "--inbox", "--json", "--signer", alice.priv))
	if n.count(other, nip59.KindGiftWrap) != 1 || n.count(relay, nip59.KindGiftWrap) != 5 {
		t.Fatalf("with --inbox: %d wraps on carol's DM relay, %d on -s", n.count(other, nip59.KindGiftWrap), n.count(relay, nip59.KindGiftWrap))
	}
	// carol reads from her DM relay too (--inbox)
	mustOK(t, "", "respond", sent.ID, "--choose", "forward", "-s", relay, "--inbox", "--signer", carol.priv)
	// results: all — bob sees carol's response
	bv := decode[viewJSON](t, mustOK(t, "", "show", sent.ID, "-s", relay, "--json", "--signer", bob.priv))
	if bv.Tally.Responses != 1 || bv.Responses[0].Author != carol.pub {
		t.Fatalf("bob's view %+v", bv)
	}
}

func TestGroupForm(t *testing.T) {
	n := setup(t)
	alice, bob, carol := newActor(t, 1), newActor(t, 2), newActor(t, 3)
	g := func(a actor, args ...string) []string {
		return append(args, "--group", "ops", "--relay", "wss://groups.example", "--json", "--signer", a.priv)
	}
	sent := decode[deliveryJSON](t, mustOK(t, "", g(alice, "create", "--spec", specFile(t, []string{social.Npub(bob.pub), carol.pub}, ""))...))
	if sent.Where != "group" || sent.Group != "ops" || n.count("wss://groups.example/", nippf.KindForm)+n.count("wss://groups.example", nippf.KindForm) != 1 {
		t.Fatalf("sent %+v %v", sent, n.relays)
	}
	mustFail(t, "create", "--spec", specFile(t, nil, ""), "--group", "ops", "--signer", alice.priv)                 // no relay
	mustFail(t, append(g(alice, "create", "--spec", specFile(t, nil, "")), "--to", bob.pub)...)                     // --to and --group
	mustOK(t, "", g(bob, "respond", sent.ID, "--choose", "forward")...)                                             // bob answers
	mustOK(t, "", g(carol, "respond", sent.ID, "--choose", "revert", "--answer", "2=1793470000")...)                // carol answers
	mustOK(t, "", g(bob, "respond", sent.ID, "--choose", "other", "--note", "other=roll back the node instead")...) // bob changes his mind
	v := decode[viewJSON](t, mustOK(t, "", g(alice, "show", sent.ID)...))
	if v.Where != "group" || v.Tally.Responses != 2 || v.Tally.Questions[0].Counts["other"] != 1 || v.Tally.Questions[0].Counts["forward"] != 0 {
		t.Fatalf("view %+v", v)
	}
	w := decode[deliveryJSON](t, mustOK(t, "", g(bob, "withdraw", sent.ID)...))
	if w.Kind != nippf.KindResponse {
		t.Fatalf("withdrawal %+v", w)
	}
	v = decode[viewJSON](t, mustOK(t, "", g(alice, "show", sent.ID)...))
	if v.Tally.Responses != 1 || !slices.Equal(v.Waiting, []string{bob.pub}) {
		t.Fatalf("after the withdrawal %+v", v)
	}
	// list: every form of the group; --asked for bob; --mine for alice
	if l := decode[[]viewJSON](t, mustOK(t, "", g(bob, "list", "--asked")...)); len(l) != 1 {
		t.Fatalf("bob asked %+v", l)
	}
	if l := decode[[]viewJSON](t, mustOK(t, "", g(bob, "list", "--mine")...)); len(l) != 0 {
		t.Fatalf("bob's own %+v", l)
	}
	if l := decode[[]viewJSON](t, mustOK(t, "", g(alice, "list", "--mine")...)); len(l) != 1 {
		t.Fatalf("alice's own %+v", l)
	}
	// a group form answered from -s is refused (it would go elsewhere)
	mustFail(t, "respond", sent.ID, "--choose", "revert", "-s", "wss://groups.example", "--signer", carol.priv)
}

func TestPublicForm(t *testing.T) {
	n := setup(t)
	alice, bob, mallory := newActor(t, 1), newActor(t, 2), newActor(t, 3)
	sent := decode[deliveryJSON](t, mustOK(t, "", "create", "--spec", specFile(t, []string{bob.pub}, ""), "-s", relay, "--json", "--signer", alice.priv))
	if sent.Where != "public" || n.count(relay, nippf.KindForm) != 1 {
		t.Fatalf("sent %+v", sent)
	}
	ev := n.relays[relay][0]
	if !slices.ContainsFunc(ev.Tags, func(t []string) bool { return t[0] == "alt" && t[1] == "Form: hello crash-loops since 14:02" }) {
		t.Fatalf("no alt: %v", ev.Tags)
	}
	// --print sends nothing
	mustOK(t, "", "create", "--spec", specFile(t, nil, ""), "-s", relay, "--print", "--signer", alice.priv)
	if n.count(relay, nippf.KindForm) != 1 {
		t.Fatal("--print sent")
	}
	// mallory isn't a responder; she reads it by its full id but may not answer
	if r := ncli(t, "", "respond", sent.ID, "--choose", "revert", "-s", relay, "--signer", mallory.priv); r.err == nil || !strings.Contains(r.err.Error(), "not a responder") {
		t.Fatalf("mallory answered: %v", r.err)
	}
	// bob answers interactively: an unknown option, then revert, its note, skip, skip, send
	r := mustOK(t, "nope\n1\nlooks right\n\n\ny\n", "respond", sent.ID[:12], "-s", relay, "--json", "--signer", bob.priv)
	if !strings.Contains(r.errOut, "— recommended") || !strings.Contains(r.errOut, `"nope" is no option`) || !strings.Contains(r.errOut, "why recommended: The revert") {
		t.Fatalf("prompts:\n%s", r.errOut)
	}
	v := decode[viewJSON](t, mustOK(t, "", "show", sent.ID, "-s", relay, "--json", "--signer", alice.priv))
	if v.Tally.Responses != 1 || v.Responses[0].Choices[0].Option != "revert" || v.Responses[0].Choices[0].Note != "looks right" {
		t.Fatalf("view %+v", v)
	}
	// interactive, but the input ends: nothing sent
	before := n.count(relay, nippf.KindResponse)
	if r := ncli(t, "3\n", "respond", sent.ID, "-s", relay, "--signer", bob.priv); r.err == nil {
		t.Fatal("an unfinished response was sent")
	}
	// interactive, answered "n" at the end: nothing sent
	if r := ncli(t, "2\n\n\n\nn\n", "respond", sent.ID, "-s", relay, "--signer", bob.priv); r.err == nil {
		t.Fatal("a declined response was sent")
	}
	if n.count(relay, nippf.KindResponse) != before {
		t.Fatal("sent anyway")
	}
	// a result closes it; text list says so
	mustOK(t, "", "result", sent.ID, "--status", "cancelled", "--content", "asked wrong", "-s", relay, "--signer", alice.priv)
	out := mustOK(t, "", "list", "-s", relay, "--signer", bob.priv).out
	if !strings.Contains(out, "cancelled") || !strings.Contains(out, sent.ID[:12]) {
		t.Fatalf("list\n%s", out)
	}
}

// An option's detail travels as its detail tag and prints on the line under
// the option, in form show and in respond's prompts; --json carries it.
func TestOptionDetail(t *testing.T) {
	n := setup(t)
	alice, bob := newActor(t, 1), newActor(t, 2)
	sent := decode[deliveryJSON](t, mustOK(t, "", "create", "--spec", specFile(t, []string{bob.pub}, ""), "-s", relay, "--json", "--signer", alice.priv))
	detail := "Keeps 9c1e0f2; hello stays down until the fix lands."
	if ev := n.relays[relay][0]; !slices.ContainsFunc(ev.Tags, func(t []string) bool {
		return len(t) == 3 && t[0] == "detail" && t[1] == "forward" && t[2] == detail
	}) {
		t.Fatalf("no detail tag: %v", ev.Tags)
	}
	out := mustOK(t, "", "show", sent.ID, "-s", relay, "--signer", bob.priv).out
	if !strings.Contains(out, "    - forward: Fix forward in a new PR  — 0\n      "+detail+"\n") {
		t.Fatalf("show:\n%s", out)
	}
	var v struct {
		Form *nippf.Form `json:"form"`
	}
	if err := json.Unmarshal([]byte(mustOK(t, "", "show", sent.ID, "-s", relay, "--json", "--signer", bob.priv).out), &v); err != nil {
		t.Fatal(err)
	}
	if o := v.Form.Questions[0].Options; o[1].Detail != detail || o[0].Detail != "" {
		t.Fatalf("--json options %+v", o)
	}
	r := mustOK(t, "2\n\n\n\ny\n", "respond", sent.ID, "-s", relay, "--signer", bob.priv)
	if !strings.Contains(r.errOut, "  2) Fix forward in a new PR\n     "+detail+"\n") {
		t.Fatalf("prompts:\n%s", r.errOut)
	}
}

func TestReadOnlyForm(t *testing.T) {
	n := setup(t)
	alice, bob := newActor(t, 1), newActor(t, 2)
	ev := nip01.NewEvent(nippf.KindForm, "Rate it")
	ev.Tags = [][]string{{"alt", "Form: Rate it"}, {"p", bob.pub}, {"question", "0", "How many stars?", "stars"}}
	ev.CreatedAt = 1793000000
	if err := ev.Sign(alice.priv); err != nil {
		t.Fatal(err)
	}
	n.relays[relay] = append(n.relays[relay], ev)
	v := decode[viewJSON](t, mustOK(t, "", "show", ev.ID[:8], "-s", relay, "--json", "--signer", bob.priv))
	if !v.Form.ReadOnly || v.CanRespond {
		t.Fatalf("view %+v", v)
	}
	if !strings.Contains(mustOK(t, "", "show", ev.ID, "-s", relay, "--signer", bob.priv).out, "read-only") {
		t.Fatal("text doesn't say read-only")
	}
	if r := ncli(t, "", "respond", ev.ID, "--answer", "0=5", "-s", relay, "--signer", bob.priv); r.err == nil || !strings.Contains(r.err.Error(), "read-only") {
		t.Fatalf("answered a read-only form: %v", r.err)
	}
}

func TestFoldSameWayOnly(t *testing.T) {
	alice, bob := newActor(t, 1), newActor(t, 2)
	f := &nippf.Form{Title: "t", Private: true, Parties: []nippf.Party{{PubKey: bob.pub, Role: nippf.RoleResponder}},
		Questions: []nippf.Question{{Index: 0, Text: "q", Type: nippf.TypeSingle, Options: []nippf.Option{{ID: "a", Label: "A"}}}}}
	ev, err := f.Event(alice.pub, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := nip59.PrepareRumor(ev, alice.pub); err != nil {
		t.Fatal(err)
	}
	back, err := nippf.ParseForm(ev, true)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := back.NewResponse(bob.pub, 101, []nippf.Choice{{Option: "a"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// a signed public response naming a private form doesn't count
	signed := resp.Copy()
	if err := signed.Sign(bob.priv); err != nil {
		t.Fatal(err)
	}
	views, invalid := fold([]item{{ev: ev, private: true}, {ev: signed}}, bob.pub, 200)
	if invalid != 0 || len(views) != 1 || views[0].Tally.Responses != 0 {
		t.Fatalf("views %+v", views[0])
	}
	// the same response as a rumor does
	rumor := resp.Copy()
	if err := nip59.PrepareRumor(rumor, bob.pub); err != nil {
		t.Fatal(err)
	}
	views, _ = fold([]item{{ev: ev, private: true}, {ev: rumor, private: true}}, bob.pub, 200)
	if views[0].Tally.Responses != 1 || views[0].Mine == nil || views[0].Role != "responder" || views[0].Waiting != nil {
		t.Fatalf("views %+v", views[0])
	}
	// an invalid form is counted, not shown
	bad := nip01.NewEvent(nippf.KindForm, "x")
	bad.Tags = [][]string{{"question", "1", "q", "text"}}
	bad.CreatedAt = 1
	_ = nip59.PrepareRumor(bad, alice.pub)
	if views, invalid := fold([]item{{ev: bad, private: true}}, bob.pub, 200); invalid != 1 || len(views) != 0 {
		t.Fatalf("invalid %d views %d", invalid, len(views))
	}
}

// A form's t tags are its labels: list and show print them, escaped, beside its author and before
// any of the author's text.
func TestFormLabels(t *testing.T) {
	alice, bob := newActor(t, 1), newActor(t, 2)
	f := &nippf.Form{Title: "Notice: PR 1", Private: true, Parties: []nippf.Party{{PubKey: bob.pub, Role: nippf.RoleResponder}},
		Questions: []nippf.Question{{Index: 0, Text: "q", Type: nippf.TypeSingle, Options: []nippf.Option{{ID: "a", Label: "A"}}}}}
	build := func(tags ...[]string) *nip01.Event {
		ev, err := f.Event(alice.pub, 100)
		if err != nil {
			t.Fatal(err)
		}
		ev.Tags = append(ev.Tags, tags...)
		if err := nip59.PrepareRumor(ev, alice.pub); err != nil {
			t.Fatal(err)
		}
		return ev
	}
	labelled := build([]string{"t", "release-ok"}, []string{"t", "release-ok"}, []string{"t", "x\x1b[2J"})
	plain := build()
	views, invalid := fold([]item{{ev: labelled, private: true}, {ev: plain, private: true}}, bob.pub, 200)
	if invalid != 0 || len(views) != 2 {
		t.Fatalf("views %d invalid %d", len(views), invalid)
	}
	var mv, pv *View
	for _, v := range views {
		if v.Form.ID == labelled.ID {
			mv = v
		} else {
			pv = v
		}
	}
	if mv == nil || pv == nil || strings.Join(mv.Labels, ",") != "release-ok,x\x1b[2J" || pv.Labels != nil {
		t.Fatalf("labels: %+v / %+v", mv, pv)
	}
	if l := listLine(mv); !strings.Contains(l, "#release-ok #x\\x1b[2J") || strings.Contains(listLine(pv), "#") {
		t.Fatalf("form list: %q / %q", l, listLine(pv))
	}
	var show bytes.Buffer
	writeView(&show, mv)
	rawControl(t, "form show", show.String())
	// the labels line first, before any of the author's text
	if lines := strings.Split(show.String(), "\n"); !strings.HasPrefix(lines[0], "labels: #release-ok #x\\x1b[2J (its author's: ") || !strings.HasPrefix(lines[1], "form ") {
		t.Fatalf("form show: %s", show.String())
	}
	var plainShow bytes.Buffer
	writeView(&plainShow, pv)
	if strings.Contains(plainShow.String(), "labels:") {
		t.Fatalf("form show, no labels: %s", plainShow.String())
	}
	// a look-alike of a label (U+0261 script g, Coptic o) prints escaped, never as the label
	fake := build([]string{"t", "reɡister-ok"}, []string{"t", "release-ⲟk"})
	views, _ = fold([]item{{ev: fake, private: true}}, bob.pub, 200)
	if len(views) != 1 {
		t.Fatal("the look-alike's view")
	}
	if l := listLine(views[0]); !strings.Contains(l, `#re\u0261ister-ok #release-\u2c9fk`) || strings.Contains(l, "#release-ok") {
		t.Fatalf("form list, a look-alike: %q", l)
	}
}

func TestInteractiveCreate(t *testing.T) {
	setup(t)
	alice, bob := newActor(t, 1), newActor(t, 2)
	in := strings.Join([]string{
		"Retire neuron claude-3?", "It refused every login since 10-28.",
		"What should happen to claude-3?", "", // singlechoice
		"retire", "Retire it", "Stops it for good.", "y", "n",
		"wait", "Keep it two more days", "", "n", "n",
		"", "y", // finish options; add Other
		"Two days of refused logins.", "n", "",
		"Retire by", "date", "", "n", "2026-11-01", "2026-12-31", "", // a date question
		"", // no more questions
		bob.pub, "", "1", "3d", "", "n",
	}, "\n") + "\n"
	r := mustOK(t, in, "create", "-s", relay, "--print", "--json", "--signer", alice.priv)
	var out struct {
		Form *nippf.Form  `json:"form"`
		Ev   *nip01.Event `json:"event"`
	}
	if err := json.Unmarshal([]byte(r.out), &out); err != nil {
		t.Fatalf("%v\n%s\n%s", err, r.out, r.errOut)
	}
	f := out.Form
	if f.Title != "Retire neuron claude-3?" || len(f.Questions) != 2 || len(f.Questions[0].Options) != 3 || !f.Questions[0].Options[0].Rec ||
		f.Questions[0].Options[0].Detail != "Stops it for good." || f.Questions[0].Options[1].Detail != "" ||
		!f.Questions[0].Options[2].NoteRequired || f.Questions[1].Min != "2026-11-01" || f.Quorum != 1 || f.EndsAt == 0 ||
		!slices.Equal(f.Responders(), []string{bob.pub}) {
		t.Fatalf("form %+v", f)
	}
}
