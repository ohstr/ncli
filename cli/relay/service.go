package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/relay/adminplane"
	"github.com/ohstr/ncli/cli/relay/jobs"
	"github.com/ohstr/ncli/cli/relay/metrics"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/ncli/web/dashboard"
	"github.com/ohstr/nmilat/huddle/room"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/nip86"
	"github.com/ohstr/nmilat/nip98"
	"github.com/ohstr/nmilat/relay"
	"github.com/ohstr/nmilat/search"
	"github.com/rs/zerolog/log"
)

type Service struct {
	server             *http.Server
	store              *relay.EventStore
	verificationWorker *relay.ProfileVerificationWorker
	// huddleRooms is nil unless huddle audio is enabled. Held so Stop can end
	// live calls, which Shutdown cannot do for hijacked WebSocket connections.
	huddleRooms *room.Manager

	metrics *metrics.Metrics
	history *metrics.History
	admin   *adminplane.Server
	jobs    *jobs.Manager
	// stopSampling ends the metrics samplers.
	stopSampling context.CancelFunc
}

// historyInterval and historyWindow size the dashboard's built-in history.
const (
	historyInterval = 2 * time.Second
	historyWindow   = time.Hour
)

func NewServer(store *relay.EventStore, searchService search.Service) *Service {
	return newServer(store, searchService, metrics.New(common.ReadBuildInfo().Version))
}

func newServer(store *relay.EventStore, searchService search.Service, m *metrics.Metrics) *Service {

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
		Logger:   log.Logger,
		Observer: m,
		// Web clients on any site connect to a public relay; access is
		// decided by NIP-42/NIP-43, not by Origin. WithSessionConfig drops
		// the SDK's own "*" default, which left only localhost allowed.
		AllowedOrigins: []string{"*"},
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

	// NIP-86 moderation (bans, kind rules, blocked IPs, NIP-11 overrides):
	// loaded before the session handler so the event policy applies from
	// the first connection.
	moderation, err := loadModeration(moderationPath(config.RelayNotesDb), config.Nip11.PubKey)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load NIP-86 moderation state")
	}
	sessionConfig.EventPolicy = moderation.eventPolicy

	wsHandler := relay.NewSessionHandler(
		store,
		&config.Nip11,
		searchService,
		relay.WithSessionConfig(sessionConfig),
	)

	wsHandler.VerificationWorker.Start(config.VerificationWorkers)

	// groups: roles, role descriptions, extra group admins, moderation log
	// and pending joins. initConfig already validated it.
	applyGroupsConfig(wsHandler.Groups(), &config)

	// NIP-98: the admin endpoints below require HTTP auth, a capability
	// this service adds on top of what the SDK's SessionHandler knows about.
	supportedNips := wsHandler.SupportedNIPs().With(nip11.NIP(98))

	// Assigned once the huddle rooms exist, below: removing a member has to be
	// able to end that member's live calls. The root handler closes over the
	// variable and reads it per request, so the order here does not matter.
	var nip86Handler *nip86.Handler
	nip86Replay := adminplane.NewReplayGuard()
	nip86Metered := m.HTTP("nip86", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		adminplane.ServeOnce(nip86Replay, nip86Handler, w, r)
	}))

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
	// Rebuilt when NIP-86 changes the relay's name, description or icon:
	// nip11.NewHandler serves the bytes it marshaled when built.
	var nip11Handler atomic.Pointer[http.Handler]
	buildNip11 := func() {
		md := nip11Metadata
		moderation.applyNip11(&md)
		h := nip11.NewHandler(&md, supportedNips)
		nip11Handler.Store(&h)
	}
	buildNip11()
	moderation.onNip11Change = buildNip11

	home := newHomepage(dashboard.HomeFS(), func() nip11.Metadata {
		md := nip11Metadata
		moderation.applyNip11(&md)
		return md
	})

	mux := http.NewServeMux()
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		// Four uses share this URL, told apart by headers alone: NIP-86 by
		// its content type, NIP-11 by its Accept, a browser's page load by
		// asking for HTML, and the WebSocket upgrade by everything else.
		switch {
		case nip86Handler != nil && (nip86.IsManagementRequest(r) || r.Method == http.MethodOptions):
			nip86Metered.ServeHTTP(w, r)
		case r.Header.Get("Accept") == nip11.ContentTypeHeader:
			(*nip11Handler.Load()).ServeHTTP(w, r)
		case homepageEnabled() && wantsHomepage(r):
			home.servePage(w, r)
		default:
			wsHandler.ServeHTTP(w, r)
		}
	}))
	if homepageEnabled() {
		mux.HandleFunc("GET /_home/", home.serveAssets)
	}

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
		mux.Handle("/query", m.HTTP("query", relay.NewQueryHandler(store, &config.Nip11.Limitation, wsHandler.Membership(), wsHandler.Groups())))
	}

	if eventsEnabled() {
		// Unwrapped by adminAuth below, same reasoning as /query above: the
		// handler does its own per-request NIP-98 check, not an
		// admin-pubkey allowlist. wsHandler.Membership() for the same
		// reason /query shares it -- one live membership view, not a
		// second cache.
		mux.Handle("/events", m.HTTP("events", relay.NewEventsHandler(store, &config.Nip11.Limitation, wsHandler.Membership(), config.Nip11.Self, relay.WithEventsPolicy(moderation.eventPolicy))))
	}

	// ADMIN ENDPOINTS
	adminReplay := adminplane.NewReplayGuard()
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
			if !adminReplay.First(r.Header.Get("Authorization"), time.Now()) {
				http.Error(w, "NIP-98 event already used", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		}
	}

	// Huddle first: both the membership admin routes and NIP-86 need the rooms
	// manager so removing a member ends that member's live calls.
	huddleRooms := registerHuddleRoutes(mux, wsHandler, config.Huddle, config.Nip11.URL)

	registerMembershipAdminRoutes(mux, wsHandler, store, huddleRooms, adminAuth)

	handler, err := newNip86Handler(wsHandler, store, huddleRooms, moderation)
	if err != nil {
		log.Fatal().Err(err).Msg("invalid nip86 configuration")
	}
	nip86Handler = handler

	jobManager := jobs.New(config.RelayNotesDb+".jobs.json", log.Logger,
		searchIndex(store, searchService, wsHandler.VerificationWorker),
		zapIndex(store),
	)
	jobManager.OnFinish = func(j jobs.Job) { m.JobFinished(j.Index, string(j.State)) }

	sampleCtx, stopSampling := context.WithCancel(context.Background())
	m.Attach(sampleCtx, metrics.Sources{
		Handler:   wsHandler,
		Store:     store,
		StorePath: config.RelayNotesDb,
		Search:    searchService,
		Rooms:     huddleRooms,
		Jobs:      jobManager.Running,
	})
	history := metrics.NewHistory(m, historyInterval, historyWindow)
	go history.Run(sampleCtx)

	s := &Service{
		store:              store,
		verificationWorker: wsHandler.VerificationWorker,
		huddleRooms:        huddleRooms,
		metrics:            m,
		history:            history,
		jobs:               jobManager,
		stopSampling:       stopSampling,
		server: &http.Server{
			Handler: moderation.ipGate(mux),
			Addr:    fmt.Sprintf(":%d", config.Port),
		},
	}

	if config.PprofAddr != "" {
		startPprof(config.PprofAddr)
	}

	go s.serve()

	return s
}

