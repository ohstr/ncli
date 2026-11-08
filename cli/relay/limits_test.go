package relay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/relay/metrics"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/nipB7"
	relayclient "github.com/ohstr/nmilat/relay/client"
	"github.com/ohstr/nmilat/relay/ratelimit"
	"github.com/ohstr/nmilat/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLimitsConfig_Decode(t *testing.T) {
	cfg := loadRelayConfigFromYAML(t, `
store: test.db
limits:
  events:   {perPubkey: {events: 30, per: 1m, burst: 60}, perIP: {events: 120, per: 1m}}
  reads:    {perIP: {events: 60, per: 1m}}
  http:     {perPubkey: {events: 10, per: 10s}, perIP: {events: 30, per: 1m}}
  blossom:
    uploads:   {perPubkey: {events: 20, per: 1h}}
    downloads: {perIP: {events: 600, per: 1m}}
  connectionsPerIP: 20
  exempt: members
  trustedProxies: [10.0.0.0/8, 127.0.0.1]
`)
	require.NotNil(t, cfg.Limits)
	l := cfg.Limits
	assert.Equal(t, RateConfig{Events: 30, Per: "1m", Burst: 60}, l.Events.PerPubkey)
	assert.Equal(t, RateConfig{Events: 120, Per: "1m"}, l.Events.PerIP)
	assert.Equal(t, RateConfig{Events: 60, Per: "1m"}, l.Reads.PerIP)
	assert.Equal(t, RateConfig{Events: 10, Per: "10s"}, l.HTTP.PerPubkey)
	assert.Equal(t, RateConfig{Events: 20, Per: "1h"}, l.Blossom.Uploads.PerPubkey)
	assert.Equal(t, RateConfig{Events: 600, Per: "1m"}, l.Blossom.Downloads.PerIP)
	assert.Equal(t, 20, l.ConnectionsPerIP)
	assert.Equal(t, "members", l.Exempt)
	assert.Equal(t, []string{"10.0.0.0/8", "127.0.0.1"}, l.TrustedProxies)
	require.NoError(t, checkLimits(&cfg))

	none := loadRelayConfigFromYAML(t, "store: test.db\n")
	assert.Nil(t, none.Limits)
	require.NoError(t, checkLimits(&none))
}

func TestCheckLimits(t *testing.T) {
	for name, c := range map[string]struct {
		yaml, input string
	}{
		"negative events":       {"events: {perPubkey: {events: -1, per: 1m}}", "limits.events.perPubkey.events"},
		"negative burst":        {"reads: {perIP: {events: 1, per: 1m, burst: -2}}", "limits.reads.perIP.burst"},
		"events without per":    {"http: {perIP: {events: 5}}", "limits.http.perIP.per"},
		"per without events":    {"events: {perIP: {per: 1m}}", "limits.events.perIP.events"},
		"burst without events":  {"events: {perIP: {burst: 3}}", "limits.events.perIP.events"},
		"bad duration":          {"blossom: {uploads: {perPubkey: {events: 1, per: 1fortnight}}}", "1fortnight"},
		"duration with no unit": {"blossom: {downloads: {perIP: {events: 1, per: 60}}}", "60"},
		"zero duration":         {"reads: {perPubkey: {events: 1, per: 0s}}", "0s"},
		"negative connections":  {"connectionsPerIP: -1", "limits.connectionsPerIP"},
		"unknown exempt":        {"exempt: admins", "admins"},
		"bad CIDR":              {"trustedProxies: [10.0.0.0/33]", "10.0.0.0/33"},
		"bad proxy IP":          {"trustedProxies: [10.0.0.1, proxy.local]", "proxy.local"},
		// Each passes the field checks, and the limiter can't count it.
		"burst overflows":            {"events: {perIP: {events: 1, per: 1h, burst: 3000000}}", "limits.events.perIP.burst"},
		"burst overflows a long per": {"blossom: {uploads: {perPubkey: {events: 1, per: 2562047h, burst: 3}}}", "limits.blossom.uploads.perPubkey.burst"},
		"under 1ns per event":        {"reads: {perIP: {events: 2000, per: 1us}}", "limits.reads.perIP.events"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := loadRelayConfigFromYAML(t, "store: test.db\nlimits:\n  "+c.yaml+"\n")
			err := checkLimits(&cfg)
			var ce *common.CLIError
			require.True(t, errors.As(err, &ce), "want a CLIError, got %v", err)
			assert.Equal(t, common.CodeInvalidInput, ce.Code)
			assert.Equal(t, c.input, ce.Input)
		})
	}

	for name, yaml := range map[string]string{
		"exempt none":         "exempt: none",
		"only proxies":        "trustedProxies: [10.0.0.0/8, \"::1\", fd00::/8]",
		"zero is unset":       "events: {perIP: {events: 0}}\n  connectionsPerIP: 0",
		"burst below events":  "events: {perIP: {events: 10, per: 1m, burst: 2}}",
		"fractional duration": "reads: {perIP: {events: 3, per: 1.5s}}",
		"1ns per event":       "reads: {perIP: {events: 1000, per: 1us}}",
		"a long per":          "events: {perPubkey: {events: 1, per: 87600h}}",
		"the largest burst":   "events: {perIP: {events: 1, per: 1h, burst: 2562047}}",
	} {
		t.Run(name, func(t *testing.T) {
			cfg := loadRelayConfigFromYAML(t, "store: test.db\nlimits:\n  "+yaml+"\n")
			require.NoError(t, checkLimits(&cfg))
		})
	}
}

