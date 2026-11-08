package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestHTTPMiddlewareCountsByClass(t *testing.T) {
	m := New("test")
	h := m.HTTP("query", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	for _, p := range []string{"/", "/", "/bad"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", p, nil))
	}
	if got := testutil.ToFloat64(m.httpRequests.WithLabelValues("query", "2xx")); got != 2 {
		t.Errorf("2xx = %v, want 2", got)
	}
	if got := testutil.ToFloat64(m.httpRequests.WithLabelValues("query", "4xx")); got != 1 {
		t.Errorf("4xx = %v, want 1", got)
	}
	if n := testutil.CollectAndCount(m.httpSeconds); n != 1 {
		t.Errorf("latency series = %d, want 1", n)
	}
}

// The admin denial counter must exist per reason before any denial, so
// alerts on it see a zero rather than no data.
func TestAdminDeniedReasonsPreRegistered(t *testing.T) {
	m := New("test")
	if n := testutil.CollectAndCount(m.adminDenied); n != 7 {
		t.Errorf("admin_denied_total series = %d, want 7 (host, cidr, token, nip98, session, csrf, replay)", n)
	}
}

// Every rate-limit series exists from the start: five actions by ip and
// by pubkey, and connection by ip.
func TestRateLimitedPreRegisteredAndCounted(t *testing.T) {
	m := New("test")
	if n := testutil.CollectAndCount(m.rateLimited); n != 11 {
		t.Errorf("rate_limited_total series = %d, want 11", n)
	}
	m.RateLimited("event", "pubkey")
	m.RateLimited("event", "pubkey")
	m.RateLimited("connection", "ip")
	m.RateLimited("future", "ip") // an action this build doesn't know still counts
	for _, c := range []struct {
		action, by string
		want       float64
	}{{"event", "pubkey", 2}, {"event", "ip", 0}, {"connection", "ip", 1}, {"future", "ip", 1}} {
		if got := testutil.ToFloat64(m.rateLimited.WithLabelValues(c.action, c.by)); got != c.want {
			t.Errorf("rate_limited_total{action=%q,by=%q} = %v, want %v", c.action, c.by, got, c.want)
		}
	}
}
