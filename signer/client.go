package signer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip46"
)

// URIScheme is the canonical signer URI scheme: bunker+unix:///abs/path.
const URIScheme = "bunker+unix"

// IsURI reports whether s is written as a signer URI (bunker+unix:// or
// unix://), valid or not.
func IsURI(s string) bool {
	return strings.HasPrefix(s, URIScheme+"://") || strings.HasPrefix(s, "unix://")
}

// ParseURI returns the socket path of a bunker+unix:// or unix:// URI. A
// bare absolute path is accepted too.
func ParseURI(s string) (string, error) {
	path := s
	if rest, ok := strings.CutPrefix(s, URIScheme+"://"); ok {
		path = rest
	} else if rest, ok := strings.CutPrefix(s, "unix://"); ok {
		path = rest
	} else if strings.Contains(s, "://") {
		return "", fmt.Errorf("unsupported signer URI %q (want %s:///path/to.sock)", s, URIScheme)
	}
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("signer socket path must be absolute, got %q (want %s:///path/to.sock)", s, URIScheme)
	}
	if strings.ContainsAny(path, "?#") {
		return "", fmt.Errorf("signer URI %q must not carry a query or fragment", s)
	}
	return filepath.Clean(path), nil
}

// RemoteError is an error response from the signer.
type RemoteError struct {
	Msg string
}

func (e *RemoteError) Error() string { return "signer: " + e.Msg }

// Denied reports whether the policy refused the request.
func (e *RemoteError) Denied() bool { return strings.HasPrefix(e.Msg, ErrPrefixDenied) }

// Invalid reports whether the signer rejected the request as malformed.
func (e *RemoteError) Invalid() bool { return strings.HasPrefix(e.Msg, ErrPrefixInvalid) }

// Client talks to a signer over its unix socket. Calls are serialized.
type Client struct {
	mu   sync.Mutex
	conn net.Conn
	sc   *bufio.Scanner
	seq  int
	pub  string
}

// Dial connects to uri and fetches the signer's pubkey.
func Dial(ctx context.Context, uri string) (*Client, error) {
	path, err := ParseURI(uri)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), MaxMessageSize)
	c := &Client{conn: conn, sc: sc}
	pub, err := c.Call(ctx, nip46.MethodGetPublicKey)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	c.pub = pub
	return c, nil
}

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

// PubKey is the signer's pubkey (hex), fetched at Dial.
func (c *Client) PubKey() string { return c.pub }

// Call sends one request and waits for its response.
func (c *Client) Call(ctx context.Context, method string, params ...string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if dl, ok := ctx.Deadline(); ok {
		_ = c.conn.SetDeadline(dl)
	} else {
		_ = c.conn.SetDeadline(time.Time{})
	}
	c.seq++
	id := strconv.Itoa(c.seq)
	if params == nil {
		params = []string{}
	}
	line, err := json.Marshal(nip46.Request{RequestID: id, Method: method, Params: params})
	if err != nil {
		return "", err
	}
	if _, err := c.conn.Write(append(line, '\n')); err != nil {
		return "", err
	}
	if !c.sc.Scan() {
		if err := c.sc.Err(); err != nil {
			return "", err
		}
		return "", errors.New("signer closed the connection")
	}
	var resp nip46.Response
	if err := json.Unmarshal(c.sc.Bytes(), &resp); err != nil {
		return "", fmt.Errorf("malformed signer response: %w", err)
	}
	if resp.RequestID != id {
		return "", fmt.Errorf("signer answered request %q, want %q", resp.RequestID, id)
	}
	if resp.Error != "" {
		return "", &RemoteError{Msg: resp.Error}
	}
	return resp.Result, nil
}

// Sign signs ev in place.
func (c *Client) Sign(ctx context.Context, ev *nip01.Event) error {
	return c.SignWithAttestations(ctx, ev, nil)
}

// SignWithAttestations signs ev in place, passing attestations as
// sign_event's params[1]. The returned event is verified and must match
// what was sent.
func (c *Client) SignWithAttestations(ctx context.Context, ev *nip01.Event, attestations []*nip01.Event) error {
	draft := *ev
	draft.ID, draft.Sig = "", ""
	if draft.Tags == nil {
		draft.Tags = [][]string{}
	}
	evJSON, err := json.Marshal(&draft)
	if err != nil {
		return err
	}
	params := []string{string(evJSON)}
	if len(attestations) > 0 {
		attJSON, err := json.Marshal(attestations)
		if err != nil {
			return err
		}
		params = append(params, string(attJSON))
	}
	result, err := c.Call(ctx, nip46.MethodSignEvent, params...)
	if err != nil {
		return err
	}
	var signed nip01.Event
	if err := json.Unmarshal([]byte(result), &signed); err != nil {
		return fmt.Errorf("malformed signed event: %w", err)
	}
	if err := signed.Verify(); err != nil {
		return fmt.Errorf("signer returned an invalid event: %w", err)
	}
	if signed.Kind != draft.Kind || signed.Content != draft.Content || signed.CreatedAt != draft.CreatedAt ||
		!reflect.DeepEqual(signed.Tags, draft.Tags) || !strings.EqualFold(signed.PubKey, c.pub) {
		return errors.New("signer returned a different event than requested")
	}
	*ev = signed
	return nil
}

// Status calls signer_status.
func (c *Client) Status(ctx context.Context) (*Status, error) {
	result, err := c.Call(ctx, MethodStatus)
	if err != nil {
		return nil, err
	}
	var st Status
	if err := json.Unmarshal([]byte(result), &st); err != nil {
		return nil, fmt.Errorf("malformed status: %w", err)
	}
	return &st, nil
}
