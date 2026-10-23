package relay

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/relay/jobs"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// withViper sets keys for one test and clears them after.
func withViper(t *testing.T, kv map[string]any) {
	t.Helper()
	for k, v := range kv {
		viper.Set(k, v)
	}
	t.Cleanup(func() {
		for k := range kv {
			viper.Set(k, nil)
		}
	})
}

func adminCmd(t *testing.T, url string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "x"}
	cmd.Flags().String("admin-url", url, "")
	cmd.Flags().Bool("json", true, "")
	cmd.Flags().Bool("quiet", true, "")
	return cmd
}

func TestAdminTarget(t *testing.T) {
	cases := []struct {
		name string
		kv   map[string]any
		flag string
		want string
		code common.ErrorCode
	}{
		{name: "default from port", kv: map[string]any{"port": 7000}, want: "http://127.0.0.1:7001"},
		{name: "no port uses the default", want: "http://127.0.0.1:5501"},
		{name: "explicit listen", kv: map[string]any{"admin.listen": "127.0.0.1:9000"}, want: "http://127.0.0.1:9000"},
		{name: "wildcard dials loopback", kv: map[string]any{"admin.listen": "0.0.0.0:9000"}, want: "http://127.0.0.1:9000"},
		{name: "tls means https", kv: map[string]any{"admin.listen": "127.0.0.1:9000", "admin.tls.cert": "/nonexistent.pem"}, want: "https://127.0.0.1:9000"},
		{name: "flag wins", kv: map[string]any{"admin.listen": "127.0.0.1:9000"}, flag: "https://admin.example.com/", want: "https://admin.example.com"},
		{name: "off", kv: map[string]any{"admin.listen": "off"}, code: common.CodeUsage},
		{name: "malformed", kv: map[string]any{"admin.listen": "nope"}, code: common.CodeInvalidInput},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withViper(t, c.kv)
			got, _, err := adminTarget(adminCmd(t, c.flag))
			if c.code != "" {
				var ce *common.CLIError
				if !errors.As(err, &ce) || ce.Code != c.code {
					t.Fatalf("err = %v, want code %s", err, c.code)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("adminTarget = %q, %v; want %q", got, err, c.want)
			}
		})
	}
}

func TestAPICallMapsStatusToCode(t *testing.T) {
	withViper(t, map[string]any{"nip11.privkey": testPrivKey})
	var status int
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":"nope","job":{"id":"abc","kind":"rebuild"}}`))
	}))
	defer srv.Close()

	cases := map[int]common.ErrorCode{
		http.StatusBadRequest:          common.CodeInvalidInput,
		http.StatusUnauthorized:        common.CodeAuth,
		http.StatusForbidden:           common.CodeAuth,
		http.StatusNotFound:            common.CodeNotFound,
		http.StatusConflict:            common.CodeConflict,
		http.StatusNotImplemented:      common.CodeUsage,
		http.StatusUnprocessableEntity: common.CodeUnsupported,
		http.StatusTooManyRequests:     common.CodeNetwork,
		http.StatusInternalServerError: common.CodeNetwork,
		http.StatusTeapot:              common.CodeInternal,
	}
	for s, want := range cases {
		status = s
		_, err := apiCall(adminCmd(t, srv.URL), http.MethodGet, "/admin/api/jobs", nil, nil)
		var ce *common.CLIError
		if !errors.As(err, &ce) || ce.Code != want {
			t.Errorf("HTTP %d -> %v, want %s", s, err, want)
		}
	}
	status = http.StatusConflict
	_, err := apiCall(adminCmd(t, srv.URL), http.MethodPost, "/admin/api/indexes/zaps/rebuild", map[string]bool{"fresh": true}, nil)
	if err == nil || !strings.Contains(err.Error(), "job abc") {
		t.Errorf("conflict should name the running job: %v", err)
	}
	if !strings.HasPrefix(sawAuth, "Nostr ") {
		t.Errorf("request not NIP-98 signed: %q", sawAuth)
	}

	status = http.StatusOK
	var out map[string]any
	if code, err := apiCall(adminCmd(t, srv.URL), http.MethodGet, "/x", nil, &out); err != nil || code != 200 || out["error"] != "nope" {
		t.Errorf("2xx decode: %d %v %v", code, out, err)
	}
}

