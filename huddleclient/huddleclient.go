// Package huddleclient joins a huddle audio room as a client: it performs the
// NIP-42 handshake, tracks the roster, and hands back inbound audio already
// attributed to whoever spoke.
//
// It is the half a TUI or a headless listener needs. It does not decode Opus or
// touch an audio device -- callers get frames and decide what to do with them,
// which is what keeps this package free of a codec and of CGO.
//
// # Why attribution needs the roster
//
// A relayed frame identifies its author by a one-byte routing index, not a
// pubkey, and indices are reused as people leave and rejoin. The pubkey behind an
// index is only knowable from the control plane -- the joined/left messages --
// which is why this client tracks the roster rather than leaving it to callers.
// Protocol v3 adds an epoch alongside the index so a late frame from a departed
// peer is distinguishable from one by whoever took its index.
package huddleclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ohstr/nmilat/huddle/wire"
	"github.com/ohstr/nmilat/nip42"
)

// DefaultHandshakeTimeout bounds the challenge/auth/joined exchange.
const DefaultHandshakeTimeout = 10 * time.Second

// DefaultFrameBuffer is how many inbound frames may queue before the oldest are
// dropped. Audio tolerates loss, not delay, so a slow consumer loses frames
// rather than stalling the reader.
const DefaultFrameBuffer = 32

// Failure modes, for callers that need to distinguish them.
var (
	ErrRefused      = errors.New("huddleclient: the relay refused the join")
	ErrNoPrivateKey = errors.New("huddleclient: a private key is required to sign the auth event")
	ErrClosed       = errors.New("huddleclient: the client is closed")
)

// RefusedError carries the relay's own error code, so a caller can act on
// room_full or upgrade_required rather than parsing prose.
type RefusedError struct {
	Code           string
	Message        string
	CurrentVersion *uint8
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("huddleclient: refused (%s): %s", e.Code, e.Message)
}

func (e *RefusedError) Unwrap() error { return ErrRefused }

// Peer is one participant.
type Peer struct {
	Pubkey string
	Index  uint8
	Epoch  uint8
}

// Frame is one inbound audio frame, attributed. Author is zero-valued when the
// frame arrived from an index this client has no roster entry for, which happens
// briefly if audio outruns the joined message announcing its sender.
type Frame struct {
	Author Peer
	Header wire.FrameHeader
	// Opus is the opaque payload. It aliases the read buffer only until the next
	// read, so a consumer keeping it must copy.
	Opus []byte
	// Attributed reports whether Author is known.
	Attributed bool
}

// Speaking reports whether this frame carries speech above threshold dBov. The
// level is sender-authored and untrusted, so this is for display only -- never
// for a decision that matters.
func (f Frame) Speaking(thresholdDbov int8) bool {
	return !f.Header.IsDTX() && f.Header.LevelDbov > thresholdDbov
}

// DefaultSpeakingThreshold is the level above which a peer is shown as speaking,
// matching the reference implementation's own activity threshold.
const DefaultSpeakingThreshold int8 = -55

// Config configures a Dial.
type Config struct {
	// Endpoint is the huddle audio WebSocket URL, e.g.
	// wss://relay.example/huddle/room-1/audio.
	Endpoint string
	// RelayURL is the relay's own base URL, which the NIP-42 event names. It is
	// not the endpoint: a client authenticates to the relay, then uses that
	// identity here.
	RelayURL string
	// PrivKey signs the auth event. Required.
	PrivKey string
	// ProtocolVersion defaults to wire.CurrentProtocolVersion.
	ProtocolVersion uint8
	// HandshakeTimeout defaults to DefaultHandshakeTimeout.
	HandshakeTimeout time.Duration
	// FrameBuffer defaults to DefaultFrameBuffer.
	FrameBuffer int
}