func TestRateLimitConfigMapping(t *testing.T) {
	t.Run("no block is no limiter", func(t *testing.T) {
		c := &RelayConfig{}
		_, ok := c.rateLimitConfig(nil, nil)
		assert.False(t, ok)
		l, err := newRateLimiter(c, nil, nil)
		require.NoError(t, err)
		assert.Nil(t, l)
	})

	t.Run("a block that sets no limit is no limiter", func(t *testing.T) {
		c := &RelayConfig{Limits: &LimitsConfig{Exempt: "members", TrustedProxies: []string{"10.0.0.0/8"}}}
		l, err := newRateLimiter(c, func(string) bool { return true }, nil)
		require.NoError(t, err)
		assert.Nil(t, l)
	})

	full := &LimitsConfig{
		Events: LimitConfig{PerPubkey: RateConfig{Events: 30, Per: "1m", Burst: 60}, PerIP: RateConfig{Events: 120, Per: "1m"}},
		Reads:  LimitConfig{PerIP: RateConfig{Events: 60, Per: "30s"}},
		HTTP:   LimitConfig{PerPubkey: RateConfig{Events: 10, Per: "1h"}},
		Blossom: BlossomLimitConfig{
			Uploads:   LimitConfig{PerPubkey: RateConfig{Events: 20, Per: "1h"}, PerIP: RateConfig{Events: 40, Per: "1h"}},
			Downloads: LimitConfig{PerIP: RateConfig{Events: 600, Per: "1m", Burst: 100}},
		},
		ConnectionsPerIP: 20,
		TrustedProxies:   []string{"10.0.0.0/8"},
	}

	t.Run("every limit maps across, durations parsed", func(t *testing.T) {
		got, ok := (&RelayConfig{Limits: full}).rateLimitConfig(func(string) bool { return true }, nil)
		require.True(t, ok)
		assert.Nil(t, got.Exempt, "exempt unset is none")
		assert.Equal(t, ratelimit.Config{
			Event:          ratelimit.Limit{PerPubkey: ratelimit.Rate{Events: 30, Per: time.Minute, Burst: 60}, PerIP: ratelimit.Rate{Events: 120, Per: time.Minute}},
			Read:           ratelimit.Limit{PerIP: ratelimit.Rate{Events: 60, Per: 30 * time.Second}},
			HTTP:           ratelimit.Limit{PerPubkey: ratelimit.Rate{Events: 10, Per: time.Hour}},
			Upload:         ratelimit.Limit{PerPubkey: ratelimit.Rate{Events: 20, Per: time.Hour}, PerIP: ratelimit.Rate{Events: 40, Per: time.Hour}},
			Download:       ratelimit.Limit{PerIP: ratelimit.Rate{Events: 600, Per: time.Minute, Burst: 100}},
			MaxConnsPerIP:  20,
			TrustedProxies: []string{"10.0.0.0/8"},
		}, got)
	})

	t.Run("exempt members uses the member check", func(t *testing.T) {
		l := *full
		l.Exempt = "members"
		got, ok := (&RelayConfig{Limits: &l}).rateLimitConfig(func(pk string) bool { return pk == "member" }, nil)
		require.True(t, ok)
		require.NotNil(t, got.Exempt)
		assert.True(t, got.Exempt("member"))
		assert.False(t, got.Exempt("stranger"))

		l.Exempt = "none"
		got, _ = (&RelayConfig{Limits: &l}).rateLimitConfig(func(string) bool { return true }, nil)
		assert.Nil(t, got.Exempt)
	})

	t.Run("refusals reach the metrics", func(t *testing.T) {
		m := metrics.New("test")
		l, err := newRateLimiter(&RelayConfig{Limits: &LimitsConfig{
			Events:           LimitConfig{PerPubkey: RateConfig{Events: 1, Per: "1h"}},
			ConnectionsPerIP: 1,
		}}, nil, m.RateLimited)
		require.NoError(t, err)
		_, ok := l.Allow(ratelimit.Event, "", "author")
		require.True(t, ok)
		_, ok = l.Allow(ratelimit.Event, "", "author")
		require.False(t, ok)
		_, ok = l.OpenConn("192.0.2.1")
		require.True(t, ok)
		_, ok = l.OpenConn("192.0.2.1")
		require.False(t, ok)

		var out strings.Builder
		for _, mf := range must2(m.Registry.Gather()) {
			if mf.GetName() != "ncli_relay_rate_limited_total" {
				continue
			}
			for _, s := range mf.GetMetric() {
				if v := s.GetCounter().GetValue(); v > 0 {
					fmt.Fprintf(&out, "%s/%s=%v ", s.GetLabel()[0].GetValue(), s.GetLabel()[1].GetValue(), v)
				}
			}
		}
		assert.Equal(t, "connection/ip=1 event/pubkey=1 ", out.String())
	})

	t.Run("connectionsPerIP alone builds a limiter", func(t *testing.T) {
		l, err := newRateLimiter(&RelayConfig{Limits: &LimitsConfig{ConnectionsPerIP: 1}}, nil, nil)
		require.NoError(t, err)
		assert.NotNil(t, l)
	})
}

