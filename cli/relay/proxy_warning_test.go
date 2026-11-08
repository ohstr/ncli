package relay

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUntrustedProxyWarner(t *testing.T) {
	perIP := RateConfig{Events: 60, Per: "1m"}
	newWarner := func(t *testing.T, l *LimitsConfig) (*untrustedProxyWarner, *[]string) {
		t.Helper()
		w := newUntrustedProxyWarner(&RelayConfig{Limits: l})
		require.NotNil(t, w)
		var got []string
		w.warn = func(msg string) { got = append(got, msg) }
		return w, &got
	}
	request := func(peer string, headers ...string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = peer
		for i := 0; i+1 < len(headers); i += 2 {
			r.Header.Set(headers[i], headers[i+1])
		}
		return r
	}

	t.Run("off without per-IP limits", func(t *testing.T) {
		assert.Nil(t, newUntrustedProxyWarner(&RelayConfig{}))
		assert.Nil(t, newUntrustedProxyWarner(&RelayConfig{Limits: &LimitsConfig{
			Events: LimitConfig{PerPubkey: perIP},
		}}))
		assert.NotNil(t, newUntrustedProxyWarner(&RelayConfig{Limits: &LimitsConfig{ConnectionsPerIP: 4}}))
	})

	t.Run("warns once per untrusted peer", func(t *testing.T) {
		w, got := newWarner(t, &LimitsConfig{Reads: LimitConfig{PerIP: perIP}})
		served := 0
		h := w.wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { served++ }))
		for range 3 {
			h.ServeHTTP(httptest.NewRecorder(), request("10.0.0.5:4000", "X-Forwarded-For", "203.0.113.1"))
		}
		h.ServeHTTP(httptest.NewRecorder(), request("[::ffff:10.0.0.6]:4000", "X-Real-IP", "203.0.113.2"))
		h.ServeHTTP(httptest.NewRecorder(), request("10.0.0.7:4000"))
		assert.Equal(t, 5, served, "the warner never refuses")
		assert.Equal(t, []string{
			"requests carry X-Forwarded-For from 10.0.0.5, which is not in limits.trustedProxies: every client behind it shares one per-IP limit",
			"requests carry X-Real-IP from 10.0.0.6, which is not in limits.trustedProxies: every client behind it shares one per-IP limit",
		}, *got)
	})

	t.Run("a trusted proxy is not warned about", func(t *testing.T) {
		w, got := newWarner(t, &LimitsConfig{HTTP: LimitConfig{PerIP: perIP}, TrustedProxies: []string{"127.0.0.1", "10.0.0.0/8"}})
		w.check(request("127.0.0.1:1", "X-Forwarded-For", "203.0.113.1"))
		w.check(request("10.9.9.9:1", "X-Forwarded-For", "203.0.113.1"))
		assert.Empty(t, *got)
		w.check(request("192.0.2.1:1", "X-Forwarded-For", "203.0.113.1"))
		assert.Len(t, *got, 1)
	})

	t.Run("caps the peers it names", func(t *testing.T) {
		w, got := newWarner(t, &LimitsConfig{ConnectionsPerIP: 4})
		for i := range 3 * maxUntrustedProxyWarnings {
			w.check(request(fmt.Sprintf("192.0.2.%d:1", i+1), "X-Forwarded-For", "203.0.113.1"))
		}
		assert.Len(t, *got, maxUntrustedProxyWarnings)
	})
}