func TestAPICallWithoutKeyIsUsage(t *testing.T) {
	withViper(t, map[string]any{"nip11.privkey": ""})
	_, err := apiCall(adminCmd(t, "http://127.0.0.1:1"), http.MethodGet, "/admin/api/jobs", nil, nil)
	var ce *common.CLIError
	if !errors.As(err, &ce) || ce.Code != common.CodeUsage {
		t.Fatalf("err = %v, want usage", err)
	}
}

func TestAPICallUnreachableIsNetwork(t *testing.T) {
	withViper(t, map[string]any{"nip11.privkey": testPrivKey})
	_, err := apiCall(adminCmd(t, "http://127.0.0.1:1"), http.MethodGet, "/admin/api/jobs", nil, nil)
	var ce *common.CLIError
	if !errors.As(err, &ce) || ce.Code != common.CodeNetwork || !strings.Contains(ce.Error(), "admin listener") {
		t.Fatalf("err = %v, want network naming the admin listener", err)
	}
}

func TestFormatters(t *testing.T) {
	for in, want := range map[float64]string{0: "0 B", 512: "512 B", 2048: "2.0 KiB", 9.2 * (1 << 30): "9.2 GiB"} {
		if got := formatBytes(in); got != want {
			t.Errorf("formatBytes(%v) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[time.Duration]string{400 * time.Microsecond: "400µs", 12500 * time.Microsecond: "12.5ms", 2 * time.Second: "2s"} {
		if got := formatDuration(in); got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", in, got, want)
		}
	}
	if got := rate(map[string]float64{}, "x"); got != "-" {
		t.Errorf("missing rate = %q, want -", got)
	}
	if got := slots(0); got != "∞" {
		t.Errorf("unbounded slots = %q", got)
	}
}

// A relay that's up but drops the request (a handler panic on an older
// relay) must not be reported as not running.
func TestAPICallDroppedConnectionIsNotRelayDown(t *testing.T) {
	withViper(t, map[string]any{"nip11.privkey": testPrivKey})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer srv.Close()
	_, err := apiCall(adminCmd(t, srv.URL), http.MethodGet, "/admin/api/indexes", nil, nil)
	if err == nil || strings.Contains(err.Error(), "is the relay running") || !strings.Contains(err.Error(), "closed the connection") {
		t.Fatalf("err = %v, want a dropped-connection message", err)
	}
}

func TestAPICallServerErrorSaysSo(t *testing.T) {
	withViper(t, map[string]any{"nip11.privkey": testPrivKey})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal error: boom"}`))
	}))
	defer srv.Close()
	_, err := apiCall(adminCmd(t, srv.URL), http.MethodGet, "/admin/api/indexes", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want the relay's 500 and its message", err)
	}
}

// One failing index listing used to fail all of relay status.
func TestFetchStatusSurvivesIndexesFailure(t *testing.T) {
	withViper(t, map[string]any{"nip11.privkey": testPrivKey})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/admin/api/indexes" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"internal error: boom"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	st, err := fetchStatus(adminCmd(t, srv.URL))
	if err != nil || st.IndexesError == "" || st.Indexes == nil {
		t.Fatalf("fetchStatus = %+v, %v; want metrics with indexes_error", st, err)
	}
	renderStatus(st)
}

func TestRenderStatusToleratesEmptyMetrics(t *testing.T) {
	// A relay just started has no rates yet; rendering must not panic.
	renderStatus(statusJSON{Indexes: []jobs.IndexInfo{{Status: jobs.Status{Name: "zaps", Enabled: true, State: jobs.StateReady}}}})
}