// Client is a joined huddle session.
type Client struct {
	conn    *websocket.Conn
	version uint8

	self Peer

	mu      sync.RWMutex
	roster  map[uint8]Peer // by routing index
	closed  bool
	joinErr error

	frames chan Frame

	writeMu   sync.Mutex
	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// control is the subset of the control plane this client reads.
type control struct {
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
	Revision  uint64 `json:"revision"`
	Pubkey    string `json:"pubkey"`
	PeerIndex uint8  `json:"peer_index"`
	Epoch     uint8  `json:"epoch"`
	Peers     []struct {
		Pubkey    string `json:"pubkey"`
		PeerIndex uint8  `json:"peer_index"`
		Epoch     uint8  `json:"epoch"`
	} `json:"peers"`
	Code           string `json:"code"`
	Message        string `json:"message"`
	CurrentVersion *uint8 `json:"current_version"`
}

// DialError reports a WebSocket upgrade that never completed, carrying the HTTP
// status the relay answered with.
//
// The status matters because a relay with huddles switched off does not mount
// the endpoint at all -- there is no protocol-level refusal to send, just a 404.
// gorilla flattens that into an opaque "bad handshake", which tells an operator
// nothing, so the status is kept here for a caller to explain.
type DialError struct {
	Endpoint string
	// Status is the HTTP status of the failed upgrade, or 0 if the connection
	// never got far enough to receive one.
	Status int
	Err    error
}

func (e *DialError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("huddleclient: dial %s: HTTP %d: %v", e.Endpoint, e.Status, e.Err)
	}
	return fmt.Sprintf("huddleclient: dial %s: %v", e.Endpoint, e.Err)
}

func (e *DialError) Unwrap() error { return e.Err }

// Dial connects, completes the handshake, and returns a joined client. The
// returned error is a *RefusedError when the relay answered with a code.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.PrivKey == "" {
		return nil, ErrNoPrivateKey
	}
	if cfg.ProtocolVersion == 0 {
		cfg.ProtocolVersion = wire.CurrentProtocolVersion
	}
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = DefaultHandshakeTimeout
	}
	if cfg.FrameBuffer <= 0 {
		cfg.FrameBuffer = DefaultFrameBuffer
	}

	dialer := websocket.Dialer{HandshakeTimeout: cfg.HandshakeTimeout}
	conn, resp, err := dialer.DialContext(ctx, cfg.Endpoint, nil)
	if err != nil {
		dialErr := &DialError{Endpoint: cfg.Endpoint, Err: err}
		if resp != nil {
			dialErr.Status = resp.StatusCode
		}
		return nil, dialErr
	}

	c := &Client{
		conn:    conn,
		version: cfg.ProtocolVersion,
		roster:  make(map[uint8]Peer),
		frames:  make(chan Frame, cfg.FrameBuffer),
		done:    make(chan struct{}),
	}

	if err := c.handshake(cfg); err != nil {
		_ = conn.Close()
		return nil, err
	}

	// No read deadline past the handshake: audio has no fixed cadence and the
	// relay's pings keep the connection alive on their own.
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, err
	}

	c.wg.Add(1)
	go c.readLoop()
	return c, nil
}

func (c *Client) handshake(cfg Config) error {
	if err := c.conn.SetReadDeadline(time.Now().Add(cfg.HandshakeTimeout)); err != nil {
		return err
	}

	var challenge control
	if err := c.readControl(&challenge); err != nil {
		return fmt.Errorf("huddleclient: reading the challenge: %w", err)
	}
	if challenge.Type != "challenge" || challenge.Challenge == "" {
		return fmt.Errorf("huddleclient: expected a challenge, got %q", challenge.Type)
	}

	// The relay tag names the relay, not this endpoint.
	event := nip42.NewAuthEvent(challenge.Challenge, cfg.RelayURL)
	if err := event.Sign(cfg.PrivKey); err != nil {
		return fmt.Errorf("huddleclient: signing the auth event: %w", err)
	}
	if err := c.write(map[string]any{
		"type": "auth", "event": event, "protocol_version": cfg.ProtocolVersion,
	}); err != nil {
		return fmt.Errorf("huddleclient: sending auth: %w", err)
	}

	for {
		var msg control
		if err := c.readControl(&msg); err != nil {
			return fmt.Errorf("huddleclient: waiting for joined: %w", err)
		}
		switch msg.Type {
		case "joined":
			c.self = Peer{Pubkey: msg.Pubkey, Index: msg.PeerIndex, Epoch: msg.Epoch}
			c.applyRoster(msg)
			return nil
		case "error":
			return &RefusedError{Code: msg.Code, Message: msg.Message, CurrentVersion: msg.CurrentVersion}
		}
		// Anything else before joined is ignored rather than fatal.
	}
}

