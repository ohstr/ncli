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
}

func NewServer(store *relay.EventStore, searchService search.Service) *Service {

	// Parse timeouts
	sessionConfig := relay.SessionConfig{
		OutgoingBufferSize:      config.OutgoingBufferSize,
		MaxConcurrentStoreTasks: config.MaxConcurrentStoreTasks,
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
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
	}))

	s := &Service{
		store:              store,
		verificationWorker: wsHandler.VerificationWorker,
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
