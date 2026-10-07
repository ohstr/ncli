package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ohstr/ncli/cli/reindex"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/huddle/room"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/nip86"
	"github.com/ohstr/nmilat/nip98"
	"github.com/ohstr/nmilat/relay"
	"github.com/ohstr/nmilat/search"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
)

type Service struct {
	server             *http.Server
	store              *relay.EventStore
	verificationWorker *relay.ProfileVerificationWorker
	// huddleRooms is nil unless huddle audio is enabled. Held so Stop can end
	// live calls, which Shutdown cannot do for hijacked WebSocket connections.
	huddleRooms *room.Manager
}

func NewServer(store *relay.EventStore, searchService search.Service) *Service {

	// Parse timeouts
	sessionConfig := relay.SessionConfig{
		OutgoingBufferSize:      config.OutgoingBufferSize,
		MaxConcurrentStoreTasks: config.MaxConcurrentStoreTasks,
		// Without this, every SDK-internal log.Logger.Warn/Error call
		// (membership/groups cache load failures, the websocket upgrade
		// warning, nmilat#72's missing-nip11.url startup warning) writes
		// to a zero-value zerolog.Logger and is silently dropped -- this
		// service's own log.Info/Warn calls go through the global
		// zerolog/log logger ConfigureLogging just set up above, so
		// route the SDK's through the same sink rather than a second,
		// silent one.
		Logger: log.Logger,
	}

	if d, err := time.ParseDuration(config.PingInterval); err == nil {
		sessionConfig.PingInterval = d
	}
	if d, err := time.ParseDuration(config.PongTimeout); err == nil {
		sessionConfig.PongTimeout = d
	}
	if d, err := time.ParseDuration(config.WriteTimeout); err == nil {
		sessionConfig.DataWriteTimeout = d
		sessionConfig.ControlWriteTimeout = d
	}

	log.Info().Str("pubkey", config.Nip11.PubKey).Int("port", config.Port).Msg("server config check")
	sessionConfig.PrivKey = config.Nip11.PrivKey
	sessionConfig.EnableTopZapped = config.Cache != nil && config.Cache.TopZapped != nil && config.Cache.TopZapped.Enabled

	// WithSessionConfig (below) replaces the SDK's defaultSessionConfig()
	// wholesale rather than merging into it, so its built-in
	// DefaultCacheWindow/DefaultCacheLimit (24h/50) never take effect here
	// unless re-applied explicitly.
	sessionConfig.DefaultCacheWindow = defaultCacheWindow
	sessionConfig.DefaultCacheLimit = defaultTopZappedLimit
	if config.Cache != nil && config.Cache.TopZapped != nil && config.Cache.TopZapped.Window != "" {
		if d, err := client.ParseDuration(config.Cache.TopZapped.Window); err == nil {
			sessionConfig.DefaultCacheWindow = d
		} else {
			log.Warn().Err(err).Str("window", config.Cache.TopZapped.Window).Msg("invalid cache.topZapped.window, using default")
		}
	}

	if config.Membership != nil {
		if d, err := time.ParseDuration(config.Membership.InviteTTL); err == nil {
			sessionConfig.MembershipInviteTTL = d
		}
		sessionConfig.MembershipInviteMaxUses = config.Membership.InviteMaxUses
		sessionConfig.MembershipPublishAddRemove = config.Membership.PublishAddRemoveEvents
	}

	if config.AgentAuth != nil {
		sessionConfig.AgentAuthEnabled = config.AgentAuth.Enabled
		if d, err := time.ParseDuration(config.AgentAuth.FreshnessWindow); err == nil {
			sessionConfig.AgentAuthFreshnessWindow = d
		}
		sessionConfig.AgentKindEnforcement = config.AgentAuth.KindEnforcement
	}

	wsHandler := relay.NewSessionHandler(
		store,
		&config.Nip11,
		searchService,
		relay.WithSessionConfig(sessionConfig),
	)

	wsHandler.VerificationWorker.Start(config.VerificationWorkers)

	// NIP-98: the admin endpoints below require HTTP auth, a capability
	// this service adds on top of what the SDK's SessionHandler knows about.
	supportedNips := wsHandler.SupportedNIPs().With(nip11.NIP(98))

	// Assigned once the huddle rooms exist, below: removing a member has to be
	// able to end that member's live calls. The root handler closes over the
	// variable and reads it per request, so the order here does not matter.
	var nip86Handler *nip86.Handler

	if nip86Enabled() {
		// Advertised so a client can tell the API is there before trying it.
		// SupportedNIPs() is computed from wiring and cannot be configured, so
		// this is the only place it can be added.
		supportedNips = supportedNips.With(nip11.NIP(86))
	}

	if queryEnabled() {
		// "CW" is buzz's own letter code (NIP-CW) for this bridge, not
		// nmilat's unrelated nipcw (NIP-CASH Circle Wallet) package -- see
		// QueryConfig's doc comment.
		supportedNips = supportedNips.With(nip11.NIPLetter("CW"))
	}

	// Built from a copy, not &config.Nip11 directly: nip11.NewHandler
	// marshals its metadata once and serves those bytes for the server's
	// whole lifetime, so this is the only chance to add NIP29 before it's
	// baked in. nmilat's own SessionHandler.ServeHTTP does this same
	// nip29Registered() check for an embedder that calls it directly --
	// but this service's "/" handler (below) answers Accept:nip11 itself
	// rather than delegating to wsHandler.ServeHTTP, because supportedNips
	// here also carries NIP-98/86/CW that wsHandler.SupportedNIPs() alone
	// doesn't know about; reaching the SDK's copy of this check isn't an
	// option without losing those.
	nip11Metadata := config.Nip11
	for _, id := range relay.RegisteredNIPs() {
		if id == nip11.NIP(29) {
			nip11Metadata.NIP29 = &nip11.NIP29Capabilities{Subgroups: true}
			break
		}
	}
	nip11Handler := nip11.NewHandler(&nip11Metadata, supportedNips)

	mux := http.NewServeMux()
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		// Three protocols share this URL, told apart by headers alone: NIP-86
		// by its content type, NIP-11 by its Accept, and the WebSocket upgrade
		// by everything else.
		switch {
		case nip86Handler != nil && (nip86.IsManagementRequest(r) || r.Method == http.MethodOptions):
			nip86Handler.ServeHTTP(w, r)
		case r.Header.Get("Accept") == nip11.ContentTypeHeader:
			nip11Handler.ServeHTTP(w, r)
		default:
			wsHandler.ServeHTTP(w, r)
		}
	}))

	if queryEnabled() {
		// Unwrapped by adminAuth below: nmilat's handler does its own NIP-98
		// check (any validly-signed caller, not just nip86Admins()) --
		// wrapping it here would wrongly turn an any-signer endpoint into an
		// admin-only one. See QueryConfig's doc comment.
		//
		// wsHandler.Membership() is passed, not a second independent
		// MembershipService: a NIP-43 join/leave processed over the
		// WebSocket must be visible to /query immediately, not through a
		// separate cache of the same store that updates on its own schedule.
		mux.Handle("/query", relay.NewQueryHandler(store, &config.Nip11.Limitation, wsHandler.Membership()))
	}

	if eventsEnabled() {
		// Unwrapped by adminAuth below, same reasoning as /query above: the
		// handler does its own per-request NIP-98 check, not an
		// admin-pubkey allowlist. wsHandler.Membership() for the same
		// reason /query shares it -- one live membership view, not a
		// second cache.
		mux.Handle("/events", relay.NewEventsHandler(store, &config.Nip11.Limitation, wsHandler.Membership(), config.Nip11.Self))
	}

	// ADMIN ENDPOINTS
	adminReplay := newReplayGuard()
	adminAuth := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			// Read the body here so the signature can be checked against it,
			// then hand it back to the handler untouched. Without this a
			// captured Authorization header is good for any body at the same
			// URL and method until it expires.
			var body []byte
			if r.Body != nil {
				read, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAdminBodyBytes))
				if err != nil {
					http.Error(w, "request body too large or unreadable", http.StatusRequestEntityTooLarge)
					return
				}
				body = read
				r.Body = io.NopCloser(bytes.NewReader(body))
			}
			// The payload tag is required: without it a captured header is
			// good for any body at this URL and method until it expires.
			if _, err := nip98.Verify(r, nip98.Options{
				AllowedPubkeys: nip86Admins(),
				Body:           body,
				RequirePayload: true,
			}); err != nil {
				http.Error(w, err.Error(), http.StatusUnauthorized)
				return
			}
			if !adminReplay.first(r.Header.Get("Authorization"), time.Now()) {
				http.Error(w, "NIP-98 event already used", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		}
	}

	mux.HandleFunc("/admin/reindex/search", adminAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		status := reindex.SearchState.GetStatus()
		if status["is_running"].(bool) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict) // already reindexing
			_ = json.NewEncoder(w).Encode(status)
			return
		}

		go func() {
			if err := reindex.ExecuteSearchReindex(&reindex.Config{
				RelayNotesDb: viper.GetString("store"),
				Search: struct {
					Host      string `mapstructure:"host"`
					Key       string `mapstructure:"key"`
					IndexName string `mapstructure:"index_name"`
				}{
					Host:      viper.GetString("cache.search.host"),
					Key:       viper.GetString("cache.search.key"),
					IndexName: viper.GetString("cache.search.index_name"),
				},
			}, store); err != nil {
				log.Error().Err(err).Msg("search reindex triggered via /admin/reindex/search failed")
			}
		}()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "started"})
	}))

	mux.HandleFunc("/admin/reindex/zaps", adminAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		status := reindex.ZapsState.GetStatus()
		if status["is_running"].(bool) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(status)
			return
		}

		go func() {
			if err := reindex.ExecuteZapReindex(store); err != nil {
				log.Error().Err(err).Msg("zap reindex triggered via /admin/reindex/zaps failed")
			}
		}()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "started"})
	}))

	mux.HandleFunc("/admin/worker/stats", adminAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		stats := map[string]interface{}{
			"status":              "active",
			"verification_worker": wsHandler.VerificationWorker.GetStats(),
			"search_reindex":      reindex.SearchState.GetStatus(),
			"zaps_reindex":        reindex.ZapsState.GetStatus(),
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(stats)
	}))

	mux.HandleFunc("/admin/search", adminAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		if searchService != nil {
			// search.Service doesn't expose a Delete method, so this only works
			// against the concrete Meilisearch-backed implementation; other
			// search.Service implementations silently no-op here.
			if impl, ok := searchService.(*search.ServiceImpl); ok {
				err := impl.DeleteIndex(context.Background())
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
	}))

	// Huddle first: both the membership admin routes and NIP-86 need the rooms
	// manager so removing a member ends that member's live calls.
	huddleRooms := registerHuddleRoutes(mux, wsHandler, config.Huddle, config.Nip11.URL)

	registerMembershipAdminRoutes(mux, wsHandler, store, huddleRooms, adminAuth)

	handler, err := newNip86Handler(wsHandler, store, huddleRooms)
	if err != nil {
		log.Fatal().Err(err).Msg("invalid nip86 configuration")
	}
	nip86Handler = handler

	mux.HandleFunc("/admin/zaps", adminAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := store.ClearZapIndex(r.Context()); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
	}))

	s := &Service{
		store:              store,
		verificationWorker: wsHandler.VerificationWorker,
		huddleRooms:        huddleRooms,
		server: &http.Server{
			Handler: mux,
			Addr:    fmt.Sprintf(":%d", config.Port),
		},
	}

	go s.serve()

	return s
}

