package climatrix

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const meiliImage = "getmeili/meilisearch:v1.35"

// StartMeili runs Meilisearch in Docker and returns its URL; skips when
// Docker isn't available.
func StartMeili(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	port := allocPort(t)
	name := fmt.Sprintf("climatrix-meili-%d-%d", os.Getpid(), port)
	out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name, "-p", fmt.Sprintf("%d:7700", port),
		"-e", "MEILI_NO_ANALYTICS=true", "-e", "MEILI_ENV=development", meiliImage).CombinedOutput()
	if err != nil {
		t.Skipf("can't start %s: %v\n%s", meiliImage, err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	url := fmt.Sprintf("http://%s:%d", dockerHost(), port)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(url + "/health"); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return url
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("meilisearch at %s never answered (set CLIMATRIX_DOCKER_HOST if docker ports aren't on localhost)", url)
	return ""
}

type indexList struct {
	Indexes []struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
		State   string `json:"state"`
		Entries int64  `json:"entries"`
	} `json:"indexes"`
}

func (l indexList) entries(t *testing.T, name string) int64 {
	t.Helper()
	for _, ix := range l.Indexes {
		if ix.Name == name {
			if !ix.Enabled {
				t.Fatalf("index %s is off", name)
			}
			return ix.Entries
		}
	}
	t.Fatalf("no %s index in %+v", name, l)
	return 0
}

func searchRelay(t *testing.T, host string) *Relay {
	t.Helper()
	return StartRelay(t, "", map[string]any{
		"cache": map[string]any{
			"profiles": map[string]any{
				"search": map[string]any{"enabled": true, "meilisearch": map[string]any{"host": host, "index": "profiles"}},
				"stats":  map[string]any{"enabled": true},
			},
			"zaps": map[string]any{"enabled": true},
		},
	})
}

