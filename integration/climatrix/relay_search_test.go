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
		Name      string `json:"name"`
		Available bool   `json:"available"`
		Estimate  int64  `json:"estimate"`
	} `json:"indexes"`
}

func (l indexList) estimate(t *testing.T, name string) int64 {
	t.Helper()
	for _, ix := range l.Indexes {
		if ix.Name == name {
			if !ix.Available {
				t.Fatalf("index %s unavailable", name)
			}
			return ix.Estimate
		}
	}
	t.Fatalf("no %s index in %+v", name, l)
	return 0
}

func searchRelay(t *testing.T, host string) *Relay {
	t.Helper()
	return StartRelay(t, "", map[string]any{
		"cache": map[string]any{
			"search":       map[string]any{"enabled": true, "host": host, "index_name": "profiles"},
			"topZapped":    map[string]any{"enabled": true},
			"profileStats": map[string]any{"enabled": true},
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
		if list.estimate(t, "search") > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if n := list.estimate(t, "search"); n <= 0 {
		t.Fatalf("search estimate = %d after seeding, want Meilisearch's document count", n)
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
	if n := list.estimate(t, "search"); n != -1 {
		t.Errorf("unreachable search estimate = %d, want -1", n)
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
		"unknown scoring section": {"cache:\n  scoring:\n    nope:\n      x: 1\n", "invalid_input"},
		"stats without privkey":   {"cache:\n  profileStats:\n    enabled: true\n", "usage"},
		"maxTargets over limit":   {"cache:\n  profileStats:\n    enabled: true\n    maxTargets: 100000\n", "invalid_input"},
	} {
		t.Run(name, func(t *testing.T) {
			body := "store: " + t.TempDir() + "/notes.db\nnip11:\n  privkey: " + priv + "\n" + c.yaml
			if c.code == "usage" {
				body = "store: " + t.TempDir() + "/notes.db\nnip11:\n  pubkey: " + A(t, "operator").PubHex + "\n" + c.yaml
			}
			e.Run(t, "relay", "--config", e.WriteFile(strings.ReplaceAll(name, " ", "-")+".yaml", body), "--check", "--json").ExpectErr(t, c.code)
		})
	}
}
