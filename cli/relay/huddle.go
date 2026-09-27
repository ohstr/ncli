package relay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/ohstr/ncli/cli/common"

	"github.com/ohstr/ncli/huddlesfu"
	"github.com/ohstr/nmilat/huddle/room"
	"github.com/ohstr/nmilat/huddle/wsaudio"
	"github.com/ohstr/nmilat/relay"
	"github.com/pion/webrtc/v4"
	"github.com/rs/zerolog/log"
)

// HuddleConfig configures the huddle audio endpoint: real-time voice rooms
// served over their own WebSocket at /huddle/{id}/audio. Off by default.
//
// Audio deliberately does not share the Nostr socket, which decodes every frame
// as JSON and would tear a session down on the first binary frame. When this
// block is absent or disabled the route is not mounted at all, so a client sees
// the same 404 an older relay gives it.
type HuddleConfig struct {
	// Enabled mounts the endpoint. Requires nip11.url, since a joining client
	// authenticates with a NIP-42 event naming this relay and the endpoint has
	// to know what to compare against.
	Enabled bool `mapstructure:"enabled"`

	// MaxRooms caps how many rooms this relay hosts at once. 0 uses the SDK's
	// default. Rooms are created on the first join and dropped when the last
	// peer leaves, so this bounds concurrent use, not total channels.
	//
	// Note there is no maxPeers knob: occupancy per room is a fixed cap in the
	// SDK, because fan-out is quadratic and the limit is a property of what one
	// process can carry rather than an operator preference.
	MaxRooms int `mapstructure:"maxRooms"`

	// RequireMembership restricts joining to relay members (NIP-43). Off means
	// anyone who can authenticate may join any room. Independent of
	// nip11.limitation.membership_required, which gates the Nostr socket:
	// a relay may want open reading and closed calls, or the reverse.
	RequireMembership bool `mapstructure:"requireMembership"`

	// AllowedOrigins restricts browser origins. Empty allows any, which is the
	// right default: admission is gated by a signed NIP-42 challenge, so Origin
	// is not the security boundary, and refusing on it only locks out web
	// clients while every CLI keeps working.
	AllowedOrigins []string `mapstructure:"allowedOrigins"`

	// AuthTimeout bounds how long a connection may sit unauthenticated, as a Go
	// duration string. Empty uses the SDK default.
	AuthTimeout string `mapstructure:"authTimeout"`

	// PingInterval is the heartbeat period, as a Go duration string. A peer that
	// stops answering is dropped rather than left holding a routing identity.
	// Empty uses the SDK default.
	PingInterval string `mapstructure:"pingInterval"`

	// RTC additionally mounts /huddle/{id}/rtc, a WebRTC endpoint, so a browser
	// can join the same rooms. Both endpoints share one set of rooms: a browser
	// and a WebSocket client using the same room id are in the same call.
	RTC bool `mapstructure:"rtc"`

	// ICEServers are the STUN/TURN servers offered to WebRTC clients. Without at
	// least a STUN server only peers on the same network will connect, and TURN
	// is what carries peers behind symmetric NAT. Ignored unless RTC is set.
	ICEServers []ICEServerConfig `mapstructure:"iceServers"`
}

// ICEServerConfig is one STUN or TURN server, mirroring the WebRTC
// RTCIceServer shape.
type ICEServerConfig struct {
	// URLs is one or more stun:/turn:/turns: URLs for the same server.
	URLs []string `mapstructure:"urls"`
	// Username and Credential are TURN's long-term credentials. STUN needs
	// neither.
	Username   string `mapstructure:"username"`
	Credential string `mapstructure:"credential"`
}

// huddleEnabled reports whether the config asks for the endpoint.
func huddleEnabled(cfg *HuddleConfig) bool {
	return cfg != nil && cfg.Enabled
}

