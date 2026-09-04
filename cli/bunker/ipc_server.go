package bunker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/ohstr/ncli/cli/common"
)

const (
	// maxIPCMessageSize bounds a single request/response line -- generous
	// for any legitimate command (the largest payload is a Grant or a
	// handful of Pending/Session records), and a hard backstop against an
	// unbounded read from a misbehaving local process. Enforced via
	// bufio.Scanner.Buffer's max-token-size, not by reading first and
	// checking after -- a oversized line just ends the scan (disconnects
	// that client) instead of buffering it all in memory first.
	maxIPCMessageSize = 1 << 20 // 1 MiB
	ipcIdleTimeout    = 5 * time.Minute
)

// SocketPath returns the OS-appropriate path to the daemon's control
// socket, under ncli's shared bunker state directory.
func SocketPath() string {
	return filepath.Join(common.AppConfigDir(), "bunker", "bunker.sock")
}

// ipcRequest/ipcResponse are the newline-delimited JSON control protocol's
// wire shapes.
type ipcRequest struct {
	Cmd    string `json:"cmd"`
	ID     string `json:"id,omitempty"`
	Pubkey string `json:"pubkey,omitempty"`
	Name   string `json:"name,omitempty"`
	URI    string `json:"uri,omitempty"`
	// Method/Kind identify one grant's scope for "revoke_grant" -- kind
	// nil means the any-kind grant, same as Grant.Kind's own convention,
	// not "every kind."
	Method   string `json:"method,omitempty"`
	Kind     *int   `json:"kind,omitempty"`
	Remember *Grant `json:"remember,omitempty"`
	// Grant carries the full grant for "set_grant" -- unlike Remember
	// (attached to an approve/reject decision), this command has no
	// pending request of its own.
	Grant *Grant `json:"grant,omitempty"`
	// GrantSpec carries "connect"'s own --grants file (see command.go),
	// already loaded and validated (LoadGrantSpec) client-side -- kept
	// unresolved across the wire deliberately: GrantSpec.Resolve isn't
	// called until the app this pairing is for actually connects (see
	// its own doc comment on why the timing matters), which can be well
	// after this request for the bunker:// direction.
	GrantSpec *GrantSpec `json:"grant_spec,omitempty"`
}

type ipcResponse struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// Listen creates the control socket at path: 0700 parent directory, 0600
// socket file -- the same posture as vault.yaml, and (matching
// ssh-agent's own model) the accepted security boundary, with no separate
// auth token layer on top. Refuses to bind over a symlink at path. If a
// socket file is already there, this dials it first to check whether a
// daemon is genuinely still listening (more robust than trusting a
// recorded PID, which can be recycled by an unrelated process) before
// concluding it's stale and safe to remove and rebind.
func Listen(path string) (net.Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	// MkdirAll only applies the given mode to directories it actually
	// creates -- an already-existing dir (e.g. a shared parent left over
	// from an earlier, looser-permissioned run) keeps whatever mode it
	// already had. Chmod explicitly so the 0700 invariant holds
	// regardless of whether this call created the directory or found it
	// already there.
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}

	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("refusing to bind over a symlink at %s", path)
		}
		if socketIsLive(path) {
			return nil, fmt.Errorf("a bunker daemon is already running (socket %s is in use)", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("removing stale socket %s: %w", path, err)
		}
	}

	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = l.Close()
		return nil, err
	}
	return l, nil
}

// socketIsLive reports whether path currently has a live listener behind
// it.
func socketIsLive(path string) bool {
	conn, err := net.DialTimeout("unix", path, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// Server serves the control protocol over an already-bound listener,
// dispatching each request against client (a *localClient wrapping the
// live *Daemon, in the daemon process).
type Server struct {
	listener net.Listener
	client   BunkerClient
}

func NewServer(listener net.Listener, client BunkerClient) *Server {
	return &Server{listener: listener, client: client}
}

// Serve accepts connections until ctx is cancelled or the listener errors,
// serving each on its own goroutine. Blocks until the listener stops.
func (s *Server) Serve(ctx context.Context) {
	go func() {
		<-ctx.Done()
		_ = s.listener.Close()
	}()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.serveConn(conn)
	}
}

func (s *Server) serveConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), maxIPCMessageSize)

	for {
		_ = conn.SetReadDeadline(time.Now().Add(ipcIdleTimeout))
		if !scanner.Scan() {
			return
		}

		var req ipcRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			if writeResponse(conn, ipcResponse{Error: "malformed request"}) != nil {
				return
			}
			continue
		}

		resp := s.dispatch(req)
		if writeResponse(conn, resp) != nil {
			return
		}
		if req.Cmd == "stop" {
			return
		}
	}
}

func (s *Server) dispatch(req ipcRequest) ipcResponse {
	switch req.Cmd {
	case "ping":
		return result(map[string]bool{"pong": true}, nil)

	case "status":
		return result(s.client.Status())

	case "list_pending":
		return result(s.client.ListPending())

	case "approve":
		return result(struct{}{}, s.client.Approve(req.ID, req.Remember))

	case "reject":
		return result(struct{}{}, s.client.Reject(req.ID, req.Remember))

	case "list_sessions":
		return result(s.client.ListSessions())

	case "history":
		return result(s.client.History())

	case "revoke":
		revoked, err := s.client.Revoke(req.Pubkey)
		return result(map[string]bool{"revoked": revoked}, err)

	case "revoke_grant":
		revoked, err := s.client.RevokeGrant(req.Pubkey, req.Method, req.Kind)
		return result(map[string]bool{"revoked": revoked}, err)

	case "set_grant":
		if req.Grant == nil {
			return ipcResponse{Error: "set_grant requires a grant"}
		}
		return result(struct{}{}, s.client.SetGrant(req.Pubkey, *req.Grant))

	case "set_name":
		updated, err := s.client.SetName(req.Pubkey, req.Name)
		return result(map[string]bool{"updated": updated}, err)

	case "connect":
		uri, err := s.client.Connect(req.URI, req.GrantSpec)
		return result(map[string]string{"uri": uri}, err)

	case "logs":
		return result(s.client.Logs())

	case "stop":
		return result(struct{}{}, s.client.Stop())

	default:
		return ipcResponse{Error: fmt.Sprintf("unknown command %q", req.Cmd)}
	}
}

// result adapts a (value, error) pair into ipcResponse -- the single
// shape every dispatch case above funnels through, whether v came from a
// BunkerClient call that itself returns (T, error) (passed straight
// through as result(s.client.Status())) or was assembled locally
// alongside a separately-obtained err.
func result(v any, err error) ipcResponse {
	if err != nil {
		return ipcResponse{Error: err.Error()}
	}
	data, mErr := json.Marshal(v)
	if mErr != nil {
		return ipcResponse{Error: "failed to encode response"}
	}
	return ipcResponse{OK: true, Data: data}
}

func writeResponse(conn net.Conn, resp ipcResponse) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	_ = conn.SetWriteDeadline(time.Now().Add(ipcIdleTimeout))
	_, err = conn.Write(append(data, '\n'))
	return err
}
