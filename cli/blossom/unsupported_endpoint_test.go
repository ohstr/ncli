package blossom

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nipB7"
)

// TestBlossom_UnsupportedEndpoints: a server without BUD-05 media or
// BUD-04 mirror -- a 307 from /media to /media/, or a 404 -- is
// unsupported (exit 8), not a network error an agent retries.
func TestBlossom_UnsupportedEndpoints(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	identity, err := client.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	r := &ncliRunner{t: t, bin: buildTestBinary(t), env: blossomTestEnv(t), nsec: identity.Nsec}

	// What a Go ServeMux with only "/media/" registered does to PUT /media.
	mux := http.NewServeMux()
	mux.HandleFunc("/media/", func(w http.ResponseWriter, _ *http.Request) {
		nipB7.WriteError(w, http.StatusMethodNotAllowed, "")
	})
	mux.HandleFunc("/mirror", func(w http.ResponseWriter, _ *http.Request) {
		nipB7.WriteError(w, http.StatusNotFound, "mirror is not supported on this server")
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	file := filepath.Join(t.TempDir(), "photo.txt")
	if err := os.WriteFile(file, []byte("optimize me"), 0o600); err != nil {
		t.Fatal(err)
	}

	for name, args := range map[string][]string{
		"upload --optimize": {"blossom", "upload", "--optimize", "--identity", r.nsec, "--server", ts.URL, file, "--json"},
		"mirror":            {"blossom", "mirror", "--identity", r.nsec, "--server", ts.URL, "https://example.com/" + strings.Repeat("d", 64), "--json"},
	} {
		t.Run(name, func(t *testing.T) {
			stdout, stderr, code := r.run(args...)
			if code != 8 {
				t.Fatalf("exit %d, want 8 (unsupported)\nstdout: %s\nstderr: %s", code, stdout, stderr)
			}
			var e struct {
				Code      string `json:"code"`
				Retryable bool   `json:"retryable"`
			}
			if err := json.Unmarshal([]byte(stderr), &e); err != nil {
				t.Fatalf("stderr is not one JSON error: %v\n%s", err, stderr)
			}
			if e.Code != "unsupported" || e.Retryable {
				t.Errorf("error = %+v, want unsupported and not retryable", e)
			}
			if strings.Contains(stdout, "may exist there") {
				t.Errorf("report says the blob may exist, but the endpoint is missing: %s", stdout)
			}
		})
	}
}
