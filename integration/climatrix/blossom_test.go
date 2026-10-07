package climatrix

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const blossomImage = "ghcr.io/hzrd149/blossom-server:latest"

// dockerHost is where a published container port is reachable: localhost
// normally, the docker gateway (e.g. 172.17.0.1) when this test itself
// runs inside a container on the host's daemon.
func dockerHost() string {
	if h := os.Getenv("CLIMATRIX_DOCKER_HOST"); h != "" {
		return h
	}
	return "localhost"
}

// StartBlossom runs the reference Blossom server in Docker and returns its
// URL; skips when Docker isn't available.
func StartBlossom(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	port := allocPort(t)
	name := fmt.Sprintf("climatrix-blossom-%d-%d", os.Getpid(), port)
	out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name, "-p", fmt.Sprintf("%d:3000", port), blossomImage).CombinedOutput()
	if err != nil {
		t.Skipf("can't start %s: %v\n%s", blossomImage, err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	url := fmt.Sprintf("http://%s:%d", dockerHost(), port)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(url + "/"); err == nil {
			_ = resp.Body.Close()
			return url
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("blossom server at %s never answered (set CLIMATRIX_DOCKER_HOST if docker ports aren't on localhost)", url)
	return ""
}

func TestBlossom(t *testing.T) {
	needsRelay(t)
	srv := StartBlossom(t)
	e := NewEnv(t)
	alice, eve := A(t, "alice"), A(t, "eve")
	file := e.WriteFile("hello.txt", "hello blossom\n")
	sum := sha256.Sum256([]byte("hello blossom\n"))
	hash := hex.EncodeToString(sum[:])

	t.Run("upload", func(t *testing.T) {
		var rep struct {
			Succeeded int
			Results   []map[string]any
		}
		e.MustOK(t, "blossom", "upload", file, "--identity", alice.Nsec, "--server", srv, "--json").JSON(t, &rep)
		if rep.Succeeded != 1 || rep.Results[0]["sha256"] != hash {
			t.Fatalf("upload: %+v", rep)
		}
	})

	t.Run("download round-trips the bytes", func(t *testing.T) {
		out := filepath.Join(e.Dir, "got.txt")
		e.MustOK(t, "blossom", "download", hash, "-o", out, "--server", srv, "--json")
		got, _ := os.ReadFile(out)
		if string(got) != "hello blossom\n" {
			t.Errorf("downloaded %q", got)
		}
		if res := e.MustOK(t, "blossom", "download", hash, "-o", "-", "--server", srv); res.Stdout != "hello blossom\n" {
			t.Errorf("-o - wrote %q", res.Stdout)
		}
	})

	t.Run("missing blob is not_found", func(t *testing.T) {
		e.Run(t, "blossom", "download", "00"+hash[2:], "--server", srv, "--json").ExpectErr(t, "not_found")
		e.Run(t, "blossom", "download", "nothash", "--server", srv, "--json").ExpectErr(t, "invalid_input")
	})

	t.Run("list with BUD-02 off is unsupported", func(t *testing.T) {
		e.Run(t, "blossom", "list", "--identity", alice.Nsec, "--server", srv, "--json").ExpectErr(t, "unsupported")
	})

	t.Run("someone else can't delete your blob", func(t *testing.T) {
		e.Run(t, "blossom", "rm", hash, "--identity", eve.Nsec, "--server", srv, "--yes", "--json").expectErr(t, "auth", true)
		e.MustOK(t, "blossom", "download", hash, "-o", "-", "--server", srv)
	})

	t.Run("report", func(t *testing.T) {
		res := e.Run(t, "blossom", "report", hash, "--type", "spam", "--reason", "test", "--identity", eve.Nsec, "--server", srv, "--json")
		t.Logf("report: exit %d", res.Code)
		e.Run(t, "blossom", "report", hash, "--identity", eve.Nsec, "--server", srv, "--json").ExpectErr(t, "usage")
	})

	t.Run("owner deletes", func(t *testing.T) {
		e.MustOK(t, "blossom", "rm", hash, "--identity", alice.Nsec, "--server", srv, "--yes", "--json")
		e.Run(t, "blossom", "download", hash, "--server", srv, "--json").ExpectErr(t, "not_found")
	})

	t.Run("mirror refused by the server is reported, not internal", func(t *testing.T) {
		// The reference server refuses private source addresses (SSRF
		// guard), which is all a local test has.
		res := e.Run(t, "blossom", "mirror", srv+"/"+hash, "--identity", alice.Nsec, "--server", srv, "--json")
		if res.Code == 0 {
			t.Fatalf("mirror from a private address accepted\n%s", res)
		}
		if res.Code == exitCodes["internal"] {
			t.Errorf("mirror refusal exited internal\n%s", res)
		}
	})

	t.Run("unreachable server is network", func(t *testing.T) {
		dead := fmt.Sprintf("http://127.0.0.1:%d", allocPort(t))
		e.Run(t, "blossom", "upload", file, "--identity", alice.Nsec, "--server", dead, "--json").expectErr(t, "network", true)
	})

	t.Run("servers list", func(t *testing.T) {
		e2 := NewEnv(t)
		var a struct{ Added bool }
		e2.MustOK(t, "blossom", "servers", "add", srv, "--json").JSON(t, &a)
		if !a.Added {
			t.Errorf("add: not added")
		}
		var l struct{ Servers []string }
		e2.MustOK(t, "blossom", "servers", "list", "--json").JSON(t, &l)
		if len(l.Servers) != 1 || l.Servers[0] != srv {
			t.Errorf("list: %v", l.Servers)
		}
		e2.MustOK(t, "blossom", "servers", "remove", srv, "--json")
		e2.Run(t, "blossom", "servers", "add", "not a url", "--json").ExpectErr(t, "invalid_input")
		e2.Run(t, "blossom", "upload", file, "--identity", alice.Nsec, "--json").ExpectErr(t, "not_found")
	})

	t.Run("servers discover via a relay", func(t *testing.T) {
		r := StartRelay(t, "", nil)
		e3 := NewEnv(t)
		e3.MustOK(t, "prefs", "relays", "add", r.URL)
		e3.MustOK(t, "blossom", "servers", "add", srv, "--publish", "--identity", alice.Nsec, "--json")
		var d struct{ Servers []string }
		e3.MustOK(t, "blossom", "servers", "discover", alice.Npub, "--json").JSON(t, &d)
		if len(d.Servers) != 1 || d.Servers[0] != srv {
			t.Errorf("discover: %v", d.Servers)
		}
	})
}
