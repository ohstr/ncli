package relay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nipB7"
	relayclient "github.com/ohstr/nmilat/relay/client"
	"github.com/ohstr/nmilat/relay/ratelimit"
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
		_, ok := c.rateLimitConfig(nil)
		assert.False(t, ok)
		l, err := newRateLimiter(c, nil)
		require.NoError(t, err)
		assert.Nil(t, l)
	})

	t.Run("a block that sets no limit is no limiter", func(t *testing.T) {
		c := &RelayConfig{Limits: &LimitsConfig{Exempt: "members", TrustedProxies: []string{"10.0.0.0/8"}}}
		l, err := newRateLimiter(c, func(string) bool { return true })
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
		got, ok := (&RelayConfig{Limits: full}).rateLimitConfig(func(string) bool { return true })
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
		got, ok := (&RelayConfig{Limits: &l}).rateLimitConfig(func(pk string) bool { return pk == "member" })
		require.True(t, ok)
		require.NotNil(t, got.Exempt)
		assert.True(t, got.Exempt("member"))
		assert.False(t, got.Exempt("stranger"))

		l.Exempt = "none"
		got, _ = (&RelayConfig{Limits: &l}).rateLimitConfig(func(string) bool { return true })
		assert.Nil(t, got.Exempt)
	})

	t.Run("connectionsPerIP alone builds a limiter", func(t *testing.T) {
		l, err := newRateLimiter(&RelayConfig{Limits: &LimitsConfig{ConnectionsPerIP: 1}}, nil)
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
