// Package climatrix is the black-box command matrix: it builds the real
// ncli binary, starts local `ncli relay` processes on demand, and drives
// every command against them. No public relay is ever dialed. See
// README.md.
package climatrix

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

// Ports 21600-21639: below the 32768 ephemeral floor, clear of the
// 21500-21590 compose stacks (see integration/README.md).
const (
	portFirst = 21600
	portLast  = 21639
)

var (
	binOnce  sync.Once
	binPath  string
	binErr   error
	nextPort atomic.Int32
)

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// bin builds ./cmd/ncli once per test process.
func bin(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		dir, err := os.MkdirTemp("", "climatrix-bin-")
		if err != nil {
			binErr = err
			return
		}
		binPath = filepath.Join(dir, "ncli")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "build", "-o", binPath, "./cmd/ncli")
		cmd.Dir = repoRoot()
		if out, err := cmd.CombinedOutput(); err != nil {
			binErr = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if binErr != nil {
		t.Fatal(binErr)
	}
	return binPath
}

// needsRelay skips relay-backed tests under -short, so `just check`
// stays fast; `just test-integration-cli` runs them.
func needsRelay(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("relay-backed matrix test; run without -short")
	}
}

// Env is one isolated ncli user: its own config dir (vault, prefs,
// relay contexts) and no inherited NCLI_* settings.
type Env struct {
	t     *testing.T
	Dir   string
	extra []string
}

func NewEnv(t *testing.T) *Env {
	t.Helper()
	dir := t.TempDir()
	return &Env{t: t, Dir: dir}
}

// Setenv adds a variable to every command this Env runs.
func (e *Env) Setenv(k, v string) { e.extra = append(e.extra, k+"="+v) }

func (e *Env) environ() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasPrefix(k, "NCLI_"), k == "HOME", k == "XDG_CONFIG_HOME", k == "NO_COLOR":
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"HOME="+e.Dir,
		"XDG_CONFIG_HOME="+filepath.Join(e.Dir, "config"),
		"NCLI_VAULT_PASSWORD=climatrix-vault-password",
	)
	return append(env, e.extra...)
}

// Result is one finished command.
type Result struct {
	Args   []string
	Stdout string
	Stderr string
	Code   int
}

func (r Result) String() string {
	return fmt.Sprintf("ncli %s\nexit=%d\nstdout:\n%s\nstderr:\n%s", strings.Join(r.Args, " "), r.Code, r.Stdout, r.Stderr)
}

// Run executes ncli with args in this Env, from its own directory.
func (e *Env) Run(t *testing.T, args ...string) Result {
	t.Helper()
	return e.RunStdin(t, "", args...)
}

func (e *Env) RunStdin(t *testing.T, stdin string, args ...string) Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin(t), args...)
	cmd.Dir = e.Dir
	cmd.Env = e.environ()
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	r := Result{Args: args, Stdout: out.String(), Stderr: errb.String()}
	if ee, ok := err.(*exec.ExitError); ok {
		r.Code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running ncli %v: %v", args, err)
	}
	if ctx.Err() != nil {
		t.Fatalf("ncli %v timed out\n%s", args, r)
	}
	return r
}

// MustOK runs and fails the test on a non-zero exit.
func (e *Env) MustOK(t *testing.T, args ...string) Result {
	t.Helper()
	r := e.Run(t, args...)
	if r.Code != 0 {
		t.Fatalf("want exit 0\n%s", r)
	}
	return r
}

// JSON decodes stdout into v, failing on anything but exactly one value.
func (r Result) JSON(t *testing.T, v any) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(r.Stdout))
	if err := dec.Decode(v); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, r)
	}
	if dec.More() {
		t.Fatalf("stdout has more than one JSON value\n%s", r)
	}
}

// ErrReport is the --json failure line on stderr.
type ErrReport struct {
	Error     string `json:"error"`
	Code      string `json:"code"`
	Retryable bool   `json:"retryable"`
	Input     string `json:"input"`
}

// Exit codes per AGENTS.md's contract table.
var exitCodes = map[string]int{
	"internal": 1, "usage": 2, "invalid_input": 3, "not_found": 4,
	"conflict": 5, "network": 6, "auth": 7, "unsupported": 8,
}

// ExpectErr asserts a --json failure: stdout empty, every stderr line
// JSON, exactly one error report with this code and the matching exit.
func (r Result) ExpectErr(t *testing.T, code string) ErrReport {
	t.Helper()
	return r.expectErr(t, code, false)
}

