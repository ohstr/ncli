package blossom

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nipB7"
)

func TestDirectDownloadURL(t *testing.T) {
	hash := strings.Repeat("c", 64)
	cases := map[string]string{
		"https://relay.example/media/" + hash + ".png": "https://relay.example/media/" + hash + ".png",
		"http://localhost:7777/" + hash:                "http://localhost:7777/" + hash,
		hash:                                           "",
		"blossom:" + hash + ".png":                     "",
		"ftp://relay.example/" + hash:                  "",
		"https://relay.example/media/readme.txt":       "",
	}
	for in, want := range cases {
		if got := directDownloadURL(in); got != want {
			t.Errorf("directDownloadURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// pathServer serves body at exactly path and 404s everything else,
// counting requests -- a host that is no Blossom server and keeps its
// blobs somewhere other than the root.
func pathServer(t *testing.T, path string, body []byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != path {
			nipB7.WriteError(w, http.StatusNotFound, "not here")
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(ts.Close)
	return ts, &hits
}

func sumHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// TestBlossomDownload_FullURLFirst: a full URL is fetched as is, from its
// own path, before any server is asked by hash; when it fails or serves
// the wrong bytes, the servers still get asked (ohstr/ncli#239).
func TestBlossomDownload_FullURLFirst(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	identity, err := client.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	r := &ncliRunner{t: t, bin: buildTestBinary(t), env: blossomTestEnv(t), nsec: identity.Nsec}

	blob := []byte("a blob kept under /media")
	hash := sumHex(blob)

	download := func(args ...string) (map[string]any, string, int) {
		out := filepath.Join(t.TempDir(), "out")
		stdout, stderr, code := r.run(append([]string{"blossom", "download", "-o", out, "--json"}, args...)...)
		if code != 0 {
			return nil, stderr, code
		}
		got, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(blob) {
			t.Errorf("downloaded %q, want %q", got, blob)
		}
		var res map[string]any
		if err := json.Unmarshal([]byte(stdout), &res); err != nil {
			t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
		}
		return res, stderr, code
	}

	t.Run("the URL is fetched as is, no server needed", func(t *testing.T) {
		host, hits := pathServer(t, "/media/"+hash+".txt", blob)
		url := host.URL + "/media/" + hash + ".txt"
		res, stderr, code := download(url)
		if code != 0 {
			t.Fatalf("exit %d\nstderr: %s", code, stderr)
		}
		if res["server"] != url {
			t.Errorf("server = %v, want the URL itself %q", res["server"], url)
		}
		if hits.Load() != 1 {
			t.Errorf("URL host saw %d requests, want 1", hits.Load())
		}
	})

	t.Run("the URL is tried before --server", func(t *testing.T) {
		host, _ := pathServer(t, "/media/"+hash, blob)
		other, otherHits := pathServer(t, "/nothing", nil)
		res, stderr, code := download(host.URL+"/media/"+hash, "--server", other.URL)
		if code != 0 {
			t.Fatalf("exit %d\nstderr: %s", code, stderr)
		}
		if res["server"] != host.URL+"/media/"+hash {
			t.Errorf("server = %v, want the URL", res["server"])
		}
		if otherHits.Load() != 0 {
			t.Errorf("--server was asked %d times though the URL worked", otherHits.Load())
		}
	})

	t.Run("a failing URL falls back to the servers by hash", func(t *testing.T) {
		host, hits := pathServer(t, "/elsewhere", blob)
		srv := newFakeBlossomServer()
		defer srv.Close()
		srv.put(identity.PubKeyHex, blob, "text/plain")
		res, stderr, code := download(host.URL+"/media/"+hash, "--server", srv.URL)
		if code != 0 {
			t.Fatalf("exit %d\nstderr: %s", code, stderr)
		}
		if res["server"] != srv.URL {
			t.Errorf("server = %v, want the fallback %q", res["server"], srv.URL)
		}
		if hits.Load() != 1 {
			t.Errorf("URL host saw %d requests, want 1 (tried first)", hits.Load())
		}
	})

	t.Run("a URL serving other bytes falls back", func(t *testing.T) {
		host, _ := pathServer(t, "/media/"+hash, []byte("not the blob"))
		srv := newFakeBlossomServer()
		defer srv.Close()
		srv.put(identity.PubKeyHex, blob, "text/plain")
		res, stderr, code := download(host.URL+"/media/"+hash, "--server", srv.URL)
		if code != 0 {
			t.Fatalf("exit %d\nstderr: %s", code, stderr)
		}
		if res["server"] != srv.URL {
			t.Errorf("server = %v, want the fallback %q", res["server"], srv.URL)
		}
		if !strings.Contains(stderr, "sha256") {
			t.Errorf("stderr does not say why the URL was skipped: %s", stderr)
		}
	})

	// errReport runs download and returns its one JSON error line.
	errReport := func(t *testing.T, url string) (code string, retryable bool, exit int, stderr string) {
		t.Helper()
		_, stderr, exit = r.run("blossom", "download", url, "-o", filepath.Join(t.TempDir(), "out"), "--json")
		var e struct {
			Code      string `json:"code"`
			Retryable bool   `json:"retryable"`
		}
		for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
			var m map[string]any
			if json.Unmarshal([]byte(line), &m) == nil && m["code"] != nil {
				_ = json.Unmarshal([]byte(line), &e)
			}
		}
		return e.Code, e.Retryable, exit, stderr
	}

	t.Run("a URL serving other bytes with no servers is not_found, not retryable", func(t *testing.T) {
		host, _ := pathServer(t, "/media/"+hash, []byte("not the blob"))
		code, retryable, exit, stderr := errReport(t, host.URL+"/media/"+hash)
		if code != "not_found" || exit != 4 || retryable {
			t.Errorf("code=%q exit=%d retryable=%v, want not_found/4/false\nstderr: %s", code, exit, retryable, stderr)
		}
		if !strings.Contains(stderr, "sha256") {
			t.Errorf("error does not name the mismatch: %s", stderr)
		}
	})

	t.Run("an unreachable URL with no servers is network", func(t *testing.T) {
		host, _ := pathServer(t, "/", nil)
		url := host.URL + "/media/" + hash
		host.Close()
		code, retryable, exit, stderr := errReport(t, url)
		if code != "network" || exit != 6 || !retryable {
			t.Errorf("code=%q exit=%d retryable=%v, want network/6/true\nstderr: %s", code, exit, retryable, stderr)
		}
	})

	t.Run("a missing URL with no servers is not_found", func(t *testing.T) {
		host, _ := pathServer(t, "/elsewhere", blob)
		_, stderr, code := download(host.URL + "/media/" + hash)
		if code != 4 {
			t.Errorf("exit %d, want 4 (not_found)\nstderr: %s", code, stderr)
		}
	})
}
