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
