package signer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohstr/ncli/client/nipcrypto"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/nip46"
)

// syncBuffer is a goroutine-safe bytes.Buffer for decision logs.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *syncBuffer) records(t *testing.T) []Record {
	t.Helper()
	var out []Record
	for _, line := range strings.Split(strings.TrimSpace(s.String()), "\n") {
		if line == "" {
			continue
		}
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("bad record %q: %v", line, err)
		}
		out = append(out, r)
	}
	return out
}

type fakeClock struct{ unix atomic.Int64 }

func (c *fakeClock) set(t time.Time) { c.unix.Store(t.Unix()) }
func (c *fakeClock) now() time.Time  { return time.Unix(c.unix.Load(), 0) }

// sockDir returns a short directory for sockets (unix paths max ~108 bytes).
func sockDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sg")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

type running struct {
	srv       *Server
	uri       string
	decisions *syncBuffer
	denials   *syncBuffer
	stop      func()
}

func startServer(t *testing.T, cfg Config) *running {
	t.Helper()
	r := &running{decisions: &syncBuffer{}, denials: &syncBuffer{}}
	cfg.Decisions, cfg.Denials = r.decisions, r.denials
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sockDir(t), "s.sock")
	l, err := Listen(path, 0o660, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = srv.Serve(ctx, l)
		close(done)
	}()
	var once sync.Once
	r.stop = func() {
		once.Do(func() {
			cancel()
			<-done
			_ = srv.Close()
		})
	}
	t.Cleanup(r.stop)
	r.srv, r.uri = srv, URIScheme+"://"+path
	return r
}