// rc.35: every index listing panicked with search enabled (nil Meilisearch
// client), so index list, status, dry runs and the dashboard's index panel
// all failed with EOF while the relay was up.
func TestRelaySearchIndexesList(t *testing.T) {
	needsRelay(t)
	r := searchRelay(t, StartMeili(t))
	r.Seed(t)
	e := NewEnv(t)

	var list indexList
	deadline := time.Now().Add(30 * time.Second)
	for {
		list = indexList{}
		e.MustOK(t, "relay", "index", "list", "--config", r.ConfigPath, "--json").JSON(t, &list)
		if list.entries(t, "search") > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if n := list.entries(t, "search"); n <= 0 {
		t.Fatalf("search entries = %d after seeding, want Meilisearch's document count", n)
	}

	var st struct {
		Indexes      []any  `json:"indexes"`
		IndexesError string `json:"indexes_error"`
	}
	e.MustOK(t, "relay", "status", "--config", r.ConfigPath, "--json").JSON(t, &st)
	if len(st.Indexes) == 0 || st.IndexesError != "" {
		t.Errorf("status indexes = %v, error %q", st.Indexes, st.IndexesError)
	}
	e.MustOK(t, "relay", "status", "--config", r.ConfigPath)
	e.MustOK(t, "relay", "index", "rebuild", "search", "--dry-run", "--config", r.ConfigPath, "--json")

	if strings.Contains(r.Log(), "panic") {
		t.Errorf("relay log has a panic:\n%s", r.Log())
	}
}

func TestRelaySearchUnreachable(t *testing.T) {
	needsRelay(t)
	r := searchRelay(t, "http://127.0.0.1:1")
	e := NewEnv(t)

	var list indexList
	e.MustOK(t, "relay", "index", "list", "--config", r.ConfigPath, "--json").JSON(t, &list)
	for _, ix := range list.Indexes {
		if ix.Name == "search" && ix.State != "unreachable" {
			t.Errorf("search state = %q, want unreachable", ix.State)
		}
	}
	if out := e.MustOK(t, "relay", "index", "list", "--config", r.ConfigPath).Stdout; !strings.Contains(out, "unreachable") {
		t.Errorf("index list should say unreachable:\n%s", out)
	}
	if out := e.MustOK(t, "relay", "status", "--config", r.ConfigPath).Stdout; !strings.Contains(out, "unreachable") {
		t.Errorf("status should say unreachable:\n%s", out)
	}
}

func TestRelayCheck(t *testing.T) {
	e := NewEnv(t)
	priv := A(t, "operator").PrivHex
	good := e.WriteFile("good.yaml", "store: "+t.TempDir()+"/notes.db\nnip11:\n  privkey: "+priv+"\n")
	res := e.MustOK(t, "relay", "--config", good, "--check", "--json")
	var ok struct {
		OK     bool   `json:"ok"`
		Config string `json:"config"`
	}
	res.JSON(t, &ok)
	if !ok.OK || ok.Config != good {
		t.Errorf("--check = %s", res.Stdout)
	}

	for name, c := range map[string]struct{ yaml, code string }{
		"unknown scoring section": {"cache:\n  profiles:\n    scoring:\n      nope:\n        x: 1\n", "invalid_input"},
		"stats without privkey":   {"cache:\n  profiles:\n    stats:\n      enabled: true\n", "usage"},
		"zaps without privkey":    {"cache:\n  zaps:\n    enabled: true\n", "usage"},
		"maxTargets over limit":   {"cache:\n  profiles:\n    stats:\n      enabled: true\n      maxTargets: 100000\n", "invalid_input"},
		"unlisted window unit":    {"cache:\n  zaps:\n    windows: [24h, 3x]\n", "invalid_input"},
		"unknown coin":            {"cache:\n  zaps:\n    coins: [btc]\n", "invalid_input"},
		"limit over maxLimit":     {"cache:\n  zaps:\n    limit: 300\n    maxLimit: 100\n", "invalid_input"},
		"relay stats without url": {"cache:\n  relay:\n    stats:\n      enabled: true\n", "usage"},
	} {
		t.Run(name, func(t *testing.T) {
			body := "store: " + t.TempDir() + "/notes.db\nnip11:\n  privkey: " + priv + "\n" + c.yaml
			if c.code == "usage" {
				body = "store: " + t.TempDir() + "/notes.db\nnip11:\n  pubkey: " + A(t, "operator").PubHex + "\n" + c.yaml
			}
			e.Run(t, "relay", "--config", e.WriteFile(strings.ReplaceAll(name, " ", "-")+".yaml", body), "--check", "--json").ExpectErr(t, c.code)
		})
	}

	// Every key from before cache was grouped fails, naming its replacement.
	for old, renamed := range map[string]string{
		"cache:\n  topZapped:\n    enabled: true\n":               "cache.topZapped was renamed to cache.zaps",
		"cache:\n  search:\n    enabled: true\n":                  "cache.search was renamed to cache.profiles.search",
		"cache:\n  profileStats:\n    enabled: true\n":            "cache.profileStats was renamed to cache.profiles.stats",
		"cache:\n  verification:\n    ttl: 7d\n":                  "cache.verification was renamed to cache.profiles.checks",
		"cache:\n  scoring:\n    social: {}\n":                    "cache.scoring was renamed to cache.profiles.scoring",
		"verificationWorkers: 4\n":                                "verificationWorkers was renamed to cache.profiles.checks.workers",
		"cache:\n  profiles:\n    search:\n      index_name: x\n": "cache.profiles.search.index_name was renamed to cache.profiles.search.meilisearch.index",
		"cache:\n  zaps:\n    window: 24h\n":                      "cache.zaps.window was renamed to cache.zaps.windows",
	} {
		body := "store: " + t.TempDir() + "/notes.db\nnip11:\n  privkey: " + priv + "\n" + old
		er := e.Run(t, "relay", "--config", e.WriteFile("renamed.yaml", body), "--check", "--json").ExpectErr(t, "usage")
		if !strings.Contains(er.Error, renamed) {
			t.Errorf("%q: error %q, want %q", old, er.Error, renamed)
		}
	}

	full := e.WriteFile("full-cache.yaml", "store: "+t.TempDir()+"/notes.db\nnip11:\n  privkey: "+priv+"\n  url: wss://relay.example.com\n"+`cache:
  profiles:
    stats: {enabled: true}
    checks: {ttl: 14d}
  zaps: {enabled: true, windows: [24h, 7d, all], coins: [sats, loki], limit: 20, maxLimit: 100, refresh: 1m}
  relay: {stats: {enabled: true, interval: 1m}}
`)
	e.MustOK(t, "relay", "--config", full, "--check", "--json")
}
