package relay

import (
	"bufio"
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
	"sort"
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

	indexCmd := &cobra.Command{
		Use:   "index",
		Short: "Rebuild or drop a running relay's search, zap and stats indexes",
		Long: `The search index (Meilisearch profiles), the zap index (top zapped) and
the stats index (each profile's zaps and follows, for scores and NIP-85)
are derived from stored events and can be rebuilt from them at any time.`,
		Example: `  ncli relay index list
  ncli relay index rebuild zaps --follow`,
		RunE: common.RequireSubcommand,
	}
	indexCmd.AddCommand(&cobra.Command{
		Use:     "list",
		Short:   "List indexes with their running and last job",
		Example: `  ncli relay index list`,
		Args:    common.NoArgs,
		RunE:    runIndexList,
	})
	rebuildCmd := &cobra.Command{
		Use:   "rebuild <search|zaps|stats|all>",
		Short: "Rebuild an index from stored events",
		Long: `Starts a job and returns its id; --follow waits for it with progress.
Rebuilding an index that is already rebuilding returns the running job.

zaps and stats always build a new index beside the live one and swap it
in. search upserts into the live index; --fresh builds a new one and swaps
it in, dropping profiles no longer stored. Either way the relay keeps
answering from the old index until the new one is ready.

A search rebuild rescores every profile from what the relay already
knows, with the current cache.scoring weights; it never re-fetches a
NIP-05 or lightning address (see ncli relay verify --refresh).`,
		Example: `  ncli relay index rebuild zaps --follow
  ncli relay index rebuild search --fresh --follow
  ncli relay index rebuild search --dry-run`,
		Args: common.ExactArgs(1),
		RunE: runIndexRebuild,
	}
	rebuildCmd.Flags().Bool("fresh", false, "build a new index and swap it in (search)")
	rebuildCmd.Flags().Bool("reverify", false, "removed: use ncli relay verify <pubkey> --refresh")
	_ = rebuildCmd.Flags().MarkHidden("reverify")
	rebuildCmd.Flags().Bool("follow", false, "wait for the job, showing progress, and exit with its outcome")
	rebuildCmd.Flags().Bool("dry-run", false, "only report how many items a rebuild would scan")
	indexCmd.AddCommand(rebuildCmd)
	dropCmd := &cobra.Command{
		Use:   "drop <search|zaps|stats>",
		Short: "Empty an index (stored events are kept)",
		Long: `Search and top-zapped answers stay empty until the index is rebuilt.
Asks for confirmation; --yes skips it, and is required with --json or no
terminal. To refresh an index without an empty window, use rebuild.`,
		Example: `  ncli relay index drop search --yes`,
		Args:    common.ExactArgs(1),
		RunE:    runIndexDrop,
	}
	dropCmd.Flags().Bool("yes", false, "skip the confirmation prompt")
	indexCmd.AddCommand(dropCmd)
	cmd.AddCommand(indexCmd)

	jobsCmd := &cobra.Command{
		Use:     "jobs",
		Short:   "List, inspect or cancel a running relay's index jobs",
		Example: `  ncli relay jobs list`,
		RunE:    common.RequireSubcommand,
	}
	jobsCmd.AddCommand(&cobra.Command{
		Use:     "list",
		Short:   "List running and recent jobs",
		Example: `  ncli relay jobs list`,
		Args:    common.NoArgs,
		RunE:    runJobsList,
	})
	showCmd := &cobra.Command{
		Use:     "show <id>",
		Short:   "Show one job",
		Example: `  ncli relay jobs show 3f9a1c2b7d4e --follow`,
		Args:    common.ExactArgs(1),
		RunE:    runJobsShow,
	}
	showCmd.Flags().Bool("follow", false, "wait for the job, showing progress")
	jobsCmd.AddCommand(showCmd)
	jobsCmd.AddCommand(&cobra.Command{
		Use:     "cancel <id>",
		Short:   "Cancel a running job",
		Example: `  ncli relay jobs cancel 3f9a1c2b7d4e`,
		Args:    common.ExactArgs(1),
		RunE:    runJobsCancel,
	})
	cmd.AddCommand(jobsCmd)

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
		return 0, &common.CLIError{
			Err:   fmt.Errorf("request failed (is the relay running, with its admin listener at %s?): %w", base, err),
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
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			code = common.CodeNetwork
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

type statusJSON struct {
	Metrics metrics.Point    `json:"metrics"`
	Indexes []jobs.IndexInfo `json:"indexes"`
}

func fetchStatus(cmd *cobra.Command) (statusJSON, error) {
	var st statusJSON
	if _, err := apiCall(cmd, http.MethodGet, "/admin/api/metrics", nil, &st.Metrics); err != nil {
		return st, err
	}
	var ix struct{ Indexes []jobs.IndexInfo }
	if _, err := apiCall(cmd, http.MethodGet, "/admin/api/indexes", nil, &ix); err != nil {
		return st, err
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
	row("Queues", fmt.Sprintf("verify %.0f (dropped %.0f)  search %.0f (dropped %.0f)",
		val("verify_queue_depth"), tot["verify_dropped_total"], val("search_index_queue_depth"), tot["search_index_dropped_total"]))
	row("Security", fmt.Sprintf("AUTH rejected %.0f  admin denied %.0f  scan-busy refusals %.0f",
		sumPrefix(tot, "nip42_auth_rejected_total:"), sumPrefix(tot, "admin_denied_total:"), tot["scan_busy_total"]))
	if val("members") > 0 || val("groups") > 0 || val("huddle_rooms") > 0 {
		row("Community", fmt.Sprintf("%.0f members, %.0f groups, %.0f huddle rooms (%.0f peers)", val("members"), val("groups"), val("huddle_rooms"), val("huddle_peers")))
	}
	row("Process", fmt.Sprintf("%.0f goroutines, %s RSS, %.2f CPU cores", val("goroutines"), formatBytes(val("rss_bytes")), val("cpu_seconds_total")))
	for _, ix := range st.Indexes {
		row("Index "+ix.Name, indexSummary(ix))
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

/////////////////////////////////////////////////////////////////////
// index
/////////////////////////////////////////////////////////////////////

func indexSummary(ix jobs.IndexInfo) string {
	if !ix.Available {
		return statusWaitStyle.Render("unavailable") + ": " + ix.Unavailable
	}
	if j := ix.Running; j != nil {
		return statusOkStyle.Render(string(j.Kind)+" running") + " " + jobProgress(*j)
	}
	if j := ix.Last; j != nil {
		return fmt.Sprintf("idle, last %s %s %s", j.Kind, stateText(j.State), relativeTime(j.FinishedAt))
	}
	return "idle"
}

func stateText(s jobs.State) string {
	switch s {
	case jobs.StateSucceeded:
		return statusOkStyle.Render(string(s))
	case jobs.StateFailed:
		return statusErrStyle.Render(string(s))
	}
	return statusWaitStyle.Render(string(s))
}

func jobProgress(j jobs.Job) string {
	if j.Kind != jobs.KindRebuild {
		return ""
	}
	s := fmt.Sprintf("scanned %d", j.Scanned)
	if j.Total > 0 {
		pct := float64(j.Scanned) / float64(j.Total) * 100
		s = fmt.Sprintf("%.0f%% (%d/~%d)", min(pct, 100), j.Scanned, j.Total)
		if j.State == jobs.StateRunning && j.Scanned > 0 {
			elapsed := time.Since(time.Unix(j.StartedAt, 0))
			left := time.Duration(float64(elapsed) * float64(j.Total-j.Scanned) / float64(j.Scanned))
			if left > 0 {
				s += ", ~" + left.Round(time.Second).String() + " left"
			}
		}
	}
	return fmt.Sprintf("%s, indexed %d", s, j.Indexed)
}

func runIndexList(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	var out struct {
		Indexes []jobs.IndexInfo `json:"indexes"`
	}
	if _, err := apiCall(cmd, http.MethodGet, "/admin/api/indexes", nil, &out); err != nil {
		return common.RuntimeError(cmd, err)
	}
	if out.Indexes == nil {
		out.Indexes = []jobs.IndexInfo{}
	}
	if jsonMode {
		common.PrintJSON(out)
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "INDEX\tENTRIES\tSTATE")
	for _, ix := range out.Indexes {
		estimate := "-"
		switch {
		case ix.Available && ix.Estimate < 0:
			estimate = "unreachable"
		case ix.Available:
			estimate = fmt.Sprintf("~%d", ix.Estimate)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", ix.Name, estimate, indexSummary(ix))
	}
	return tw.Flush()
}

func indexNames(cmd *cobra.Command, arg string) ([]string, error) {
	if arg != "all" {
		return []string{arg}, nil
	}
	var out struct{ Indexes []jobs.IndexInfo }
	if _, err := apiCall(cmd, http.MethodGet, "/admin/api/indexes", nil, &out); err != nil {
		return nil, err
	}
	var names []string
	for _, ix := range out.Indexes {
		if ix.Available {
			names = append(names, ix.Name)
		}
	}
	return names, nil
}

func runIndexRebuild(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	follow, _ := cmd.Flags().GetBool("follow")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	req := jobs.RebuildRequest{DryRun: dryRun}
	req.Fresh, _ = cmd.Flags().GetBool("fresh")
	if reverify, _ := cmd.Flags().GetBool("reverify"); reverify {
		return &common.CLIError{
			Err:  errors.New("--reverify was removed: a rebuild never re-fetches NIP-05 or lightning addresses; use ncli relay verify <pubkey> --refresh"),
			Code: common.CodeUsage,
		}
	}

	names, err := indexNames(cmd, args[0])
	if err != nil {
		return common.RuntimeError(cmd, err)
	}

	if dryRun {
		var results []map[string]any
		for _, name := range names {
			var out map[string]any
			if _, err := apiCall(cmd, http.MethodPost, "/admin/api/indexes/"+name+"/rebuild", req, &out); err != nil {
				return common.RuntimeError(cmd, err)
			}
			results = append(results, out)
		}
		if jsonMode {
			common.PrintJSON(map[string]any{"dry_run": results})
			return nil
		}
		for _, r := range results {
			fmt.Printf("%v: would scan ~%v items (~%v entries now)\n", r["index"], r["scan_estimate"], r["estimate"])
		}
		return nil
	}

	var started []jobs.Job
	for _, name := range names {
		var job jobs.Job
		status, err := apiCall(cmd, http.MethodPost, "/admin/api/indexes/"+name+"/rebuild", req, &job)
		if err != nil {
			return common.RuntimeError(cmd, err)
		}
		if !jsonMode && !follow {
			verb := "started"
			if status == http.StatusOK {
				verb = "already running"
			}
			fmt.Printf("%s %s rebuild: job %s\n", statusOkStyle.Render("✔"), name, job.ID)
			fmt.Fprintf(os.Stderr, "%s. Follow it with `ncli relay jobs show %s --follow`.\n", verb, job.ID)
		}
		started = append(started, job)
	}
	if !follow {
		if jsonMode {
			common.PrintJSON(map[string]any{"jobs": started})
		}
		return nil
	}
	return followJobs(cmd, started)
}

func runIndexDrop(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	yes, _ := cmd.Flags().GetBool("yes")
	name := args[0]
	if !yes {
		if jsonMode || !term.IsTerminal(int(os.Stdin.Fd())) {
			return common.UsageError(cmd, fmt.Errorf("refusing to drop the %s index without --yes in a non-interactive session", name))
		}
		prompt := fmt.Sprintf("Empty the %s index? Its answers stay empty until a rebuild. [y/N] ", name)
		if !common.PromptYesNo(bufio.NewReader(os.Stdin), prompt) {
			return common.RuntimeError(cmd, errors.New("aborted"))
		}
	}
	var job jobs.Job
	if _, err := apiCall(cmd, http.MethodPost, "/admin/api/indexes/"+name+"/drop", nil, &job); err != nil {
		return common.RuntimeError(cmd, err)
	}
	if job.State == jobs.StateFailed {
		return common.RuntimeError(cmd, fmt.Errorf("drop failed: %s", job.Error))
	}
	if jsonMode {
		common.PrintJSON(job)
		return nil
	}
	fmt.Printf("%s %s index dropped (job %s)\n", statusOkStyle.Render("✔"), name, job.ID)
	return nil
}

/////////////////////////////////////////////////////////////////////
// jobs
/////////////////////////////////////////////////////////////////////

func runJobsList(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	var out struct {
		Jobs []jobs.Job `json:"jobs"`
	}
	if _, err := apiCall(cmd, http.MethodGet, "/admin/api/jobs", nil, &out); err != nil {
		return common.RuntimeError(cmd, err)
	}
	if out.Jobs == nil {
		out.Jobs = []jobs.Job{}
	}
	if jsonMode {
		common.PrintJSON(out)
		return nil
	}
	if len(out.Jobs) == 0 {
		fmt.Println(" (no jobs)")
		return nil
	}
	sort.SliceStable(out.Jobs, func(i, j int) bool { return out.Jobs[i].StartedAt > out.Jobs[j].StartedAt })
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tINDEX\tKIND\tSTATE\tSTARTED\tDETAIL")
	for _, j := range out.Jobs {
		detail := jobProgress(j)
		if j.Error != "" {
			detail = j.Error
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", j.ID, j.Index, j.Kind, stateText(j.State), relativeTime(j.StartedAt), detail)
	}
	return tw.Flush()
}

func runJobsShow(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	follow, _ := cmd.Flags().GetBool("follow")
	var job jobs.Job
	if _, err := apiCall(cmd, http.MethodGet, "/admin/api/jobs/"+args[0], nil, &job); err != nil {
		return common.RuntimeError(cmd, err)
	}
	if follow {
		return followJobs(cmd, []jobs.Job{job})
	}
	if jsonMode {
		common.PrintJSON(job)
		return nil
	}
	printJob(job)
	return nil
}

func runJobsCancel(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	var job jobs.Job
	if _, err := apiCall(cmd, http.MethodDelete, "/admin/api/jobs/"+args[0], nil, &job); err != nil {
		return common.RuntimeError(cmd, err)
	}
	if jsonMode {
		common.PrintJSON(job)
		return nil
	}
	printJob(job)
	return nil
}

func printJob(j jobs.Job) {
	fmt.Printf(" Job:      %s\n", j.ID)
	fmt.Printf(" Index:    %s (%s)\n", j.Index, j.Kind)
	fmt.Printf(" State:    %s\n", stateText(j.State))
	if j.Kind == jobs.KindRebuild {
		fmt.Printf(" Progress: %s\n", jobProgress(j))
	}
	fmt.Printf(" Started:  %s\n", formatUnix(j.StartedAt))
	if j.FinishedAt > 0 {
		fmt.Printf(" Took:     %s\n", (time.Duration(j.FinishedAt-j.StartedAt) * time.Second).String())
	}
	if j.Error != "" {
		fmt.Printf(" Error:    %s\n", statusErrStyle.Render(j.Error))
	}
}

// followInterval paces job polling.
const followInterval = time.Second

// followJobs polls jobs until each finishes, drawing progress on stderr (or
// one JSON line per poll with --json), and fails unless all succeeded.
func followJobs(cmd *cobra.Command, list []jobs.Job) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	quiet, _ := cmd.Flags().GetBool("quiet")
	live := !jsonMode && !quiet && term.IsTerminal(int(os.Stderr.Fd()))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	for {
		running := 0
		for i := range list {
			if list[i].State == jobs.StateRunning {
				running++
			}
		}
		if jsonMode {
			for _, j := range list {
				common.PrintJSONLine(j)
			}
		} else if live && running > 0 {
			var parts []string
			for _, j := range list {
				parts = append(parts, fmt.Sprintf("%s: %s", j.Index, jobProgress(j)))
			}
			fmt.Fprintf(os.Stderr, "\r\033[K%s", strings.Join(parts, " | "))
		}
		if running == 0 {
			break
		}
		select {
		case <-ctx.Done():
			if live {
				fmt.Fprintln(os.Stderr)
			}
			return common.RuntimeError(cmd, errors.New("stopped following; the job keeps running (ncli relay jobs cancel <id> stops it)"))
		case <-time.After(followInterval):
		}
		for i := range list {
			if list[i].State != jobs.StateRunning {
				continue
			}
			var j jobs.Job
			if _, err := apiCall(cmd, http.MethodGet, "/admin/api/jobs/"+list[i].ID, nil, &j); err != nil {
				return common.RuntimeError(cmd, err)
			}
			list[i] = j
		}
	}
	if live {
		fmt.Fprintln(os.Stderr)
	}

	var failed []string
	for _, j := range list {
		if !jsonMode {
			fmt.Printf("%s %s %s: %s, %s\n", stateIcon(j.State), j.Index, j.Kind, stateText(j.State), jobProgress(j))
		}
		if j.State != jobs.StateSucceeded {
			msg := fmt.Sprintf("%s %s", j.Index, j.State)
			if j.Error != "" {
				msg += ": " + j.Error
			}
			failed = append(failed, msg)
		}
	}
	if len(failed) > 0 {
		return common.RuntimeError(cmd, errors.New(strings.Join(failed, "; ")))
	}
	return nil
}

func stateIcon(s jobs.State) string {
	if s == jobs.StateSucceeded {
		return statusOkStyle.Render("✔")
	}
	return statusErrStyle.Render("✘")
}
