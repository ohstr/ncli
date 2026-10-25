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
	"github.com/ohstr/ncli/cli/common/meilisearch"
	"github.com/ohstr/ncli/cli/relay/adminplane"
	"github.com/ohstr/ncli/cli/relay/jobs"
	"github.com/ohstr/ncli/cli/relay/metrics"
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
	// reconciler starts the jobs the indexes need.
	reconciler *jobs.Reconciler
	// stopSampling ends the metrics samplers and the stats publisher.
	stopSampling context.CancelFunc
	// publisherDone closes when the NIP-66 stats publisher has stopped; nil
	// when cache.relay.stats is off.
	publisherDone chan struct{}
}

// The dashboard's built-in history: an hour of 2s samples, then a day of 1m
// rollups.
const (
	historyInterval = 2 * time.Second
	historyWindow   = time.Hour
	rollupStep      = time.Minute
	rollupWindow    = 24 * time.Hour
)

// NewServer serves store; meili is the search engine when search is on
// (nil otherwise).
func NewServer(store *relay.EventStore, meili *meilisearch.MeiliClient) *Service {
	return newServer(store, meili, metrics.New(common.ReadBuildInfo().Version))
}

func newServer(store *relay.EventStore, meili *meilisearch.MeiliClient, m *metrics.Metrics) *Service {
	// Handlers below keep pointers into the config for the server's whole
	// lifetime, including WebSocket sessions that outlive Close; a copy keeps
	// them off the package global so swapping it (tests do) can't race them.
	cfg := config

	if want := cfg.derivedIndexes(); store != nil && !storeHasIndexes(store, want) {
		log.Warn().Msg("the store was opened with other indexes than the cache block needs; open it with openRelayStore")
	}

	// A nil *MeiliClient must not become a non-nil interface.
	var searcher search.Searcher
	if meili != nil {
		searcher = meili
	}

	// nmilat fills zero sizes into the shared SessionConfig on every
	// connection, racing itself; set them here so it never writes.
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
	sessionConfig.EnableProfileStats = config.profileStatsEnabled()
	if sessionConfig.EnableProfileStats {
		sessionConfig.MaxStatsTargets = config.profiles().Stats.MaxTargets
	}
	// initConfig already validated these.
	sessionConfig.Verification, _ = verificationSettings(&config)
	sessionConfig.Zaps, _ = zapRanking(&config)
	applyScoring(&config)

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
		&cfg.Nip11,
		searcher,
		relay.WithSessionConfig(sessionConfig),
	)

	// Profile checks and social scores serve only search and stats.
	if config.searchEnabled() || config.profileStatsEnabled() {
		wsHandler.VerificationWorker.Start(config.VerificationWorkers)
	}

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
		mux.Handle("/query", m.HTTP("query", relay.NewQueryHandler(store, &cfg.Nip11.Limitation, wsHandler.Membership(), wsHandler.Groups())))
	}

	if eventsEnabled() {
		// Unwrapped by adminAuth below, same reasoning as /query above: the
		// handler does its own per-request NIP-98 check, not an
		// admin-pubkey allowlist. wsHandler.Membership() for the same
		// reason /query shares it -- one live membership view, not a
		// second cache.
		mux.Handle("/events", m.HTTP("events", relay.NewEventsHandler(store, &cfg.Nip11.Limitation, wsHandler.Membership(), config.Nip11.Self, relay.WithEventsPolicy(moderation.eventPolicy))))
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

	sampleCtx, stopSampling := context.WithCancel(context.Background())
	searchIx := searchIndex(store, meili)
	if searchIx.Indexer != nil {
		go searchIx.Indexer.Run(sampleCtx)
	}
	jobManager := jobs.New(config.RelayNotesDb+".jobs.json", log.Logger,
		searchIx,
		zapIndex(store),
		statsIndex(store),
	)
	jobManager.OnFinish = func(j jobs.Job) {
		m.JobFinished(j.Index, string(j.Kind), string(j.Trigger), string(j.State))
	}
	reconciler := jobManager.Reconcile(sampleCtx)

	m.Attach(sampleCtx, metrics.Sources{
		Handler:   wsHandler,
		Store:     store,
		StorePath: config.RelayNotesDb,
		Rooms:     huddleRooms,
		Jobs:      jobManager.Running,
		Indexes: func() []metrics.IndexSample {
			var out []metrics.IndexSample
			for _, ix := range jobManager.Indexes(reconciler) {
				out = append(out, metrics.IndexSample{Name: ix.Name, State: ix.State, Entries: ix.Entries, Behind: ix.Behind, Queued: ix.Queued})
			}
			return out
		},
	})
	history := metrics.NewHistory(m, historyInterval, historyWindow).WithRollup(rollupStep, rollupWindow)
	go history.Run(sampleCtx)

	var publisherDone chan struct{}
	if config.relayStatsEnabled() {
		publisher := newRelayStatsPublisher(store, func() (nip11.Metadata, []int, []int) {
			md := nip11Metadata
			moderation.applyNip11(&md)
			md.SupportedNips = supportedNips.Slice()
			allowed, disallowed := moderation.kindRules()
			return md, allowed, disallowed
		})
		publisherDone = make(chan struct{})
		go func() {
			defer close(publisherDone)
			publisher.run(sampleCtx)
		}()
	}

	s := &Service{
		store:              store,
		verificationWorker: wsHandler.VerificationWorker,
		huddleRooms:        huddleRooms,
		metrics:            m,
		history:            history,
		jobs:               jobManager,
		reconciler:         reconciler,
		stopSampling:       stopSampling,
		publisherDone:      publisherDone,
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
		Nip05:    func(pubkey string) string { return verifiedNip05(s.store, pubkey) },
	})
	if err != nil {
		return err
	}
	s.jobs.Routes(admin, s.reconciler)
	profileRoutes(admin, s.store, s.verificationWorker)
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

