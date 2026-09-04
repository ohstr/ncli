package bunker

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClient is an in-memory BunkerClient stub for exercising ipc_server.go
// without a real Daemon -- keeps these tests scoped to the transport
// (framing, permissions, size limits) rather than re-testing daemon.go.
type revokedGrant struct {
	pubkey, method string
	kind           *int
}

type setGrant struct {
	pubkey string
	grant  Grant
}

type fakeClient struct {
	mu            sync.Mutex
	pending       []Pending
	sessions      []Session
	history       []HistoryEntry
	approved      []string
	rejected      []string
	revokedGrants []revokedGrant
	setGrants     []setGrant
	stopped       bool
	logs          LogSnapshot
}

func (f *fakeClient) Status() (StatusInfo, error) {
	return StatusInfo{IdentityPub: "abc123", Relays: []string{"wss://relay.example"}}, nil
}
func (f *fakeClient) ListPending() ([]Pending, error) { return f.pending, nil }
func (f *fakeClient) Approve(id string, remember *Grant) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.approved = append(f.approved, id)
	return nil
}
func (f *fakeClient) Reject(id string, remember *Grant) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rejected = append(f.rejected, id)
	return nil
}
func (f *fakeClient) ListSessions() ([]Session, error) { return f.sessions, nil }
func (f *fakeClient) History() ([]HistoryEntry, error) { return f.history, nil }
func (f *fakeClient) Revoke(pubkey string) (bool, error) {
	return pubkey == "known-app", nil
}
func (f *fakeClient) RevokeGrant(pubkey, method string, kind *int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revokedGrants = append(f.revokedGrants, revokedGrant{pubkey: pubkey, method: method, kind: kind})
	return pubkey == "known-app", nil
}
func (f *fakeClient) SetGrant(pubkey string, grant Grant) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setGrants = append(f.setGrants, setGrant{pubkey: pubkey, grant: grant})
	return nil
}
func (f *fakeClient) SetName(pubkey, name string) (bool, error) {
	return pubkey == "known-app", nil
}
func (f *fakeClient) Connect(uri string, spec *GrantSpec) (string, error) {
	if uri == "" {
		return "bunker://abc123?relay=wss://relay.example&secret=s3cr3t", nil
	}
	return "paired", nil
}
func (f *fakeClient) Logs() (LogSnapshot, error) { return f.logs, nil }
func (f *fakeClient) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	return nil
}
func (f *fakeClient) Close() error { return nil }

func startTestServer(t *testing.T, client BunkerClient) (socketPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets not supported on windows")
	}

	socketPath = filepath.Join(t.TempDir(), "bunker.sock")
	l, err := Listen(socketPath)
	if err != nil {
		t.Fatal(err)
	}

	srv := NewServer(l, client)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go srv.Serve(ctx)

	return socketPath
}

func TestIPC_SocketPermissions(t *testing.T) {
	socketPath := startTestServer(t, &fakeClient{})

	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("socket mode = %o, want 0600", perm)
	}

	dirInfo, err := os.Stat(filepath.Dir(socketPath))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0700 {
		t.Errorf("socket dir mode = %o, want 0700", perm)
	}
}

