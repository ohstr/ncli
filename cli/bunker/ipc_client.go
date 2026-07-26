package bunker

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// ErrDaemonUnreachable is returned by DialIPC when path has no daemon
// listening on it (missing socket, or one nothing answers on) -- callers
// use this to print "start one with `ncli bunker`" instead of a raw
// connection-refused error.
var ErrDaemonUnreachable = errors.New("bunker: no daemon reachable at the control socket")

// ipcClient is the Unix-socket implementation of BunkerClient -- the
// Linux/macOS attach path (`ncli bunker`/`ncli bunker attach` reconnecting
// to an already-running background daemon). One request in flight at a
// time per connection; callers needing concurrent calls should dial their
// own separate ipcClient.
type ipcClient struct {
	mu      sync.Mutex
	conn    net.Conn
	scanner *bufio.Scanner
}

// DialIPC connects to the control socket at path.
func DialIPC(path string, timeout time.Duration) (BunkerClient, error) {
	conn, err := net.DialTimeout("unix", path, timeout)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDaemonUnreachable, err)
	}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 4096), maxIPCMessageSize)
	return &ipcClient{conn: conn, scanner: sc}, nil
}

func (c *ipcClient) call(req ipcRequest) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	c.conn.SetWriteDeadline(time.Now().Add(ipcIdleTimeout))
	if _, err := c.conn.Write(append(data, '\n')); err != nil {
		return nil, err
	}

	c.conn.SetReadDeadline(time.Now().Add(ipcIdleTimeout))
	if !c.scanner.Scan() {
		if err := c.scanner.Err(); err != nil {
			return nil, err
		}
		return nil, io.ErrUnexpectedEOF
	}

	var resp ipcResponse
	if err := json.Unmarshal(c.scanner.Bytes(), &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, errors.New(resp.Error)
	}
	return resp.Data, nil
}

func decode[T any](data json.RawMessage, err error) (T, error) {
	var v T
	if err != nil {
		return v, err
	}
	if len(data) == 0 {
		return v, nil
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return v, err
	}
	return v, nil
}

func (c *ipcClient) Status() (StatusInfo, error) {
	return decode[StatusInfo](c.call(ipcRequest{Cmd: "status"}))
}

func (c *ipcClient) ListPending() ([]Pending, error) {
	return decode[[]Pending](c.call(ipcRequest{Cmd: "list_pending"}))
}

func (c *ipcClient) Approve(id string, remember *Grant) error {
	_, err := c.call(ipcRequest{Cmd: "approve", ID: id, Remember: remember})
	return err
}

func (c *ipcClient) Reject(id string, remember *Grant) error {
	_, err := c.call(ipcRequest{Cmd: "reject", ID: id, Remember: remember})
	return err
}

func (c *ipcClient) ListSessions() ([]Session, error) {
	return decode[[]Session](c.call(ipcRequest{Cmd: "list_sessions"}))
}

func (c *ipcClient) History() ([]HistoryEntry, error) {
	return decode[[]HistoryEntry](c.call(ipcRequest{Cmd: "history"}))
}

func (c *ipcClient) Revoke(pubkey string) (bool, error) {
	res, err := decode[map[string]bool](c.call(ipcRequest{Cmd: "revoke", Pubkey: pubkey}))
	return res["revoked"], err
}

func (c *ipcClient) RevokeGrant(pubkey, method string, kind *int) (bool, error) {
	res, err := decode[map[string]bool](c.call(ipcRequest{Cmd: "revoke_grant", Pubkey: pubkey, Method: method, Kind: kind}))
	return res["revoked"], err
}

func (c *ipcClient) SetGrant(pubkey string, grant Grant) error {
	_, err := c.call(ipcRequest{Cmd: "set_grant", Pubkey: pubkey, Grant: &grant})
	return err
}

func (c *ipcClient) SetName(pubkey, name string) (bool, error) {
	res, err := decode[map[string]bool](c.call(ipcRequest{Cmd: "set_name", Pubkey: pubkey, Name: name}))
	return res["updated"], err
}

func (c *ipcClient) Connect(uri string, spec *GrantSpec) (string, error) {
	res, err := decode[map[string]string](c.call(ipcRequest{Cmd: "connect", URI: uri, GrantSpec: spec}))
	return res["uri"], err
}

func (c *ipcClient) Logs() (LogSnapshot, error) {
	return decode[LogSnapshot](c.call(ipcRequest{Cmd: "logs"}))
}

func (c *ipcClient) Stop() error {
	_, err := c.call(ipcRequest{Cmd: "stop"})
	return err
}

func (c *ipcClient) Close() error {
	return c.conn.Close()
}