func TestLimitsWarnings(t *testing.T) {
	public := &RelayConfig{}
	assert.Equal(t, []string{noLimitsWarning}, public.limitsWarnings())

	emptyBlock := &RelayConfig{Limits: &LimitsConfig{Exempt: "members"}}
	assert.Equal(t, []string{noLimitsWarning}, emptyBlock.limitsWarnings(), "a block that limits nothing still warns")

	limited := &RelayConfig{Limits: &LimitsConfig{ConnectionsPerIP: 10}}
	assert.Equal(t, []string{}, limited.limitsWarnings())

	authRequired := &RelayConfig{}
	authRequired.Nip11.Limitation.AuthRequired = true
	assert.Equal(t, []string{}, authRequired.limitsWarnings())

	yes, no := true, false
	perPubkey := LimitConfig{PerPubkey: RateConfig{Events: 5, Per: "1m"}}
	for name, c := range map[string]struct {
		cfg  RelayConfig
		want []string
	}{
		"downloads per pubkey, public downloads": {
			RelayConfig{Blossom: &BlossomConfig{Enabled: true}, Limits: &LimitsConfig{Blossom: BlossomLimitConfig{Downloads: perPubkey}}},
			[]string{downloadsPerPubkeyWarning},
		},
		"downloads per pubkey, members-only downloads": {
			RelayConfig{Blossom: &BlossomConfig{Enabled: true, MembersOnly: BlossomMembersOnly{Download: true}}, Limits: &LimitsConfig{Blossom: BlossomLimitConfig{Downloads: perPubkey}}},
			[]string{},
		},
		"downloads per pubkey, blossom off": {
			RelayConfig{Limits: &LimitsConfig{Blossom: BlossomLimitConfig{Downloads: perPubkey}}},
			[]string{},
		},
		"uploads per pubkey, members-only uploads, members exempt": {
			RelayConfig{Blossom: &BlossomConfig{Enabled: true, MembersOnly: BlossomMembersOnly{Upload: &yes}}, Limits: &LimitsConfig{Exempt: "members", Blossom: BlossomLimitConfig{Uploads: perPubkey}}},
			[]string{uploadsPerPubkeyWarning},
		},
		"uploads per pubkey, members-only by membership_required": {
			RelayConfig{
				Nip11:   nip11.Metadata{Limitation: nip11.Limitation{MembershipRequired: true}},
				Blossom: &BlossomConfig{Enabled: true}, Limits: &LimitsConfig{Exempt: "members", Blossom: BlossomLimitConfig{Uploads: perPubkey}},
			},
			[]string{uploadsPerPubkeyWarning},
		},
		"uploads per pubkey, open uploads": {
			RelayConfig{
				Nip11:   nip11.Metadata{Limitation: nip11.Limitation{MembershipRequired: true}},
				Blossom: &BlossomConfig{Enabled: true, MembersOnly: BlossomMembersOnly{Upload: &no}}, Limits: &LimitsConfig{Exempt: "members", Blossom: BlossomLimitConfig{Uploads: perPubkey}},
			},
			[]string{},
		},
		"uploads per pubkey, nobody exempt": {
			RelayConfig{Blossom: &BlossomConfig{Enabled: true, MembersOnly: BlossomMembersOnly{Upload: &yes}}, Limits: &LimitsConfig{Blossom: BlossomLimitConfig{Uploads: perPubkey}}},
			[]string{},
		},
		"events per pubkey, members-only writes, members exempt": {
			RelayConfig{Nip11: nip11.Metadata{Limitation: nip11.Limitation{MembershipRequired: true}}, Limits: &LimitsConfig{Exempt: "members", Events: perPubkey}},
			[]string{eventsPerPubkeyWarning},
		},
		"events per pubkey, agents write too": {
			RelayConfig{
				Nip11:     nip11.Metadata{Limitation: nip11.Limitation{MembershipRequired: true}},
				AgentAuth: &AgentAuthConfig{Enabled: true}, Limits: &LimitsConfig{Exempt: "members", Events: perPubkey},
			},
			[]string{},
		},
		"events per pubkey, anyone writes": {
			RelayConfig{Limits: &LimitsConfig{Exempt: "members", Events: perPubkey}},
			[]string{},
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, c.want, c.cfg.limitsWarnings())
		})
	}
}