// expectErr is ExpectErr; stdoutOK allows a result on stdout alongside
// the failure (ping's per-relay table).
func (r Result) expectErr(t *testing.T, code string, stdoutOK bool) ErrReport {
	t.Helper()
	if r.Stdout != "" && !stdoutOK {
		t.Errorf("failure wrote to stdout\n%s", r)
	}
	var reports []ErrReport
	for _, line := range r.stderrLines() {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("stderr line is not JSON under --json: %q\n%s", line, r)
		}
		if _, ok := m["error"].(string); ok {
			if _, isLog := m["level"]; isLog {
				continue
			}
			var er ErrReport
			_ = json.Unmarshal([]byte(line), &er)
			reports = append(reports, er)
		}
	}
	if len(reports) != 1 {
		t.Fatalf("want exactly one error report, got %d\n%s", len(reports), r)
	}
	er := reports[0]
	if er.Code != code {
		t.Errorf("code = %q, want %q\n%s", er.Code, code, r)
	}
	if want := exitCodes[code]; r.Code != want {
		t.Errorf("exit = %d, want %d (%s)\n%s", r.Code, want, code, r)
	}
	if wantRetry := code == "network" || code == "conflict"; er.Retryable != wantRetry {
		t.Errorf("retryable = %v, want %v\n%s", er.Retryable, wantRetry, r)
	}
	if strings.HasPrefix(er.Input, "nsec1") || (len(er.Input) == 64 && isPrivOf(er.Input)) {
		t.Errorf("error report echoes private key material\n%s", r)
	}
	return er
}

// ExpectCleanJSONStderr asserts every stderr line is a JSON object with
// no ANSI escapes -- the --json logging contract.
func (r Result) ExpectCleanJSONStderr(t *testing.T) {
	t.Helper()
	for _, line := range r.stderrLines() {
		if strings.Contains(line, "\x1b[") {
			t.Errorf("ANSI escape on stderr under --json: %q", line)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Errorf("stderr line is not JSON under --json: %q", line)
		}
	}
}

func (r Result) stderrLines() []string {
	var lines []string
	sc := bufio.NewScanner(strings.NewReader(r.Stderr))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// Actor is one fixed test identity from testdata/actors.json.
type Actor struct {
	Name    string `json:"name"`
	Role    string `json:"role"`
	PrivHex string `json:"privkey"`
	PubHex  string `json:"pubkey"`
	Nsec    string `json:"nsec"`
	Npub    string `json:"npub"`
}

var (
	actorsOnce sync.Once
	actors     map[string]Actor
)

// A returns a fixed actor by name (operator, admin, alice, bob, carol,
// eve, mallory, agent).
func A(t *testing.T, name string) Actor {
	t.Helper()
	actorsOnce.Do(func() {
		b, err := os.ReadFile(filepath.Join(repoRoot(), "testdata", "actors.json"))
		if err != nil {
			panic(err)
		}
		var list []Actor
		if err := json.Unmarshal(b, &list); err != nil {
			panic(err)
		}
		actors = map[string]Actor{}
		for _, a := range list {
			actors[a.Name] = a
		}
	})
	a, ok := actors[name]
	if !ok {
		t.Fatalf("no actor %q in testdata/actors.json", name)
	}
	return a
}

func isPrivOf(hex string) bool {
	for _, a := range actors {
		if strings.EqualFold(a.PrivHex, hex) {
			return true
		}
	}
	return false
}

// CorpusPath is testdata/events.json: 339 real signed events.
func CorpusPath() string { return filepath.Join(repoRoot(), "testdata", "events.json") }

// examplePath resolves a file under examples/.
func examplePath(rel string) string { return filepath.Join(repoRoot(), "examples", rel) }

// Relay is one running `ncli relay` process.
type Relay struct {
	Port       int
	URL        string // ws://localhost:<port>
	ConfigPath string
	LogPath    string
	Dir        string
	args       []string
	cmd        *exec.Cmd
	env        *Env
}

func allocPort(t *testing.T) int {
	t.Helper()
	for range portLast - portFirst + 1 {
		p := portFirst + int(nextPort.Add(1)-1)%(portLast-portFirst+1)
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			continue
		}
		_ = l.Close()
		return p
	}
	t.Fatalf("no free port in %d-%d", portFirst, portLast)
	return 0
}

// StartRelay runs `ncli relay` with base (a YAML document, "" for an
// empty one) deep-merged with overrides. port, store, logdir, nip11.url
// and logs.filename are always set by the harness; nip11.privkey defaults
// to the operator actor.
func StartRelay(t *testing.T, base string, overrides map[string]any) *Relay {
	t.Helper()
	return StartRelayArgs(t, base, overrides)
}