// registerHuddleRoutes mounts the huddle audio endpoint and returns the room
// manager so the service can end live calls on shutdown. It returns nil when
// huddles are disabled, and the caller mounts nothing.
//
// The path wildcard is read by the SDK handler via Request.PathValue, the same
// mechanism the membership admin routes use.
func registerHuddleRoutes(mux *http.ServeMux, wsHandler *relay.SessionHandler, cfg *HuddleConfig, relayURL string) *room.Manager {
	if !huddleEnabled(cfg) {
		return nil
	}

	rooms := room.NewManager(cfg.MaxRooms)
	handlerConfig := wsaudio.Config{
		Enabled:        true,
		RelayURL:       relayURL,
		Rooms:          rooms,
		AllowedOrigins: cfg.AllowedOrigins,
		Logger:         log.Logger,
	}

	if d, err := time.ParseDuration(cfg.AuthTimeout); err == nil {
		handlerConfig.AuthTimeout = d
	}
	if d, err := time.ParseDuration(cfg.PingInterval); err == nil {
		handlerConfig.PingInterval = d
	}

	if cfg.RequireMembership {
		// Resolved once here rather than per join: Membership() is the same
		// service for the life of the handler, and IsMember reads a cache built
		// for exactly this kind of hot lookup.
		membership := wsHandler.Membership()
		handlerConfig.Authorize = func(_ context.Context, _, pubkey string) error {
			if membership == nil {
				// Configured to require membership with no membership service
				// to ask: fail closed rather than silently let everyone in.
				return errors.New("membership is required but not configured")
			}
			if !membership.IsMember(pubkey) {
				return errors.New("not a relay member")
			}
			return nil
		}
	}

	mux.Handle("/huddle/{id}/audio", wsaudio.NewHandler(handlerConfig))
	log.Info().
		Bool("requireMembership", cfg.RequireMembership).
		Int("maxRooms", cfg.MaxRooms).
		Msg("huddle audio endpoint mounted at /huddle/{id}/audio")

	if cfg.RTC {
		// The same rooms manager on purpose: a browser and a WebSocket client
		// using one room id must end up in one call, not two.
		mux.Handle("/huddle/{id}/rtc", huddlesfu.NewHandler(huddlesfu.Config{
			Enabled:        true,
			RelayURL:       relayURL,
			Rooms:          rooms,
			Authorize:      handlerConfig.Authorize,
			AllowedOrigins: cfg.AllowedOrigins,
			ICEServers:     iceServers(cfg.ICEServers),
			AuthTimeout:    handlerConfig.AuthTimeout,
			Logger:         log.Logger,
		}))
		log.Info().
			Int("iceServers", len(cfg.ICEServers)).
			Msg("huddle WebRTC endpoint mounted at /huddle/{id}/rtc")
	}

	return rooms
}

// iceServers converts the config shape into pion's.
func iceServers(configured []ICEServerConfig) []webrtc.ICEServer {
	servers := make([]webrtc.ICEServer, 0, len(configured))
	for _, c := range configured {
		if len(c.URLs) == 0 {
			continue
		}
		server := webrtc.ICEServer{URLs: c.URLs}
		if c.Username != "" {
			server.Username = c.Username
			server.Credential = c.Credential
		}
		servers = append(servers, server)
	}
	return servers
}

// endHuddleRooms ends every live call. http.Server.Shutdown never waits on
// hijacked WebSocket connections, so without this a restart drops calls without
// telling anyone -- clients would sit waiting for audio that stopped arriving.
// Ending the rooms asks each peer's writer to close first.
func endHuddleRooms(rooms *room.Manager) {
	if rooms == nil {
		return
	}
	occupancy := rooms.Occupancy()
	for id := range occupancy {
		rooms.End(id)
	}
	if len(occupancy) > 0 {
		log.Info().Int("rooms", len(occupancy)).Msg("ended live huddles on shutdown")
	}
}

// checkHuddleDuration rejects a duration string that cannot be parsed. The
// handler falls back to a default on an unparseable value, which would silently
// ignore a typo in a config file -- so the typo is caught at startup instead.
func checkHuddleDuration(field, value string) error {
	if value == "" {
		return nil
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return &common.CLIError{Err: fmt.Errorf("%s: %w", field, err), Code: common.CodeInvalidInput}
	}
	if d <= 0 {
		return &common.CLIError{Err: fmt.Errorf("%s must be positive", field), Code: common.CodeInvalidInput}
	}
	return nil
}
