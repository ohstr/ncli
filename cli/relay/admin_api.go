package relay

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/relay/jobs"
	"github.com/ohstr/ncli/cli/relay/metrics"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/term"
)

// addAdminAPICommands adds status, index and jobs: NIP-98-signed requests to
// a running relay's admin listener.
func addAdminAPICommands(cmd *cobra.Command) {
	cmd.PersistentFlags().String("admin-url", "", "admin listener URL (default: from admin.listen in the relay config)")

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show a running relay's health and live metrics",
		Example: `  ncli relay status --config relay.yaml
  ncli relay status --watch`,
		Args: common.NoArgs,
		RunE: runStatus,
	}
	statusCmd.Flags().Bool("watch", false, "refresh every 2s until interrupted (NDJSON with --json)")
	cmd.AddCommand(statusCmd)

	addIndexCommands(cmd)

	addVerifyCommand(cmd)
}

// adminTarget resolves the admin listener's base URL, and the TLS config
// that trusts its own certificate when the relay config names one.
func adminTarget(cmd *cobra.Command) (string, *tls.Config, error) {
	base, _ := cmd.Flags().GetString("admin-url")
	certFile := viper.GetString("admin.tls.cert")
	if base == "" {
		listen := viper.GetString("admin.listen")
		if listen == "off" {
			return "", nil, &common.CLIError{Err: errors.New("the admin listener is off (admin.listen: off)"), Code: common.CodeUsage}
		}
		if listen == "" {
			port := viper.GetInt("port")
			if port == 0 {
				port = defaultPort
			}
			listen = net.JoinHostPort("127.0.0.1", strconv.Itoa(port+1))
		}
		host, port, err := net.SplitHostPort(listen)
		if err != nil {
			return "", nil, &common.CLIError{Err: fmt.Errorf("admin.listen: %w", err), Code: common.CodeInvalidInput, Input: listen}
		}
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		scheme := "http"
		if certFile != "" {
			scheme = "https"
		}
		base = scheme + "://" + net.JoinHostPort(host, port)
	}
	base = strings.TrimRight(base, "/")

	var tlsCfg *tls.Config
	if strings.HasPrefix(base, "https://") && certFile != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if pem, err := os.ReadFile(certFile); err == nil {
			pool.AppendCertsFromPEM(pem)
		}
		tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	}
	return base, tlsCfg, nil
}

// apiError is the admin API's JSON error body.
type apiError struct {
	Error string    `json:"error"`
	Job   *jobs.Job `json:"job"`
}