// StartRelayArgs is StartRelay with extra `ncli relay` flags (e.g. --json).
func StartRelayArgs(t *testing.T, base string, overrides map[string]any, args ...string) *Relay {
	t.Helper()
	cfg := map[string]any{}
	if base != "" {
		if err := yaml.Unmarshal([]byte(base), &cfg); err != nil {
			t.Fatalf("relay base config: %v", err)
		}
	}
	deepMerge(cfg, overrides)

	dir := t.TempDir()
	port := allocPort(t)
	url := fmt.Sprintf("ws://localhost:%d", port)
	nip11, _ := cfg["nip11"].(map[string]any)
	if nip11 == nil {
		nip11 = map[string]any{}
	}
	if _, ok := nip11["privkey"]; !ok {
		nip11["privkey"] = A(t, "operator").PrivHex
		delete(nip11, "pubkey")
	}
	if _, ok := nip11["name"]; !ok {
		nip11["name"] = "climatrix"
	}
	nip11["url"] = url
	cfg["nip11"] = nip11
	cfg["port"] = port
	cfg["store"] = filepath.Join(dir, "db", "notes.db")
	cfg["logdir"] = filepath.Join(dir, "logs")
	logs, _ := cfg["logs"].(map[string]any)
	if logs == nil {
		logs = map[string]any{}
	}
	logs["filename"] = filepath.Join(dir, "logs", "nrelay.log")
	cfg["logs"] = logs

	b, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r := &Relay{Port: port, URL: url, Dir: dir,
		ConfigPath: filepath.Join(dir, "relay.yaml"),
		LogPath:    filepath.Join(dir, "relay.out"),
		args:       args,
		env:        NewEnv(t)}
	if err := os.WriteFile(r.ConfigPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	r.start(t)
	return r
}

func (r *Relay) start(t *testing.T) {
	t.Helper()
	logf, err := os.OpenFile(r.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	r.cmd = exec.Command(bin(t), append([]string{"relay", "--config", r.ConfigPath}, r.args...)...)
	r.cmd.Dir = r.Dir
	r.cmd.Env = r.env.environ()
	r.cmd.Stdout, r.cmd.Stderr = logf, logf
	if err := r.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Stop(); _ = logf.Close() })

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(r.Log(), "listening...") {
			if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", r.Port), time.Second); err == nil {
				_ = c.Close()
				return
			}
		}
		if r.cmd.ProcessState != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("relay on %d never started listening\n%s", r.Port, r.Log())
}

// Stop kills the relay; safe to call twice.
func (r *Relay) Stop() {
	if r.cmd == nil || r.cmd.Process == nil {
		return
	}
	_ = r.cmd.Process.Kill()
	_ = r.cmd.Wait()
	r.cmd = nil
}

// Restart stops and starts the same relay (same store, same port).
func (r *Relay) Restart(t *testing.T) {
	t.Helper()
	r.Stop()
	r.start(t)
}

// Log returns the relay's combined stdout/stderr so far.
func (r *Relay) Log() string {
	b, _ := os.ReadFile(r.LogPath)
	return string(b)
}

// AdminConfig writes a relay config pointing at this relay's port but
// signing with privHex -- for driving `relay stats`/`members`/... as a
// key that may or may not be the relay's own.
func (r *Relay) AdminConfig(t *testing.T, privHex string) string {
	t.Helper()
	raw, err := os.ReadFile(r.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	nip11 := cfg["nip11"].(map[string]any)
	nip11["privkey"] = privHex
	delete(nip11, "pubkey")
	b, _ := yaml.Marshal(cfg)
	p := filepath.Join(t.TempDir(), "admin.yaml")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Seed publishes the testdata corpus to this relay and asserts every
// event was accepted.
func (r *Relay) Seed(t *testing.T) {
	t.Helper()
	res := NewEnv(t).Run(t, "publish", "-e", CorpusPath(), "-s", r.URL, "--json")
	var rep struct {
		Succeeded int `json:"succeeded"`
		Failed    int `json:"failed"`
	}
	res.JSON(t, &rep)
	if rep.Failed != 0 || rep.Succeeded == 0 {
		t.Fatalf("seeding corpus: %d ok, %d failed\n%s", rep.Succeeded, rep.Failed, res)
	}
}

func deepMerge(dst, src map[string]any) {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			dm, ok := dst[k].(map[string]any)
			if !ok {
				dm = map[string]any{}
			}
			deepMerge(dm, sm)
			dst[k] = dm
			continue
		}
		dst[k] = v
	}
}

// Nip05Server serves /.well-known/nostr.json over TLS on 127.0.0.1.
// Identifiers look like "alice@127.0.0.1:<port>"; Env.TrustNip05 makes
// the binary trust its certificate.
type Nip05Server struct {
	*httptest.Server
	Domain   string
	CertFile string
}

func StartNip05(t *testing.T, names map[string]string) *Nip05Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/nostr.json", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		_ = json.NewEncoder(w).Encode(map[string]any{"names": names})
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	certFile := filepath.Join(t.TempDir(), "nip05.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(certFile, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return &Nip05Server{Server: srv, Domain: strings.TrimPrefix(srv.URL, "https://"), CertFile: certFile}
}

// ID returns name@domain for this server.
func (s *Nip05Server) ID(name string) string { return name + "@" + s.Domain }

// TrustNip05 makes this Env's ncli trust s's certificate.
func (e *Env) TrustNip05(s *Nip05Server) { e.Setenv("SSL_CERT_FILE", s.CertFile) }

// WriteFile writes content under the Env's directory and returns its path.
func (e *Env) WriteFile(name, content string) string {
	e.t.Helper()
	p := filepath.Join(e.Dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
	return p
}

// DeadRelayURL is a local port nothing listens on.
func DeadRelayURL(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("ws://localhost:%d", allocPort(t))
}