// searchIndex is the search index and, while search is on and writable,
// the indexer that keeps it in step with the store. A disabled one names
// its Meilisearch index only when search is off, so dropping it never
// touches an index another relay writes.
func searchIndex(store *relay.EventStore, meili *meilisearch.MeiliClient) *jobs.SearchIndex {
	ix := &jobs.SearchIndex{Store: store}
	cfg := config.searchConfig()
	switch {
	case cfg == nil || !cfg.Enabled || meili == nil:
		ix.Disabled = "search is not enabled on this relay (cache.profiles.search.enabled)"
		if cfg != nil && cfg.Meilisearch.Host != "" {
			ix.Host, ix.Key, ix.IndexName = cfg.Meilisearch.Host, cfg.Meilisearch.Key, cfg.Meilisearch.Index
		}
	case cfg.Readonly:
		ix.Disabled = "search is read-only on this relay (cache.profiles.search.readonly)"
	default:
		ix.Host, ix.Key, ix.IndexName = cfg.Meilisearch.Host, cfg.Meilisearch.Key, cfg.Meilisearch.Index
		ix.Client, ix.BatchSize, ix.MaxInFlight = meili, cfg.BatchSize, cfg.MaxInFlight
		indexer, err := relay.NewSearchIndexer(store, meili, relay.WithSearchBatch(cfg.BatchSize), relay.WithSearchInFlight(cfg.MaxInFlight))
		if err != nil {
			log.Error().Err(err).Msg("failed to start search indexing")
			ix.Disabled = "search indexing failed to start: " + err.Error()
			return ix
		}
		ix.Indexer = indexer
	}
	return ix
}

// storeHasIndexes reports whether store keeps exactly the indexes want.
func storeHasIndexes(store *relay.EventStore, want relay.DerivedIndexes) bool {
	z, zerr := store.IndexStatus(relay.IndexZaps)
	s, serr := store.IndexStatus(relay.IndexSocial)
	return zerr == nil && serr == nil && z.Enabled == want.Zaps && s.Enabled == want.Social
}

// zapIndex is the zap ranking index.
func zapIndex(store *relay.EventStore) *jobs.StoreIndex {
	why := ""
	if !config.zapsEnabled() {
		why = "zap ranking is not enabled on this relay (cache.zaps.enabled)"
	}
	return jobs.NewZapIndex(store, why)
}

// statsIndex is the follows and zaps index behind profile scores and
// NIP-85 stats, in use only when search or stats is on.
func statsIndex(store *relay.EventStore) *jobs.StoreIndex {
	why := ""
	if !config.statsInUse() {
		why = "stats are not enabled on this relay (cache.profiles.search.enabled or cache.profiles.stats.enabled)"
	}
	return jobs.NewStatsIndex(store, why)
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
	if s.publisherDone != nil {
		select {
		case <-s.publisherDone:
		case <-ctx.Done():
		}
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
