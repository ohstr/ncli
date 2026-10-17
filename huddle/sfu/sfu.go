// Package sfu serves huddle audio to WebRTC peers, so a browser can sit in
// the same room as a peer on the huddle WebSocket.
//
//	browser --DTLS/SRTP--> sfu --------+
//	                                   |  one room.Room
//	buzz client --WS binary--> wsaudio -+
//
// It is a selective forwarding unit, not a mixer: a speaker's Opus is
// repacketized between RTP and huddle frames by huddle/rtp and never decoded,
// so no codec is linked here either.
//
// # Signalling
//
// JSON objects over the same WebSocket, reusing wsaudio's challenge/auth/joined
// shapes so a client can share that code:
//
//	server -> {"type":"challenge","challenge":"..."}
//	client -> {"type":"auth","event":{...},"protocol_version":3}
//	server -> {"type":"joined","revision":N,"pubkey":"...","peer_index":N,"epoch":N,"peers":[...]}
//	client -> {"type":"offer","sdp":"..."}
//	server -> {"type":"answer","sdp":"..."}
//	server -> {"type":"offer","sdp":"..."}     // renegotiation, when a speaker joins
//	client -> {"type":"answer","sdp":"..."}
//	both   -> {"type":"candidate","candidate":{...}}
//	server -> {"type":"error","code":"...","message":"..."}
//
// The client offers first because it owns the microphone. The server offers only
// to add a newly-heard speaker's track, and the client is expected to answer.
//
// # Control
//
// Once a peer is in, the room's control messages reach it over the same socket,
// behind its own "joined" -- see control.go:
//
//	server -> {"type":"joined",...,"features":["control","media","leave"],
//	           "peers":[{"pubkey":"...","peer_index":N,"epoch":N,
//	                     "media":[{"kind":"camera","state":"off","track_id":"...","stream_id":"...","seq":N}]}]}
//	server -> {"type":"joined",...}       // someone else joined
//	server -> {"type":"left","pubkey":"...","peer_index":N,"epoch":N,"revision":N,"reason":"leave|evicted|dropped"}
//	client -> {"type":"media","kind":"mic|camera|screen","state":"live|off","track_id":"...","stream_id":"..."}
//	server -> {"type":"media","pubkey":"...","peer_index":N,"epoch":N,"kind":"...","state":"...","track_id":"...","stream_id":"...","seq":N}
//	client -> {"type":"leave"}
//
// All of it is additive: a client ignores types and fields it does not know,
// and one that never sends media or leave behaves exactly as before. "features"
// is how a client tells a relay that does this from one that does not.
package sfu

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ohstr/nmilat/huddle/room"
	"github.com/ohstr/nmilat/nip01"
	"github.com/pion/webrtc/v4"
	"github.com/rs/zerolog"
)

// Defaults, matching wsaudio so the two endpoints behave alike.
const (
	DefaultAuthTimeout  = 5 * time.Second
	DefaultWriteTimeout = 10 * time.Second
)

// Error codes, shared with wsaudio where they mean the same thing.
const (
	CodeAuthFailed       = "auth_failed"
	CodeJoinRejected     = "join_rejected"
	CodeRoomFull         = "room_full"
	CodeRoomEnded        = "room_ended"
	CodeUpgradeRequired  = "upgrade_required"
	CodeRoomUnavailable  = "room_unavailable"
	CodeAudioUnavailable = "huddle_audio_unavailable"
	// CodeNegotiationFailed means the WebRTC handshake itself failed, as
	// distinct from being refused admission.
	CodeNegotiationFailed = "negotiation_failed"
)

// PathValueKey is the wildcard name Handler reads the room id from.
const PathValueKey = "id"

// Config configures a Handler.
type Config struct {
	// Enabled must be true to admit anyone.
	Enabled bool

	// RelayURL is the base relay WebSocket URL a client's NIP-42 event must name
	// in its "relay" tag -- the relay's own URL, not this endpoint.
	RelayURL string

	// Rooms holds the live rooms. Required.
	Rooms *room.Manager

	// Authorize, when set, decides whether a pubkey may join a room. This is
	// where relay membership is consulted; the package holds no policy itself.
	Authorize func(ctx context.Context, roomID, pubkey string) error

	// ICEServers are the STUN/TURN servers offered to the client. Without at
	// least a STUN server, only peers on the same network will connect; TURN is
	// what carries the ones behind symmetric NAT.
	ICEServers []webrtc.ICEServer

	// UDPPortMin and UDPPortMax pin the range media is carried on. Zero lets
	// the OS pick from the ephemeral range, which is right on a host with no
	// filtering in the way -- but a relay in a container or behind a firewall
	// needs a known range to publish. Ignored unless both are set.
	UDPPortMin uint16
	UDPPortMax uint16

	// AllowedOrigins restricts browser origins. Empty allows any, for the same
	// reason as wsaudio: admission is gated by a signed challenge, so Origin is
	// not the security boundary and refusing on it only breaks web clients.
	AllowedOrigins []string

	AuthTimeout  time.Duration
	WriteTimeout time.Duration

	Logger zerolog.Logger
}