// apiCall sends a signed request to the admin listener and decodes a 2xx
// JSON answer into out.
func apiCall(cmd *cobra.Command, method, path string, body, out any) (int, error) {
	privKey, _, _, err := getAdminConfig()
	if err != nil {
		return 0, &common.CLIError{Err: err, Code: common.CodeUsage, Help: common.HelpAfterError}
	}
	base, tlsCfg, err := adminTarget(cmd)
	if err != nil {
		return 0, err
	}
	url := base + path

	var encoded []byte
	if body != nil {
		if encoded, err = json.Marshal(body); err != nil {
			return 0, err
		}
	}
	header, err := common.GenerateNIP98Header(privKey, url, method, encoded)
	if err != nil {
		return 0, &common.CLIError{Err: fmt.Errorf("failed to sign admin request: %w", err), Code: common.CodeInvalidInput}
	}
	req, err := http.NewRequest(method, url, bytes.NewReader(encoded))
	if err != nil {
		return 0, &common.CLIError{Err: err, Code: common.CodeInvalidInput, Input: url}
	}
	req.Header.Set("Authorization", header)
	if encoded != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 45 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsCfg}}
	var resp *http.Response
	err = common.WithSpinner(cmd, "contacting the relay's admin listener", func() error {
		var dErr error
		resp, dErr = client.Do(req)
		return dErr
	})
	if err != nil {
		hint := fmt.Sprintf("is the relay running, with its admin listener at %s?", base)
		if droppedByServer(err) {
			hint = fmt.Sprintf("the relay at %s closed the connection without answering; check its log for an error", base)
		}
		return 0, &common.CLIError{
			Err:   fmt.Errorf("request failed (%s): %w", hint, err),
			Code:  common.CodeNetwork,
			Input: url,
		}
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))

	if resp.StatusCode >= 400 {
		var ae apiError
		msg := strings.TrimSpace(string(data))
		if json.Unmarshal(data, &ae) == nil && ae.Error != "" {
			msg = ae.Error
			if ae.Job != nil {
				msg = fmt.Sprintf("%s (job %s, %s)", msg, ae.Job.ID, ae.Job.Kind)
			}
		}
		code := common.CodeInternal
		switch {
		case resp.StatusCode == http.StatusBadRequest:
			code = common.CodeInvalidInput
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			code = common.CodeAuth
			msg += " (the request is signed with nip11.privkey, which must be the relay's key or in nip86.admins)"
		case resp.StatusCode == http.StatusNotFound:
			code = common.CodeNotFound
		case resp.StatusCode == http.StatusConflict:
			code = common.CodeConflict
		case resp.StatusCode == http.StatusNotImplemented:
			code = common.CodeUsage
		case resp.StatusCode == http.StatusUnprocessableEntity:
			code = common.CodeUnsupported
		case resp.StatusCode == http.StatusTooManyRequests:
			code = common.CodeNetwork
		case resp.StatusCode >= 500:
			code = common.CodeNetwork
			msg = fmt.Sprintf("the relay failed to answer (HTTP %d): %s", resp.StatusCode, msg)
		}
		return resp.StatusCode, &common.CLIError{Err: fmt.Errorf("%s", msg), Code: code, Input: url}
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("failed to decode response: %w", err)
		}
	}
	return resp.StatusCode, nil
}

/////////////////////////////////////////////////////////////////////
// status
/////////////////////////////////////////////////////////////////////

// droppedByServer reports a request the server accepted and then hung up
// on, as opposed to one that never connected.
func droppedByServer(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET)
}

type statusJSON struct {
	Metrics metrics.Point    `json:"metrics"`
	Indexes []jobs.IndexInfo `json:"indexes"`
	// IndexesError says why indexes couldn't be listed; the metrics still render.
	IndexesError string `json:"indexes_error,omitempty"`
}

func fetchStatus(cmd *cobra.Command) (statusJSON, error) {
	var st statusJSON
	if _, err := apiCall(cmd, http.MethodGet, "/admin/api/metrics", nil, &st.Metrics); err != nil {
		return st, err
	}
	var ix struct{ Indexes []jobs.IndexInfo }
	if _, err := apiCall(cmd, http.MethodGet, "/admin/api/indexes", nil, &ix); err != nil {
		st.IndexesError = err.Error()
	}
	st.Indexes = ix.Indexes
	if st.Indexes == nil {
		st.Indexes = []jobs.IndexInfo{}
	}
	return st, nil
}