func TestIPC_StatusRoundTrip(t *testing.T) {
	socketPath := startTestServer(t, &fakeClient{})

	client, err := DialIPC(socketPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	st, err := client.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.IdentityPub != "abc123" {
		t.Errorf("IdentityPub = %q, want abc123", st.IdentityPub)
	}
}

func TestIPC_LogsRoundTrip(t *testing.T) {
	fc := &fakeClient{logs: LogSnapshot{Lines: []string{"line one", "line two"}, Total: 5}}
	socketPath := startTestServer(t, fc)

	client, err := DialIPC(socketPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	snap, err := client.Logs()
	if err != nil {
		t.Fatal(err)
	}
	if snap.Total != 5 {
		t.Errorf("Total = %d, want 5", snap.Total)
	}
	if len(snap.Lines) != 2 || snap.Lines[0] != "line one" || snap.Lines[1] != "line two" {
		t.Errorf("Lines = %v, want [line one, line two]", snap.Lines)
	}
}

func TestIPC_HistoryRoundTrip(t *testing.T) {
	want := []HistoryEntry{
		{ID: "req-1", ClientKey: "app1", Method: "sign_event", Kind: 1, Verdict: Allow, Remembered: true},
		{ID: "req-2", ClientKey: "app2", Method: "connect", Verdict: Deny, Expired: true},
	}
	fc := &fakeClient{history: want}
	socketPath := startTestServer(t, fc)

	client, err := DialIPC(socketPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	got, err := client.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "req-1" || got[0].Remembered != true || got[1].ID != "req-2" || got[1].Expired != true {
		t.Errorf("History() = %+v, want %+v to round-trip intact over IPC", got, want)
	}
}

func TestIPC_ApproveRejectRevokeConnect(t *testing.T) {
	fc := &fakeClient{}
	socketPath := startTestServer(t, fc)

	client, err := DialIPC(socketPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	if err := client.Approve("req-1", nil); err != nil {
		t.Fatal(err)
	}
	if err := client.Reject("req-2", nil); err != nil {
		t.Fatal(err)
	}

	revoked, err := client.Revoke("known-app")
	if err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Error("Revoke(known-app) = false, want true")
	}
	revoked, err = client.Revoke("unknown-app")
	if err != nil {
		t.Fatal(err)
	}
	if revoked {
		t.Error("Revoke(unknown-app) = true, want false")
	}

	updated, err := client.SetName("known-app", "My Wallet")
	if err != nil {
		t.Fatal(err)
	}
	if !updated {
		t.Error("SetName(known-app) = false, want true")
	}
	updated, err = client.SetName("unknown-app", "My Wallet")
	if err != nil {
		t.Fatal(err)
	}
	if updated {
		t.Error("SetName(unknown-app) = true, want false")
	}

	uri, err := client.Connect("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(uri, "bunker://") {
		t.Errorf("Connect(\"\") = %q, want a bunker:// URI", uri)
	}

	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.approved) != 1 || fc.approved[0] != "req-1" {
		t.Errorf("approved = %v, want [req-1]", fc.approved)
	}
	if len(fc.rejected) != 1 || fc.rejected[0] != "req-2" {
		t.Errorf("rejected = %v, want [req-2]", fc.rejected)
	}
}

func TestIPC_Stop(t *testing.T) {
	fc := &fakeClient{}
	socketPath := startTestServer(t, fc)

	client, err := DialIPC(socketPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	if err := client.Stop(); err != nil {
		t.Fatal(err)
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if !fc.stopped {
		t.Error("Stop() never reached the underlying client")
	}
}

func TestIPC_MalformedRequest_DoesNotCrashServer(t *testing.T) {
	socketPath := startTestServer(t, &fakeClient{})

	raw, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()

	_, _ = raw.Write([]byte("not json at all\n"))

	buf := make([]byte, 4096)
	_ = raw.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := raw.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	var resp ipcResponse
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		t.Fatalf("response wasn't valid JSON: %v", err)
	}
	if resp.OK {
		t.Error("expected ok=false for a malformed request")
	}

	// The server must still be alive for a fresh, well-formed client.
	client, err := DialIPC(socketPath, time.Second)
	if err != nil {
		t.Fatalf("server appears to have crashed after a malformed request: %v", err)
	}
	defer func() { _ = client.Close() }()
	if _, err := client.Status(); err != nil {
		t.Fatalf("Status() after malformed request error = %v", err)
	}
}

func TestIPC_OversizedRequest_DisconnectsWithoutCrashingServer(t *testing.T) {
	socketPath := startTestServer(t, &fakeClient{})

	raw, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	huge := make([]byte, maxIPCMessageSize*2)
	for i := range huge {
		huge[i] = 'a'
	}
	huge = append(huge, '\n')
	_, _ = raw.Write(huge)
	_ = raw.Close()

	// The server must still be alive for a fresh client afterward.
	client, err := DialIPC(socketPath, time.Second)
	if err != nil {
		t.Fatalf("server appears to have crashed after an oversized request: %v", err)
	}
	defer func() { _ = client.Close() }()
	if _, err := client.Status(); err != nil {
		t.Fatalf("Status() after oversized request error = %v", err)
	}
}

func TestIPC_StaleSocketIsReplaced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets not supported on windows")
	}
	socketPath := filepath.Join(t.TempDir(), "bunker.sock")

	// Bind and then close the listener directly (not via a running
	// Server) -- the file is left behind exactly like a crashed daemon
	// would leave it, with nothing listening anymore.
	l, err := Listen(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Close()

	l2, err := Listen(socketPath)
	if err != nil {
		t.Fatalf("Listen() on a stale socket error = %v, want it to detect staleness and rebind", err)
	}
	_ = l2.Close()
}

func TestIPC_LiveSocketRefusesSecondListener(t *testing.T) {
	socketPath := startTestServer(t, &fakeClient{})

	if _, err := Listen(socketPath); err == nil {
		t.Fatal("Listen() on a live socket error = nil, want it to refuse (a daemon is already running)")
	}
}

func TestIPC_ConcurrentClients(t *testing.T) {
	fc := &fakeClient{}
	socketPath := startTestServer(t, fc)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client, err := DialIPC(socketPath, time.Second)
			if err != nil {
				t.Error(err)
				return
			}
			defer func() { _ = client.Close() }()
			if _, err := client.Status(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}
