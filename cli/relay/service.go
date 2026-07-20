package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/ohstr/ncli/cli/reindex"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/nip26"
	"github.com/ohstr/nmilat/nip98"
	"github.com/ohstr/nmilat/relay"
	"github.com/ohstr/nmilat/search"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
)

type Service struct {
	server *http.Server
	store  *relay.EventStore
}

func NewServer(store *relay.EventStore, searchService search.Service) *Service {

	// Parse timeouts
	sessionConfig := relay.SessionConfig{
		OutgoingBufferSize:      config.OutgoingBufferSize,
		MaxConcurrentStoreTasks: config.MaxConcurrentStoreTasks,
	}

	if _, err := time.ParseDuration(config.HandshakeTimeout); err == nil {
		// Handshake timeout is usually handled by http server config, but session might use it
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

	// Delegation is optional. If present, it MUST be valid.
	if config.Nip11.Delegation != nil {
		if err := nip26.VerifyDelegationToken(
			config.Nip11.Delegation.Issuer,
			config.Nip11.PubKey,
			config.Nip11.Delegation.Conditions,
			config.Nip11.Delegation.Token,
		); err != nil {
			log.Fatal().Err(err).Msg("invalid delegation token or issuer signature")
		}

		sessionConfig.Delegation = &relay.DelegationConfig{
			Issuer:     config.Nip11.Delegation.Issuer,
			Conditions: config.Nip11.Delegation.Conditions,
			Token:      config.Nip11.Delegation.Token,
		}
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
	nip11Handler := nip11.NewHandler(&config.Nip11, supportedNips)

	mux := http.NewServeMux()
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Accept") == nip11.ContentTypeHeader {
			nip11Handler.ServeHTTP(w, r)
		} else {
			wsHandler.ServeHTTP(w, r)
		}
	}))

	// ADMIN ENDPOINTS
	adminAuth := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if err := nip98.VerifyAuthHeader(r, config.Nip11.PubKey); err != nil {
				http.Error(w, err.Error(), http.StatusUnauthorized)
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
			w.WriteHeader(http.StatusConflict) // Or 200, Conflict shows it's busy
			json.NewEncoder(w).Encode(status)
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
		json.NewEncoder(w).Encode(map[string]string{"status": "started"})
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
			json.NewEncoder(w).Encode(status)
			return
		}

		go func() {
			if err := reindex.ExecuteZapReindex(store); err != nil {
				log.Error().Err(err).Msg("zap reindex triggered via /admin/reindex/zaps failed")
			}
		}()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"status": "started"})
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
		json.NewEncoder(w).Encode(stats)
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
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
	}))

	registerMembershipAdminRoutes(mux, wsHandler, store, adminAuth)

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
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
	}))

	s := &Service{
		store: store,
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

func (s *Service) Stop() {

	log.Info().Msg("stopping server gracefully")

	if err := s.server.Shutdown(context.Background()); err != nil {
		log.Fatal().Err(err).Msg("server shutdown failed")
	}

	log.Info().Msg("server stopped")
	log.Info().Msg("stopping events store...")
	s.store.Close()
	log.Info().Msg("events store stopped")
}