// startAdmin starts the admin listener, if configured. A bind failure is
// returned rather than logged, so the relay refuses to run without it.
func (s *Service) startAdmin() error {
	if adminSettings == nil {
		return nil
	}
	admin, err := adminplane.New(adminplane.Options{
		Settings: adminSettings,
		Admins:   func() []string { return append(nip86Admins(), adminSettings.Pubkeys...) },
		UI:       dashboard.FS(),
		Metrics:  s.metrics,
		History:  s.history,
		Logger:   log.Logger,
	})
	if err != nil {
		return err
	}
	s.jobs.Routes(admin)
	if err := admin.Start(); err != nil {
		if adminSettings.Defaulted && !adminSettings.Dashboard && !adminSettings.Metrics {
			log.Warn().Err(err).Msg("admin listener not started; set admin.listen to choose a free address")
			return nil
		}
		return err
	}
	s.admin = admin
	return nil
}

// searchIndex describes the search index for maintenance jobs, or why it
// can't be maintained on this relay.
func searchIndex(store *relay.EventStore, svc search.Service, worker *relay.ProfileVerificationWorker) *jobs.SearchIndex {
	ix := &jobs.SearchIndex{Store: store, Worker: worker}
	var cfg *SearchConfig
	if config.Cache != nil {
		cfg = config.Cache.Search
	}
	switch {
	case cfg == nil || !cfg.Enabled || svc == nil:
		ix.Disabled = "search is not enabled on this relay (cache.search.enabled)"
	case cfg.Readonly:
		ix.Disabled = "search is read-only on this relay (cache.search.readonly)"
	default:
		ix.Host, ix.Key, ix.IndexName = cfg.Host, cfg.Key, cfg.IndexName
	}
	return ix
}

// zapIndex describes the top-zapped index for maintenance jobs, or why it
// can't be maintained on this relay.
func zapIndex(store *relay.EventStore) *jobs.ZapIndex {
	ix := &jobs.ZapIndex{Store: store}
	if config.Cache == nil || config.Cache.TopZapped == nil || !config.Cache.TopZapped.Enabled {
		ix.Disabled = "top-zapped is not enabled on this relay (cache.topZapped.enabled)"
	}
	return ix
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
	if s.admin != nil {
		if err := s.admin.Shutdown(ctx); err != nil {
			log.Warn().Err(err).Msg("admin listener did not stop cleanly")
		}
	}
	if s.stopSampling != nil {
		s.stopSampling()
	}
	if s.jobs != nil {
		s.jobs.Shutdown(ctx)
	}
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