func dial(t *testing.T, uri string) *Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func ctx5(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

const basicPolicy = `kind: signer-policy
rules:
  - name: notes
    allow: {kinds: [1]}
  - name: dm
    allow: {counterparts: [%s]}
`

func TestServerRoundTrip(t *testing.T) {
	priv, pub := testKey(t, "signer")
	friendPriv, friend := testKey(t, "friend")
	r := startServer(t, Config{PrivKeyHex: priv, PolicyPath: writePolicy(t, basicPolicy, friend), Version: "test"})
	c := dial(t, r.uri)
	ctx := ctx5(t)

	if c.PubKey() != pub {
		t.Fatalf("PubKey = %s, want %s", c.PubKey(), pub)
	}
	for method, want := range map[string]string{
		nip46.MethodPing:         "pong",
		nip46.MethodConnect:      "ack",
		nip46.MethodGetRelays:    "{}",
		nip46.MethodSwitchRelays: "null",
	} {
		if got, err := c.Call(ctx, method); err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", method, got, err, want)
		}
	}

	ev := &nip01.Event{CreatedAt: uint64(time.Now().Unix()), Kind: 1, Content: "hello", Tags: [][]string{{"t", "x"}}}
	if err := c.Sign(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if err := ev.Verify(); err != nil || ev.PubKey != pub {
		t.Fatalf("signed event: %v, pubkey %s", err, ev.PubKey)
	}

	for _, enc := range []struct{ encrypt, decrypt string }{
		{nip46.MethodNIP44Encrypt, nip46.MethodNIP44Decrypt},
		{nip46.MethodNIP04Encrypt, nip46.MethodNIP04Decrypt},
	} {
		ct, err := c.Call(ctx, enc.encrypt, npub(t, friend), "hi friend")
		if err != nil {
			t.Fatal(err)
		}
		if pt, err := nipcrypto.Do(enc.decrypt, friendPriv, []string{pub, ct}); err != nil || pt != "hi friend" {
			t.Fatalf("%s: friend decrypts %q, %v", enc.encrypt, pt, err)
		}
		reply, _ := nipcrypto.Do(enc.encrypt, friendPriv, []string{pub, "hi back"})
		if pt, err := c.Call(ctx, enc.decrypt, friend, reply); err != nil || pt != "hi back" {
			t.Fatalf("%s = %q, %v", enc.decrypt, pt, err)
		}
	}

	st, err := c.Status(ctx)
	if err != nil || st.PubKey != pub || st.Rules != 2 || st.PolicySHA256 != r.srv.Policy().SHA256 || st.Version != "test" {
		t.Fatalf("status = %+v, %v", st, err)
	}

	_, err = c.Call(ctx, "get_secret_key")
	var re *RemoteError
	if !errors.As(err, &re) || !re.Invalid() {
		t.Fatalf("unknown method: %v", err)
	}
}

func TestServerDenials(t *testing.T) {
	priv, _ := testKey(t, "signer")
	_, friend := testKey(t, "friend")
	_, stranger := testKey(t, "stranger")
	_, other := testKey(t, "other")
	r := startServer(t, Config{PrivKeyHex: priv, PolicyPath: writePolicy(t, basicPolicy, friend)})
	c := dial(t, r.uri)
	ctx := ctx5(t)
	now := uint64(time.Now().Unix())

	nsec, _ := nip19.EncodePrivateKey(priv)
	cases := []struct {
		name   string
		ev     *nip01.Event
		reason string
	}{
		{"unlisted kind", &nip01.Event{CreatedAt: now, Kind: 4, Content: "secret dm body"}, "no rule matches"},
		{"other pubkey", &nip01.Event{PubKey: other, CreatedAt: now, Kind: 1}, "event pubkey does not match the signer"},
		{"future", &nip01.Event{CreatedAt: now + 3600, Kind: 1}, "future"},
		{"own key in content", &nip01.Event{CreatedAt: now, Kind: 1, Content: "oops " + nsec}, "event contains the signer's key"},
	}
	for _, tc := range cases {
		err := c.Sign(ctx, tc.ev)
		var re *RemoteError
		if !errors.As(err, &re) || !re.Denied() || !strings.Contains(re.Msg, tc.reason) {
			t.Errorf("%s: err = %v, want denial containing %q", tc.name, err, tc.reason)
		}
	}
	if _, err := c.Call(ctx, nip46.MethodNIP44Decrypt, stranger, "x"); err == nil {
		t.Error("decrypt from an unlisted counterpart allowed")
	}
	if _, err := c.Call(ctx, nip46.MethodNIP44Encrypt, friend, "my key is "+priv); err == nil {
		t.Error("encrypting the signer's own key allowed")
	}
	if err := c.Sign(ctx, &nip01.Event{CreatedAt: now, Kind: 1, Content: "fine"}); err != nil {
		t.Fatal(err)
	}

	recs := r.decisions.records(t)
	if len(recs) != len(cases)+3 {
		t.Fatalf("got %d decision records, want %d:\n%s", len(recs), len(cases)+3, r.decisions)
	}
	denials := r.denials.records(t)
	if len(denials) != len(cases)+2 {
		t.Fatalf("got %d denial records, want %d", len(denials), len(cases)+2)
	}
	for _, d := range denials {
		if d.Decision != "deny" {
			t.Errorf("denials file holds %+v", d)
		}
	}
	if last := recs[len(recs)-1]; last.Decision != "allow" || last.Rule != "notes" || last.EventID == "" || last.Client.UID == nil {
		t.Errorf("allow record = %+v", last)
	}

	logs := r.decisions.String()
	for _, secret := range []string{priv, nsec, "secret dm body", "my key is"} {
		if strings.Contains(logs, secret) {
			t.Errorf("decision log leaks %.16q", secret)
		}
	}
}

func TestServerRateLimit(t *testing.T) {
	priv, _ := testKey(t, "signer")
	r := startServer(t, Config{PrivKeyHex: priv, PolicyPath: writePolicy(t, "kind: signer-policy\nrules: [{name: notes, allow: {kinds: [1]}, rate: 2/h}]")})
	c := dial(t, r.uri)
	ctx := ctx5(t)
	now := uint64(time.Now().Unix())
	for i := 0; i < 2; i++ {
		if err := c.Sign(ctx, &nip01.Event{CreatedAt: now, Kind: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Sign(ctx, &nip01.Event{CreatedAt: now, Kind: 1}); err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("third request: %v", err)
	}
}

func TestServerOversizedLine(t *testing.T) {
	priv, _ := testKey(t, "signer")
	r := startServer(t, Config{PrivKeyHex: priv, PolicyPath: writePolicy(t, "kind: signer-policy\nrules: [{name: n, deny: {}}]")})
	path, _ := ParseURI(r.uri)
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	go func() { _, _ = conn.Write(bytes.Repeat([]byte("x"), MaxMessageSize+10)) }()
	buf := make([]byte, 64)
	if n, err := conn.Read(buf); err == nil {
		t.Fatalf("server answered an oversized line: %q", buf[:n])
	}
}

func TestListen(t *testing.T) {
	dir := sockDir(t)
	path := filepath.Join(dir, "s.sock")
	l, err := Listen(path, 0o660, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o660 {
		t.Fatalf("mode = %v, %v", info.Mode().Perm(), err)
	}
	if _, err := Listen(path, 0o660, -1, -1); err == nil || !strings.Contains(err.Error(), "already listening") {
		t.Fatalf("live socket: %v", err)
	}
	_ = l.Close()

	// A socket file left behind by a dead process is replaced.
	stale, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = stale.Close()
	l, err = Listen(path, 0o600, -1, -1)
	if err != nil {
		t.Fatalf("stale socket: %v", err)
	}
	_ = l.Close()

	link := filepath.Join(dir, "link.sock")
	if err := os.Symlink(filepath.Join(dir, "elsewhere"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(link, 0o660, -1, -1); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink: %v", err)
	}
	file := filepath.Join(dir, "file")
	_ = os.WriteFile(file, nil, 0o600)
	if _, err := Listen(file, 0o660, -1, -1); err == nil || !strings.Contains(err.Error(), "not a socket") {
		t.Fatalf("regular file: %v", err)
	}
}

func TestReloadKeepsPolicyOnError(t *testing.T) {
	priv, _ := testKey(t, "signer")
	path := writePolicy(t, "kind: signer-policy\nrules: [{name: notes, allow: {kinds: [1]}}]")
	r := startServer(t, Config{PrivKeyHex: priv, PolicyPath: path})
	before := r.srv.Policy().SHA256

	_ = os.WriteFile(path, []byte("kind: signer-policy\nrules: [{name: x, allow: {}}]"), 0o600)
	if err := r.srv.Reload(); err == nil {
		t.Fatal("reload of a catch-all policy succeeded")
	}
	if r.srv.Policy().SHA256 != before {
		t.Fatal("bad reload replaced the policy")
	}

	_ = os.WriteFile(path, []byte("kind: signer-policy\nrules: [{name: notes, allow: {kinds: [1, 7]}}]"), 0o600)
	if err := r.srv.Reload(); err != nil || r.srv.Policy().SHA256 == before {
		t.Fatalf("good reload: %v", err)
	}
}

func TestNewRequiresStateDirForAttestations(t *testing.T) {
	priv, _ := testKey(t, "signer")
	_, approver := testKey(t, "approver")
	path := writePolicy(t, "kind: signer-policy\nrules: [{name: r, allow: {kinds: [1]}, require: {attestations: [{name: a, kinds: [9], authors: [%s], max_age: 1m, binds: [{tag: e, from: id}]}]}}]", approver)
	if _, err := New(Config{PrivKeyHex: priv, PolicyPath: path}); err == nil || !strings.Contains(err.Error(), "--state-dir") {
		t.Fatalf("err = %v", err)
	}
}

// TestReplayAcrossRestart covers both replay layers: the persisted used
// set, and the before-start check for a state dir that was lost.
func TestReplayAcrossRestart(t *testing.T) {
	priv, pub := testKey(t, "signer")
	approverPriv, approver := testKey(t, "approver")
	policy := writePolicy(t, `kind: signer-policy
rules:
  - name: guarded
    allow: {kinds: [30618]}
    max_clock_skew: 1m
    require:
      attestations:
        - {name: ok, kinds: [9], authors: [%s], max_age: 15m, binds: [{content: "approve {id}"}]}
`, approver)
	start := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	clock := func(at time.Time) func() time.Time { return func() time.Time { return at } }
	r1clock := &fakeClock{}
	r1clock.set(start)
	state := t.TempDir()

	tgt := target(t, pub, 30618, start.Add(time.Minute), "")
	att := signed(t, approverPriv, 9, start.Add(time.Minute), "approve "+tgt.ID)
	sign := func(r *running) error {
		ev := *tgt
		return dial(t, r.uri).SignWithAttestations(ctx5(t), &ev, []*nip01.Event{att})
	}

	r1 := startServer(t, Config{PrivKeyHex: priv, PolicyPath: policy, StateDir: state, Now: r1clock.now})
	r1clock.set(start.Add(time.Minute))
	if err := sign(r1); err != nil {
		t.Fatal(err)
	}
	if err := sign(r1); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("replay in the same run: %v", err)
	}
	r1.stop()

	r2 := startServer(t, Config{PrivKeyHex: priv, PolicyPath: policy, StateDir: state, Now: clock(start.Add(90 * time.Second))})
	if err := sign(r2); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("replay after restart with the same state dir: %v", err)
	}
	r2.stop()

	r3 := startServer(t, Config{PrivKeyHex: priv, PolicyPath: policy, StateDir: t.TempDir(), Now: clock(start.Add(5 * time.Minute))})
	if err := sign(r3); err == nil || !strings.Contains(err.Error(), "created before the signer started") {
		t.Fatalf("replay after restart with an empty state dir: %v", err)
	}
}

func TestWatchReloadsSymlinkSwap(t *testing.T) {
	priv, _ := testKey(t, "signer")
	dir := t.TempDir()
	// Mimic a Kubernetes ConfigMap mount: policy.yaml -> ..data/policy.yaml,
	// ..data -> a versioned directory swapped atomically.
	version := func(name, body string) {
		vdir := filepath.Join(dir, name)
		_ = os.Mkdir(vdir, 0o700)
		if err := os.WriteFile(filepath.Join(vdir, "policy.yaml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		tmp := filepath.Join(dir, "..data_tmp")
		_ = os.Remove(tmp)
		if err := os.Symlink(name, tmp); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, filepath.Join(dir, "..data")); err != nil {
			t.Fatal(err)
		}
	}
	version("v1", "kind: signer-policy\nrules: [{name: notes, allow: {kinds: [1]}}]")
	path := filepath.Join(dir, "policy.yaml")
	if err := os.Symlink(filepath.Join("..data", "policy.yaml"), path); err != nil {
		t.Fatal(err)
	}

	r := startServer(t, Config{PrivKeyHex: priv, PolicyPath: path})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.srv.Watch(ctx, 10*time.Millisecond)

	v1 := r.srv.Policy().SHA256
	version("v2", "kind: signer-policy\nrules: [{name: notes, allow: {kinds: [1, 7]}}]")
	deadline := time.Now().Add(5 * time.Second)
	for r.srv.Policy().SHA256 == v1 {
		if time.Now().After(deadline) {
			t.Fatal("watch did not pick up the swapped policy")
		}
		time.Sleep(10 * time.Millisecond)
	}
	v2 := r.srv.Policy().SHA256

	version("v3", "kind: nope")
	time.Sleep(200 * time.Millisecond)
	if r.srv.Policy().SHA256 != v2 {
		t.Fatal("a bad file replaced the active policy")
	}
}

func TestAllowUID(t *testing.T) {
	if !PeerCredSupported {
		t.Skip("SO_PEERCRED unsupported")
	}
	priv, _ := testKey(t, "signer")
	policy := writePolicy(t, "kind: signer-policy\nrules: [{name: notes, allow: {kinds: [1]}}]")

	r := startServer(t, Config{PrivKeyHex: priv, PolicyPath: policy, AllowUIDs: []int{os.Getuid() + 1}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if c, err := Dial(ctx, r.uri); err == nil {
		_ = c.Close()
		t.Fatal("a caller outside --allow-uid got an answer")
	}
	recs := r.denials.records(t)
	if len(recs) != 1 || recs[0].Reason != "peer uid/gid not allowed" || recs[0].Client.UID == nil {
		t.Fatalf("denials = %+v", recs)
	}

	ok := startServer(t, Config{PrivKeyHex: priv, PolicyPath: policy, AllowUIDs: []int{os.Getuid()}})
	dial(t, ok.uri)
	okGID := startServer(t, Config{PrivKeyHex: priv, PolicyPath: policy, AllowUIDs: []int{os.Getuid() + 1}, AllowGIDs: []int{os.Getgid()}})
	dial(t, okGID.uri)
}

func BenchmarkSignOverSocket(b *testing.B) {
	t := &testing.T{}
	priv, _ := testKey(t, "signer")
	dir, _ := os.MkdirTemp("", "sg")
	defer func() { _ = os.RemoveAll(dir) }()
	policy := filepath.Join(dir, "p.yaml")
	_ = os.WriteFile(policy, []byte("kind: signer-policy\nrules: [{name: notes, allow: {kinds: [1]}}]"), 0o600)
	srv, err := New(Config{PrivKeyHex: priv, PolicyPath: policy})
	if err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(dir, "s.sock")
	l, err := Listen(path, 0o600, -1, -1)
	if err != nil {
		b.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx, l) }()
	c, err := Dial(ctx, URIScheme+"://"+path)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ev := &nip01.Event{CreatedAt: uint64(time.Now().Unix()), Kind: 1, Content: "bench"}
		if err := c.Sign(ctx, ev); err != nil {
			b.Fatal(err)
		}
	}
}