func (c Config) authTimeout() time.Duration {
	if c.AuthTimeout <= 0 {
		return DefaultAuthTimeout
	}
	return c.AuthTimeout
}

func (c Config) writeTimeout() time.Duration {
	if c.WriteTimeout <= 0 {
		return DefaultWriteTimeout
	}
	return c.WriteTimeout
}

// Handler serves the WebRTC signalling endpoint. Mount it on a path carrying the
// room id as a wildcard, e.g. "/huddle/{id}/rtc".
type Handler struct {
	cfg      Config
	api      *webrtc.API
	upgrader websocket.Upgrader
	// hub is shared by every session this handler serves, which is what lets two
	// browsers in one room see each other's video.
	hub *videoHub
}

// NewHandler returns a Handler for cfg.
func NewHandler(cfg Config) *Handler {
	h := &Handler{cfg: cfg, hub: newVideoHub(), api: newAPI(cfg)}
	h.upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     h.checkOrigin,
	}
	return h
}

// newAPI builds the WebRTC API every session's peer connection comes from.
// Only a SettingEngine is passed: pion then fills in the same default media
// engine and interceptors webrtc.NewPeerConnection would, so nothing about
// codec or header-extension negotiation changes.
func newAPI(cfg Config) *webrtc.API {
	var settings webrtc.SettingEngine
	if cfg.UDPPortMin > 0 && cfg.UDPPortMax > 0 {
		if err := settings.SetEphemeralUDPPortRange(cfg.UDPPortMin, cfg.UDPPortMax); err != nil {
			cfg.Logger.Error().Err(err).
				Uint16("min", cfg.UDPPortMin).Uint16("max", cfg.UDPPortMax).
				Msg("ignoring an unusable huddle UDP port range")
		}
	}
	return webrtc.NewAPI(webrtc.WithSettingEngine(settings))
}

func (h *Handler) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || len(h.cfg.AllowedOrigins) == 0 {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	for _, allowed := range h.cfg.AllowedOrigins {
		if allowed == "*" || strings.EqualFold(allowed, origin) || strings.EqualFold(allowed, parsed.Host) {
			return true
		}
	}
	return false
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue(PathValueKey)
	if roomID == "" {
		http.Error(w, "missing room id", http.StatusBadRequest)
		return
	}
	if h.cfg.Rooms == nil {
		http.Error(w, "huddle audio is not configured", http.StatusServiceUnavailable)
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.cfg.Logger.Debug().Err(err).Msg("huddle rtc upgrade failed")
		return
	}

	s := &session{
		cfg:    h.cfg,
		api:    h.api,
		conn:   conn,
		roomID: roomID,
		hub:    h.hub,
		log:    h.cfg.Logger.With().Str("room", roomID).Str("transport", "rtc").Logger(),
	}
	s.run(r.Context())
}

/////////////////////////////////////////////////////////////////////
// Signalling messages
/////////////////////////////////////////////////////////////////////

type signal struct {
	Type string `json:"type"`

	// challenge / auth
	Challenge       string       `json:"challenge,omitempty"`
	Event           *nip01.Event `json:"event,omitempty"`
	ProtocolVersion *uint8       `json:"protocol_version,omitempty"`

	// joined / left
	Revision  uint64        `json:"revision,omitempty"`
	Pubkey    string        `json:"pubkey,omitempty"`
	PeerIndex uint8         `json:"peer_index,omitempty"`
	Epoch     uint8         `json:"epoch,omitempty"`
	Peers     []peerMessage `json:"peers,omitempty"`
	Features  []string      `json:"features,omitempty"`
	Reason    string        `json:"reason,omitempty"`

	// media
	Kind     string `json:"kind,omitempty"`
	State    string `json:"state,omitempty"`
	TrackID  string `json:"track_id,omitempty"`
	StreamID string `json:"stream_id,omitempty"`
	Seq      uint64 `json:"seq,omitempty"`

	// offer / answer
	SDP string `json:"sdp,omitempty"`

	// candidate
	Candidate *webrtc.ICECandidateInit `json:"candidate,omitempty"`

	// error
	Code           string `json:"code,omitempty"`
	Message        string `json:"message,omitempty"`
	CurrentVersion *uint8 `json:"current_version,omitempty"`
}

type peerMessage struct {
	Pubkey    string `json:"pubkey"`
	PeerIndex uint8  `json:"peer_index"`
	Epoch     uint8  `json:"epoch"`
	// Media is what this peer has said it is publishing, so a peer joining
	// mid-call knows whose camera is off without waiting for the next change.
	// Empty for a WebSocket peer, which publishes only audio and never says.
	Media []mediaState `json:"media,omitempty"`
}

// rosterMessages describes the roster, merging in each peer's media state
// (keyed by mediaKey) where there is any.
func rosterMessages(peers []room.PeerInfo, media map[string][]mediaState) []peerMessage {
	out := make([]peerMessage, 0, len(peers))
	for _, p := range peers {
		out = append(out, peerMessage{Pubkey: p.Pubkey, PeerIndex: p.Index, Epoch: p.Epoch, Media: media[mediaKey(p.Pubkey, p.Index)]})
	}
	return out
}