// limitedRelay boots the community relay with limits.
func limitedRelay(t *testing.T, limits *LimitsConfig) *httptest.Server {
	cfg := communityRelayConfig(t)
	cfg.Limits = limits
	return bootScenario(t, cfg)
}

func note(i int) *nip01.Event {
	return &nip01.Event{Kind: 1, CreatedAt: uint64(time.Now().Unix()), Content: fmt.Sprintf("note %d", i)}
}

func TestRateLimits_Relay(t *testing.T) {
	t.Run("events: a burst, then rate-limited", func(t *testing.T) {
		ts := limitedRelay(t, &LimitsConfig{Events: LimitConfig{PerPubkey: RateConfig{Events: 3, Per: "1h"}}})
		conn := connectAs(t, ts, scenarioAgentPriv)
		for i := range 3 {
			ok := publishAs(t, conn, note(i), scenarioAgentPriv)
			require.True(t, ok.Accepted, "event %d: %s", i, ok.Message)
		}
		ok := publishAs(t, conn, note(3), scenarioAgentPriv)
		require.False(t, ok.Accepted)
		require.True(t, strings.HasPrefix(ok.Message, "rate-limited:"), "message %q", ok.Message)

		// Another author has a bucket of its own.
		other := connectAs(t, ts, scenarioMemberPriv)
		require.True(t, publishAs(t, other, note(4), scenarioMemberPriv).Accepted)
	})

	t.Run("events: the author's limit counts only a connection logged in as the author", func(t *testing.T) {
		ts := limitedRelay(t, &LimitsConfig{Events: LimitConfig{PerPubkey: RateConfig{Events: 1, Per: "1h"}}})
		u, err := url.Parse("ws" + strings.TrimPrefix(ts.URL, "http"))
		require.NoError(t, err)
		anon, err := relayclient.Connect(context.Background(), u)
		require.NoError(t, err)
		t.Cleanup(anon.Close)
		for i := range 3 {
			ok := publishAs(t, anon, note(i), scenarioAgentPriv)
			require.True(t, ok.Accepted, "event %d: %s", i, ok.Message)
		}
		// Logged in as the author, its budget applies.
		conn := connectAs(t, ts, scenarioAgentPriv)
		require.True(t, publishAs(t, conn, note(10), scenarioAgentPriv).Accepted)
		require.False(t, publishAs(t, conn, note(11), scenarioAgentPriv).Accepted)
	})

	t.Run("no limits: 300 events accepted", func(t *testing.T) {
		ts := limitedRelay(t, nil)
		conn := connectAs(t, ts, scenarioAgentPriv)
		for i := range 300 {
			ok := publishAs(t, conn, note(i), scenarioAgentPriv)
			require.True(t, ok.Accepted, "event %d: %s", i, ok.Message)
		}
	})

	t.Run("exempt members skip the per-pubkey limit", func(t *testing.T) {
		ts := limitedRelay(t, &LimitsConfig{
			Events: LimitConfig{PerPubkey: RateConfig{Events: 2, Per: "1h"}},
			Exempt: "members",
		})
		enrollMember(t, ts.URL, scenarioMemberPub)

		member := connectAs(t, ts, scenarioMemberPriv)
		for i := range 10 {
			ok := publishAs(t, member, note(i), scenarioMemberPriv)
			require.True(t, ok.Accepted, "member event %d: %s", i, ok.Message)
		}
		stranger := connectAs(t, ts, scenarioAgentPriv)
		for i := range 2 {
			require.True(t, publishAs(t, stranger, note(100+i), scenarioAgentPriv).Accepted)
		}
		ok := publishAs(t, stranger, note(102), scenarioAgentPriv)
		require.False(t, ok.Accepted)
		require.True(t, strings.HasPrefix(ok.Message, "rate-limited:"), "message %q", ok.Message)
	})

	t.Run("POST /query over the http limit is 429", func(t *testing.T) {
		ts := limitedRelay(t, &LimitsConfig{HTTP: LimitConfig{PerIP: RateConfig{Events: 2, Per: "1h"}}})
		body := []byte(`[{"kinds":[1],"limit":1}]`)
		for range 2 {
			require.Equal(t, http.StatusOK, postNIP98(t, ts, "/query", scenarioAgentPriv, body).StatusCode)
		}
		resp := postNIP98(t, ts, "/query", scenarioAgentPriv, body)
		require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
		require.NotEmpty(t, resp.Header.Get("Retry-After"))
		// /count and /events share the http limit.
		require.Equal(t, http.StatusTooManyRequests, postNIP98(t, ts, "/count", scenarioAgentPriv, body).StatusCode)
		ev := note(0)
		require.NoError(t, ev.Sign(scenarioAgentPriv))
		evBody, err := json.Marshal(ev)
		require.NoError(t, err)
		require.Equal(t, http.StatusTooManyRequests, postNIP98(t, ts, "/events", scenarioAgentPriv, evBody).StatusCode)
	})

	t.Run("events: one author budget across the WebSocket and POST /events", func(t *testing.T) {
		ts := limitedRelay(t, &LimitsConfig{Events: LimitConfig{PerPubkey: RateConfig{Events: 2, Per: "1h"}}})
		conn := connectAs(t, ts, scenarioAgentPriv)
		post := func(i int) *http.Response {
			ev := note(i)
			require.NoError(t, ev.Sign(scenarioAgentPriv))
			body, err := json.Marshal(ev)
			require.NoError(t, err)
			return postNIP98(t, ts, "/events", scenarioAgentPriv, body)
		}

		require.True(t, publishAs(t, conn, note(0), scenarioAgentPriv).Accepted)
		require.Equal(t, http.StatusOK, post(1).StatusCode)

		ok := publishAs(t, conn, note(2), scenarioAgentPriv)
		require.False(t, ok.Accepted, "the WebSocket sees the event POSTed")
		require.True(t, strings.HasPrefix(ok.Message, "rate-limited:"), "message %q", ok.Message)
		resp := post(3)
		require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
		require.NotEmpty(t, resp.Header.Get("Retry-After"))
	})

	t.Run("reads: AUTH spends the IP's read budget", func(t *testing.T) {
		ts := limitedRelay(t, &LimitsConfig{Reads: LimitConfig{PerIP: RateConfig{Events: 1, Per: "1h"}}})
		conn := connectAs(t, ts, scenarioAgentPriv) // the AUTH takes the one token
		conn.Outgoing() <- wire.NewRequestPacket("q", nip01.NewSubscriptionFilterGroup(&nip01.SubscriptionFilter{Kinds: []int{1}}))
		deadline := time.After(5 * time.Second)
		for {
			select {
			case res := <-conn.Read():
				switch r := res.(type) {
				case *wire.ClosedSubscriptionResponse:
					require.True(t, strings.HasPrefix(r.Message, "rate-limited:"), "message %q", r.Message)
					return
				case *wire.EOSESubscriptionResponse:
					t.Fatal("REQ served: AUTH did not take a read token")
				}
			case <-deadline:
				t.Fatal("timed out waiting for CLOSED")
			}
		}
	})

	t.Run("trustedProxies: a trusted proxy's X-Forwarded-For names the client", func(t *testing.T) {
		ts := limitedRelay(t, &LimitsConfig{
			HTTP:           LimitConfig{PerIP: RateConfig{Events: 1, Per: "1h"}},
			TrustedProxies: []string{"127.0.0.1"}, // the test server's client is the proxy
		})
		body := []byte(`[{"kinds":[1],"limit":1}]`)
		query := func(headers map[string]string) int {
			return postNIP98With(t, ts, "/query", scenarioAgentPriv, body, headers).StatusCode
		}
		xff := func(v string) map[string]string { return map[string]string{"X-Forwarded-For": v} }

		require.Equal(t, http.StatusOK, query(xff("203.0.113.1")))
		require.Equal(t, http.StatusTooManyRequests, query(xff("203.0.113.1")), "one client, one bucket")
		require.Equal(t, http.StatusOK, query(xff("203.0.113.2")), "another client behind the proxy has its own")
		// The rightmost hop that isn't a trusted proxy is the client.
		require.Equal(t, http.StatusOK, query(xff("198.51.100.7, 127.0.0.1")))
		// X-Real-IP is read only without X-Forwarded-For.
		require.Equal(t, http.StatusTooManyRequests, query(map[string]string{"X-Forwarded-For": "203.0.113.1", "X-Real-IP": "198.51.100.99"}))
		require.Equal(t, http.StatusOK, query(map[string]string{"X-Real-IP": "198.51.100.8"}))
		// A hop that isn't an address falls back to the proxy's own.
		require.Equal(t, http.StatusOK, query(xff("unknown")))
		require.Equal(t, http.StatusTooManyRequests, query(nil), "the proxy's own address, spent by the line above")
	})

	t.Run("trustedProxies unset: X-Forwarded-For is ignored", func(t *testing.T) {
		ts := limitedRelay(t, &LimitsConfig{HTTP: LimitConfig{PerIP: RateConfig{Events: 1, Per: "1h"}}})
		body := []byte(`[{"kinds":[1],"limit":1}]`)
		require.Equal(t, http.StatusOK, postNIP98With(t, ts, "/query", scenarioAgentPriv, body, map[string]string{"X-Forwarded-For": "203.0.113.1"}).StatusCode)
		require.Equal(t, http.StatusTooManyRequests, postNIP98With(t, ts, "/query", scenarioAgentPriv, body, map[string]string{"X-Forwarded-For": "203.0.113.2"}).StatusCode)
	})

	t.Run("blossom: uploads per IP", func(t *testing.T) {
		ts := limitedRelay(t, &LimitsConfig{Blossom: BlossomLimitConfig{Uploads: LimitConfig{PerIP: RateConfig{Events: 1, Per: "1h"}}}})
		enrollMember(t, ts.URL, scenarioMemberPub)
		upload := func(blob []byte) (int, string) {
			sum := sha256.Sum256(blob)
			req, err := http.NewRequest(http.MethodPut, ts.URL+"/upload", bytes.NewReader(blob))
			require.NoError(t, err)
			req.Header.Set("Authorization", blossomToken(t, scenarioMemberPriv, nipB7.VerbUpload, hex.EncodeToString(sum[:])))
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			b, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, string(b) + resp.Header.Get("X-Reason")
		}
		code, msg := upload([]byte("first"))
		require.Equal(t, http.StatusOK, code, msg)
		code, msg = upload([]byte("second"))
		require.Equal(t, http.StatusTooManyRequests, code)
		require.Contains(t, msg, "rate-limited")
	})

	t.Run("blossom: downloads per IP", func(t *testing.T) {
		ts := limitedRelay(t, &LimitsConfig{Blossom: BlossomLimitConfig{Downloads: LimitConfig{PerIP: RateConfig{Events: 2, Per: "1h"}}}})
		sum := sha256.Sum256([]byte("absent"))
		get := func() *http.Response {
			resp, err := http.Get(ts.URL + "/media/" + hex.EncodeToString(sum[:]))
			require.NoError(t, err)
			_ = resp.Body.Close()
			return resp
		}
		for range 2 {
			require.Equal(t, http.StatusNotFound, get().StatusCode)
		}
		resp := get()
		require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
		require.NotEmpty(t, resp.Header.Get("Retry-After"))
	})

	t.Run("connectionsPerIP counts huddle WebSockets", func(t *testing.T) {
		cfg := communityRelayConfig(t)
		cfg.Huddle = &HuddleConfig{Enabled: true, RTC: true}
		cfg.Limits = &LimitsConfig{ConnectionsPerIP: 2}
		ts := bootScenario(t, cfg)
		base := "ws" + strings.TrimPrefix(ts.URL, "http")
		dial := func(path string) (*websocket.Conn, *http.Response, error) {
			c, resp, err := websocket.DefaultDialer.Dial(base+path, nil)
			if c != nil {
				t.Cleanup(func() { _ = c.Close() })
			}
			return c, resp, err
		}

		audio, _, err := dial("/huddle/room-1/audio")
		require.NoError(t, err)
		_, _, err = dial("/huddle/room-1/rtc")
		require.NoError(t, err)

		// Both slots are huddles': the relay's own WebSocket and a third
		// huddle are refused.
		_, resp, err := dial("/")
		require.Error(t, err)
		require.NotNil(t, resp)
		require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
		_, resp, err = dial("/huddle/room-2/audio")
		require.Error(t, err)
		require.NotNil(t, resp)
		require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
		b, _ := io.ReadAll(resp.Body)
		require.Contains(t, string(b), ratelimit.ConnMessage)

		require.NoError(t, audio.Close())
		require.Eventually(t, func() bool {
			_, _, err := dial("/")
			return err == nil
		}, 5*time.Second, 50*time.Millisecond, "a closed huddle frees its slot")
	})

	t.Run("blossom: the upload rate and the default in-flight cap both apply", func(t *testing.T) {
		ts := limitedRelay(t, &LimitsConfig{Blossom: BlossomLimitConfig{Uploads: LimitConfig{PerPubkey: RateConfig{Events: 3, Per: "1h"}}}})
		enrollMember(t, ts.URL, scenarioMemberPub)

		// With Expect: 100-continue the body is sent only once the
		// server reads it, after it took an in-flight slot.
		client := &http.Client{Transport: &http.Transport{ExpectContinueTimeout: time.Minute}}
		put := func(blob []byte, body io.Reader) (*http.Request, string) {
			sum := sha256.Sum256(blob)
			sha := hex.EncodeToString(sum[:])
			req, err := http.NewRequest(http.MethodPut, ts.URL+"/upload", body)
			require.NoError(t, err)
			req.ContentLength = int64(len(blob))
			req.Header.Set("Authorization", blossomToken(t, scenarioMemberPriv, nipB7.VerbUpload, sha))
			return req, sha
		}
		do := func(req *http.Request) (int, string) {
			resp, err := client.Do(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			b, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, string(b) + resp.Header.Get("X-Reason")
		}

		// Two uploads held open: the default maxUploadsPerSigner (2).
		type result struct {
			code int
			msg  string
		}
		var writers []*io.PipeWriter
		var blobs [][]byte
		results := make(chan result, 2)
		for i := range 2 {
			blob := []byte(fmt.Sprintf("held upload %d", i))
			pr, pw := io.Pipe()
			req, _ := put(blob, pr)
			req.Header.Set("Expect", "100-continue")
			go func() {
				code, msg := do(req)
				results <- result{code, msg}
			}()
			_, err := pw.Write(blob[:1]) // returns once the server reads
			require.NoError(t, err)
			writers = append(writers, pw)
			blobs = append(blobs, blob)
		}

		third := []byte("third upload")
		code, msg := do(must(put(third, bytes.NewReader(third))))
		require.Equal(t, http.StatusTooManyRequests, code)
		require.Contains(t, msg, "in flight", "the cap, not the rate, refuses the third")

		for i, pw := range writers {
			_, err := pw.Write(blobs[i][1:])
			require.NoError(t, err)
			require.NoError(t, pw.Close())
		}
		for range 2 {
			r := <-results
			require.Equal(t, http.StatusOK, r.code, r.msg)
		}

		fourth := []byte("fourth upload")
		code, msg = do(must(put(fourth, bytes.NewReader(fourth))))
		require.Equal(t, http.StatusTooManyRequests, code)
		require.Contains(t, msg, "rate-limited")
	})

	t.Run("connectionsPerIP refuses one connection too many", func(t *testing.T) {
		ts := limitedRelay(t, &LimitsConfig{ConnectionsPerIP: 2})
		u, err := url.Parse("ws" + strings.TrimPrefix(ts.URL, "http"))
		require.NoError(t, err)
		var conns []*relayclient.Connection
		for range 2 {
			c, err := relayclient.Connect(context.Background(), u)
			require.NoError(t, err)
			t.Cleanup(c.Close)
			conns = append(conns, c)
		}
		_, err = relayclient.Connect(context.Background(), u)
		require.Error(t, err)

		conns[0].Close()
		require.Eventually(t, func() bool {
			c, err := relayclient.Connect(context.Background(), u)
			if err != nil {
				return false
			}
			t.Cleanup(c.Close)
			return true
		}, 5*time.Second, 50*time.Millisecond, "a closed connection frees its slot")
	})
}

func must(req *http.Request, _ string) *http.Request { return req }

func must2[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// postNIP98With is postNIP98 with extra request headers.
func postNIP98With(t *testing.T, ts *httptest.Server, path, privKey string, body []byte, headers map[string]string) *http.Response {
	t.Helper()
	target := ts.URL + path
	header, err := common.GenerateNIP98Header(privKey, target, http.MethodPost, body)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", header)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}