func (s *Service) serve() {
	log.Info().Msg("listening...")

	if err := s.server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal().Err(err).Msg("listening error")
	}
}

// shutdownGracePeriod bounds how long Stop waits for server.Shutdown to
// drain in-flight HTTP requests before moving on regardless -- previously
// this was context.Background() (no deadline at all), so anything that
// kept Shutdown from returning (a slow client, a stuck handler) hung the
// whole process indefinitely with no recourse but an external SIGKILL,
// which skips the store/verification-worker cleanup below entirely. Note
// this does not cover live WebSocket sessions either way: net/http's own
// Shutdown never waits on a hijacked connection, graceful or not.
const shutdownGracePeriod = 10 * time.Second

func (s *Service) Stop() {

	log.Info().Msg("stopping server gracefully")

	// Before Shutdown, not after: it never waits on hijacked WebSocket
	// connections, so a live call would otherwise be severed with no notice and
	// its participants would sit waiting for audio that had simply stopped.
	endHuddleRooms(s.huddleRooms)

	ctx, cancel := context.WithTimeout(context.Background(), shutdownGracePeriod)
	defer cancel()
	if err := s.server.Shutdown(ctx); err != nil {
		// Shutdown itself never force-closes anything on timeout -- per its
		// own doc comment, it just stops waiting and returns ctx's error,
		// leaving any still-active connections as they were. Close() is
		// what actually severs them, so the grace period means something
		// (a bounded wait *and* a real hard stop after it), not just "stop
		// waiting and hope". Logged, not log.Fatal (which os.Exits
		// immediately) -- Fatal-ing here used to skip the verification-
		// worker/store cleanup below entirely on exactly the failure path
		// that most needs it to still run.
		log.Warn().Err(err).Msg("server did not shut down within the grace period, forcing close")
		if closeErr := s.server.Close(); closeErr != nil {
			log.Warn().Err(closeErr).Msg("forced close also failed")
		}
	} else {
		log.Info().Msg("server stopped")
	}

	log.Info().Msg("stopping verification workers...")
	s.verificationWorker.Stop()
	log.Info().Msg("verification workers stopped")

	log.Info().Msg("stopping events store...")
	s.store.Close()
	log.Info().Msg("events store stopped")
}
