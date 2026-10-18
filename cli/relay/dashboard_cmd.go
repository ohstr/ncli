package relay

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/relay/adminplane"
	"github.com/ohstr/ncli/web/dashboard"
	"github.com/spf13/cobra"
)

func addDashboardCommand(cmd *cobra.Command) {
	dashCmd := &cobra.Command{
		Use:   "dashboard",
		Short: "Open a running relay's dashboard in the browser, signed by this CLI",
		Long: `Serves the dashboard on 127.0.0.1 and forwards its requests to the
relay's admin listener, signing each with nip11.privkey: the browser never
holds a key, and the relay need not serve the dashboard itself. Open the
printed link; it works once, in one browser.`,
		Example: `  ncli relay dashboard --config relay.yaml
  ncli relay dashboard --port 8088 --no-open`,
		Args: common.NoArgs,
		RunE: runDashboard,
	}
	dashCmd.Flags().Int("port", 0, "local port to serve on (default: a free one)")
	dashCmd.Flags().Bool("no-open", false, "print the link without opening a browser")
	cmd.AddCommand(dashCmd)
}

const localCookie = "ncli_dashboard"

func randomString() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

type dashboardProxy struct {
	cmd     *cobra.Command
	privKey string
	base    string
	client  *http.Client
	pubkey  string
	addr    string
	token   string
	session string
	csrf    string
	ui      http.Handler

	mu   sync.Mutex
	used bool
}

func runDashboard(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	port, _ := cmd.Flags().GetInt("port")
	noOpen, _ := cmd.Flags().GetBool("no-open")

	privKey, _, _, err := getAdminConfig()
	if err != nil {
		return common.RuntimeError(cmd, &common.CLIError{Err: err, Code: common.CodeUsage, Help: common.HelpAfterError})
	}
	base, tlsCfg, err := adminTarget(cmd)
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	// Fail now, not in the browser, if the relay is down or the key isn't
	// an admin.
	var who struct{ Pubkey string }
	if _, err := apiCall(cmd, http.MethodGet, "/admin/api/session", nil, &who); err != nil {
		return common.RuntimeError(cmd, err)
	}

	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return common.RuntimeError(cmd, &common.CLIError{Err: err, Code: common.CodeConflict, Input: strconv.Itoa(port)})
	}
	p := &dashboardProxy{
		cmd: cmd, privKey: privKey, base: base, pubkey: who.Pubkey,
		client: &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}},
		addr:   ln.Addr().String(), token: randomString(), session: randomString(), csrf: randomString(),
		ui: http.StripPrefix("/admin/ui", adminplane.UIHandler(dashboard.FS())),
	}
	srv := &http.Server{Handler: p, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()

	link := fmt.Sprintf("http://%s/?t=%s", p.addr, p.token)
	if jsonMode {
		common.PrintJSONLine(map[string]string{"url": link, "relay": base, "pubkey": who.Pubkey})
	} else {
		fmt.Println(link)
		fmt.Fprintf(os.Stderr, "Dashboard for %s at the link above (one-time; Ctrl-C to stop).\n", base)
	}
	if !noOpen && !jsonMode {
		openBrowser(link)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	shut, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(shut)
	return nil
}

func openBrowser(url string) {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", url)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		c = exec.Command("xdg-open", url)
	}
	_ = c.Start()
}

func (p *dashboardProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")

	// A page on another site can make the browser resolve its own hostname
	// to 127.0.0.1 (DNS rebinding); only requests for this exact address
	// are ours.
	if r.Host != p.addr {
		http.Error(w, "wrong host", http.StatusForbidden)
		return
	}

	if r.URL.Path == "/" {
		if t := r.URL.Query().Get("t"); t != "" {
			p.mu.Lock()
			ok := !p.used && equal(t, p.token)
			p.used = p.used || ok
			p.mu.Unlock()
			if !ok {
				http.Error(w, "this link was already used or is wrong; restart `ncli relay dashboard`", http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: localCookie, Value: p.session, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		}
		http.Redirect(w, r, "/admin/ui/", http.StatusFound)
		return
	}

	c, err := r.Cookie(localCookie)
	if err != nil || !equal(c.Value, p.session) {
		http.Error(w, "open the link `ncli relay dashboard` printed", http.StatusUnauthorized)
		return
	}

	switch {
	case strings.HasPrefix(r.URL.Path, "/admin/ui/"):
		p.ui.ServeHTTP(w, r)
	case r.URL.Path == "/admin/api/session":
		// No expiry: the session lasts as long as this command.
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"pubkey":%q,"expires_at":0,"csrf":%q}`, p.pubkey, p.csrf)
	case r.URL.Path == "/admin/api/login" || r.URL.Path == "/admin/api/logout":
		http.NotFound(w, r)
	case strings.HasPrefix(r.URL.Path, "/admin/api/"):
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !equal(r.Header.Get("X-CSRF-Token"), p.csrf) {
			http.Error(w, "missing CSRF token", http.StatusForbidden)
			return
		}
		p.forward(w, r)
	default:
		http.NotFound(w, r)
	}
}

// forward replays r against the admin listener, signed for that URL, and
// streams the answer back (SSE included).
func (p *dashboardProxy) forward(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	url := p.base + r.URL.RequestURI()
	header, err := common.GenerateNIP98Header(p.privKey, url, r.Method, body)
	if err != nil {
		http.Error(w, "failed to sign", http.StatusInternalServerError)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, url, bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	req.Header.Set("Authorization", header)
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if acc := r.Header.Get("Accept"); acc != "" {
		req.Header.Set("Accept", acc)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			http.Error(w, "relay unreachable: "+err.Error(), http.StatusBadGateway)
		}
		return
	}
	defer func() { _ = resp.Body.Close() }()
	for _, k := range []string{"Content-Type", "X-Accel-Buffering"} {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	rc := http.NewResponseController(w)
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			_ = rc.Flush()
		}
		if err != nil {
			return
		}
	}
}