func runStatus(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	watch, _ := cmd.Flags().GetBool("watch")

	if !watch {
		st, err := fetchStatus(cmd)
		if err != nil {
			return common.RuntimeError(cmd, err)
		}
		if jsonMode {
			common.PrintJSON(st)
			return nil
		}
		renderStatus(st)
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	clear := !jsonMode && term.IsTerminal(int(os.Stdout.Fd()))
	for {
		st, err := fetchStatus(cmd)
		if err != nil {
			return common.RuntimeError(cmd, err)
		}
		if jsonMode {
			common.PrintJSONLine(st)
		} else {
			if clear {
				fmt.Print("\033[H\033[2J")
			}
			renderStatus(st)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(2 * time.Second):
		}
	}
}

func renderStatus(st statusJSON) {
	v, tot := st.Metrics.Values, st.Metrics.Totals
	val := func(k string) float64 { return v[k] }
	sumPrefix := func(m map[string]float64, prefix string) float64 {
		n := 0.0
		for k, x := range m {
			if strings.HasPrefix(k, prefix) {
				n += x
			}
		}
		return n
	}

	fmt.Println(titleStyle.Render(" ncli relay: status "))
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	row := func(label, value string) { _, _ = fmt.Fprintf(tw, " %s\t%s\n", label, value) }

	row("Uptime", formatSeconds(val("uptime_seconds")))
	row("Connections", fmt.Sprintf("%.0f sessions, %.0f subscriptions", val("ws_sessions"), val("subscriptions_active")))
	row("Messages/s", fmt.Sprintf("EVENT %s  REQ %s  COUNT %s", rate(v, "messages_total:EVENT"), rate(v, "messages_total:REQ"), rate(v, "messages_total:COUNT")))
	row("Events/s", fmt.Sprintf("accepted %s  duplicate %s  rejected %s", rate(v, "events_total:accepted"), rate(v, "events_total:duplicate"), rate(v, "events_total:rejected")))
	row("Latency p99", fmt.Sprintf("EVENT→OK %s  REQ→EOSE %s  store write %s", ms(v, "event_ok_seconds.p99"), ms(v, "req_eose_seconds.p99"), ms(v, "store_batch_seconds.p99")))
	row("Store", fmt.Sprintf("%s  write queue %.0f/%.0f  scans %.0f/%s", formatBytes(val("store_size_bytes")),
		val("store_task_queue_depth"), val("store_task_queue_capacity"), val("scans_inflight"), slots(val("scan_slots"))))
	row("Queues", fmt.Sprintf("profile checks %.0f", val("verify_queue_depth")))
	row("Security", fmt.Sprintf("AUTH rejected %.0f  admin denied %.0f  scan-busy refusals %.0f",
		sumPrefix(tot, "nip42_auth_rejected_total:"), sumPrefix(tot, "admin_denied_total:"), tot["scan_busy_total"]))
	if val("members") > 0 || val("groups") > 0 || val("huddle_rooms") > 0 {
		row("Community", fmt.Sprintf("%.0f members, %.0f groups, %.0f huddle rooms (%.0f peers)", val("members"), val("groups"), val("huddle_rooms"), val("huddle_peers")))
	}
	row("Process", fmt.Sprintf("%.0f goroutines, %s RSS, %.2f CPU cores", val("goroutines"), formatBytes(val("rss_bytes")), val("cpu_seconds_total")))
	now := time.Now()
	for _, ix := range st.Indexes {
		summary := fmt.Sprintf("%s, %s entries", stateLabel(ix.State), jobs.Count(ix.Entries))
		if ix.Behind > 0 && ix.Enabled {
			summary += fmt.Sprintf(", %s behind", jobs.Count(ix.Behind))
		}
		if note := indexNote(ix, now); note != "" {
			summary += " — " + note
		}
		row("Index "+ix.Name, summary)
	}
	if st.IndexesError != "" {
		row("Indexes", "unavailable: "+st.IndexesError)
	}
	_ = tw.Flush()
}

func rate(v map[string]float64, k string) string {
	x, ok := v[k]
	if !ok {
		return "-"
	}
	return strconv.FormatFloat(x, 'f', 1, 64)
}

func ms(v map[string]float64, k string) string {
	x, ok := v[k]
	if !ok {
		return "-"
	}
	return formatDuration(time.Duration(x * float64(time.Second)))
}

func slots(n float64) string {
	if n == 0 {
		return "∞"
	}
	return strconv.FormatFloat(n, 'f', 0, 64)
}

func formatDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%.1fms", float64(d.Microseconds())/1000)
	default:
		return d.Round(10 * time.Millisecond).String()
	}
}

func formatSeconds(s float64) string {
	return (time.Duration(s) * time.Second).Round(time.Second).String()
}

func formatBytes(b float64) string {
	const unit = 1024.0
	if b < unit {
		return fmt.Sprintf("%.0f B", b)
	}
	exp, n := 0, b/unit
	for n >= unit && exp < 4 {
		n /= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", n, "KMGTP"[exp])
}