// readControl reads one text frame as JSON, skipping binary frames. Audio can
// legitimately arrive mid-handshake once the relay has admitted us.
func (c *Client) readControl(v any) error {
	for {
		kind, data, err := c.conn.ReadMessage()
		if err != nil {
			return err
		}
		if kind != websocket.TextMessage {
			continue
		}
		return json.Unmarshal(data, v)
	}
}

// Self is this client's own identity in the room.
func (c *Client) Self() Peer { return c.self }

// Roster is the participants currently known, excluding nobody -- the client's
// own entry is included, as the relay reports it.
func (c *Client) Roster() []Peer {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Peer, 0, len(c.roster))
	for _, p := range c.roster {
		out = append(out, p)
	}
	return out
}

// Frames is the inbound audio stream. It is closed when the client closes or the
// relay disconnects, so a consumer can range over it.
func (c *Client) Frames() <-chan Frame { return c.frames }

// Err returns why the session ended, or nil if it ended cleanly.
func (c *Client) Err() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.joinErr
}

// Send publishes one frame of audio. header's level is clamped on encode.
func (c *Client) Send(header wire.FrameHeader, opus []byte) error {
	if len(opus) == 0 {
		return nil
	}
	select {
	case <-c.done:
		return ErrClosed
	default:
	}
	frame := wire.EncodeFrame(header, opus)

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteMessage(websocket.BinaryMessage, frame)
}

func (c *Client) write(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteJSON(v)
}

// Close leaves the room and releases the connection. Idempotent.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
	c.wg.Wait()
	return nil
}

func (c *Client) readLoop() {
	defer c.wg.Done()
	defer close(c.frames)

	for {
		kind, data, err := c.conn.ReadMessage()
		if err != nil {
			c.mu.Lock()
			if !c.closed {
				c.closed = true
				select {
				case <-c.done:
					// Our own Close, so not an error.
				default:
					c.joinErr = err
				}
			}
			c.mu.Unlock()
			return
		}

		switch kind {
		case websocket.BinaryMessage:
			c.deliver(data)
		case websocket.TextMessage:
			var msg control
			if json.Unmarshal(data, &msg) == nil {
				c.applyControl(msg)
			}
		}
	}
}

// deliver parses a relayed frame and hands it to the consumer, attributed.
func (c *Client) deliver(data []byte) {
	index, epoch, clientFrame, ok := wire.ParseRelayFrame(c.version, data)
	if !ok {
		return
	}
	header, opus, parsed := wire.ParseFrame(clientFrame)
	if !parsed && wire.HasHeader(c.version) {
		return
	}
	if !wire.HasHeader(c.version) {
		// v1 carries a bare payload and no header.
		header, opus = wire.FrameHeader{LevelDbov: wire.LevelSilenceFloor}, clientFrame
	}

	frame := Frame{Header: header, Opus: opus}
	c.mu.RLock()
	author, known := c.roster[index]
	c.mu.RUnlock()
	// The epoch must match: an index is reused, so a frame from a departed peer
	// would otherwise be credited to whoever took their place.
	if known && (!wire.HasHeader(c.version) || author.Epoch == epoch || epoch == 0) {
		frame.Author, frame.Attributed = author, true
	}

	select {
	case c.frames <- frame:
	default:
		// Drop rather than block the reader: a stalled consumer must not stall
		// the socket, which would stall the relay's view of this peer.
	}
}

func (c *Client) applyControl(msg control) {
	switch msg.Type {
	case "joined":
		c.applyRoster(msg)
	case "left":
		c.mu.Lock()
		if existing, ok := c.roster[msg.PeerIndex]; ok && existing.Pubkey == msg.Pubkey {
			delete(c.roster, msg.PeerIndex)
		}
		c.mu.Unlock()
	}
}

// applyRoster folds a joined message into the roster: its peers list is
// authoritative, and its own entry covers the joiner announced by it.
func (c *Client) applyRoster(msg control) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range msg.Peers {
		c.roster[p.PeerIndex] = Peer{Pubkey: p.Pubkey, Index: p.PeerIndex, Epoch: p.Epoch}
	}
	if msg.Pubkey != "" {
		c.roster[msg.PeerIndex] = Peer{Pubkey: msg.Pubkey, Index: msg.PeerIndex, Epoch: msg.Epoch}
	}
}
