package relay

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/relay/jobs"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// addIndexCommands adds index and jobs.
func addIndexCommands(cmd *cobra.Command) {
	indexCmd := &cobra.Command{
		Use:   "index",
		Short: "See and maintain a running relay's search, zap and stats indexes",
		Long: `The search index (Meilisearch profiles), the zap index (zap ranking) and
the stats index (each profile's zaps and follows, for scores and NIP-85)
are derived from stored events.

The relay keeps each one current on its own: an index that fell behind
(its feature was off while events were stored) catches up, one that can't
be trusted is rebuilt, and old data is freed — each as a job you can watch
with ncli relay jobs. Readers keep the previous data until new data is
ready. rebuild forces a rebuild; drop removes what an index whose feature
is off still holds.`,
		Example: `  ncli relay index list
  ncli relay index rebuild zaps
  ncli relay index drop stats --yes`,
		RunE: common.RequireSubcommand,
	}
	indexCmd.AddCommand(&cobra.Command{
		Use:     "list",
		Short:   "List indexes: state, entries, how far behind, and their jobs",
		Example: `  ncli relay index list`,
		Args:    common.NoArgs,
		RunE:    runIndexList,
	})

	rebuildCmd := &cobra.Command{
		Use:   "rebuild <search|zaps|stats|all>",
		Short: "Rebuild an index from the stored events",
		Long: `Builds a new index beside the live one, which keeps answering, then
swaps it in; writes are never held up. In a terminal it shows the plan,
then progress, then what changed; Ctrl-C stops watching, not the job.
With --json it returns the job at once (--wait or --follow to watch).
A rebuild of an index already rebuilding watches that one.

A search rebuild scores profiles from what the relay already knows; it
never re-fetches a NIP-05 or lightning address (ncli relay verify --refresh).`,
		Example: `  ncli relay index rebuild zaps
  ncli relay index rebuild all --detach
  ncli relay index rebuild stats --json --wait
  ncli relay index rebuild search --dry-run`,
		Args: common.ExactArgs(1),
		RunE: runIndexRebuild,
	}
	addFollowFlags(rebuildCmd)
	rebuildCmd.Flags().Bool("dry-run", false, "only show what a rebuild would do")
	rebuildCmd.Flags().Bool("fresh", false, "removed: every rebuild builds a new index and swaps it in")
	_ = rebuildCmd.Flags().MarkHidden("fresh")
	rebuildCmd.Flags().Bool("reverify", false, "removed: use ncli relay verify <pubkey> --refresh")
	_ = rebuildCmd.Flags().MarkHidden("reverify")
	indexCmd.AddCommand(rebuildCmd)

	dropCmd := &cobra.Command{
		Use:   "drop <search|zaps|stats|all>",
		Short: "Remove what an index whose feature is off still holds",
		Long: `Only an index whose feature is off can be dropped: an index in use is kept
current, so rebuild it instead. The stored events are kept, and the index
is rebuilt from them if its feature is turned on again. For search it
removes the Meilisearch index and the profiles queued for it. Asks for
confirmation, showing what it removes; --yes skips it, and is required
with --json or no terminal.`,
		Example: `  ncli relay index drop zaps
  ncli relay index drop all --yes`,
		Args: common.ExactArgs(1),
		RunE: runIndexDrop,
	}
	addFollowFlags(dropCmd)
	dropCmd.Flags().Bool("yes", false, "skip the confirmation prompt")
	indexCmd.AddCommand(dropCmd)
	cmd.AddCommand(indexCmd)

	jobsCmd := &cobra.Command{
		Use:     "jobs",
		Short:   "List, watch or cancel a running relay's index jobs",
		Example: `  ncli relay jobs list`,
		RunE:    common.RequireSubcommand,
	}
	listCmd := &cobra.Command{
		Use:     "list",
		Short:   "List running and recent jobs, newest first",
		Example: `  ncli relay jobs list --index zaps`,
		Args:    common.NoArgs,
		RunE:    runJobsList,
	}
	listCmd.Flags().String("index", "", "only this index's jobs")
	jobsCmd.AddCommand(listCmd)
	showCmd := &cobra.Command{
		Use:     "show <id>",
		Short:   "Show one job",
		Example: `  ncli relay jobs show 3f9a1c2b7d4e --follow`,
		Args:    common.ExactArgs(1),
		RunE:    runJobsShow,
	}
	showCmd.Flags().Bool("follow", false, "watch it until it ends (NDJSON with --json)")
	showCmd.Flags().Bool("wait", false, "wait until it ends and print it")
	showCmd.Flags().Duration("timeout", 0, "give up waiting after this long (0: no limit)")
	jobsCmd.AddCommand(showCmd)
	cancelCmd := &cobra.Command{
		Use:   "cancel <id>",
		Short: "Stop a running job",
		Long: `Stops the job and waits until it has; a rebuild stopped this way is
discarded, the live index is untouched. A job that already ended is left
as it is.`,
		Example: `  ncli relay jobs cancel 3f9a1c2b7d4e`,
		Args:    common.ExactArgs(1),
		RunE:    runJobsCancel,
	}
	cancelCmd.Flags().Bool("detach", false, "return once the job was asked to stop")
	jobsCmd.AddCommand(cancelCmd)
	cmd.AddCommand(jobsCmd)
}

func addFollowFlags(c *cobra.Command) {
	c.Flags().Bool("detach", false, "return once the job started (text mode watches it by default)")
	c.Flags().Bool("wait", false, "wait until the job ends and print it (for --json)")
	c.Flags().Bool("follow", false, "watch the job until it ends (NDJSON with --json)")
	c.Flags().Duration("timeout", 0, "give up waiting after this long (0: no limit); the job keeps running")
}

/////////////////////////////////////////////////////////////////////
// list
/////////////////////////////////////////////////////////////////////

func fetchIndexes(cmd *cobra.Command) ([]jobs.IndexInfo, error) {
	var out struct {
		Indexes []jobs.IndexInfo `json:"indexes"`
	}
	if _, err := apiCall(cmd, http.MethodGet, "/admin/api/indexes", nil, &out); err != nil {
		return nil, err
	}
	if out.Indexes == nil {
		out.Indexes = []jobs.IndexInfo{}
	}
	return out.Indexes, nil
}

func runIndexList(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	list, err := fetchIndexes(cmd)
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	if jsonMode {
		common.PrintJSON(map[string]any{"indexes": list})
		return nil
	}
	renderIndexes(os.Stdout, list, time.Now())
	return nil
}

// renderIndexes prints the index table, then a line for each index that
// isn't simply ready: what is happening, or what to do.
func renderIndexes(w io.Writer, list []jobs.IndexInfo, now time.Time) {
	rows := make([][]cell, 0, len(list))
	for _, ix := range list {
		entries, behind := jobs.Count(ix.Entries), "-"
		if !ix.Enabled && !ix.HasData {
			entries = "-"
		}
		if ix.Enabled && ix.State != jobs.StateUnreachable {
			behind = jobs.Count(ix.Behind)
		}
		last := cell{text: "-"}
		if j := ix.Last; j != nil {
			last = cell{text: fmt.Sprintf("%s %s %s", describeKind(j.Kind), j.State, relativeTimeAt(j.FinishedAt, now)), style: jobStyle(j.State)}
		}
		rows = append(rows, []cell{{text: ix.Name}, {text: strings.ReplaceAll(ix.State, "_", " "), style: indexStyle(ix.State)}, {text: entries}, {text: behind}, last})
	}
	writeTable(w, []string{"INDEX", "STATE", "ENTRIES", "BEHIND", "LAST JOB"}, rows)
	for _, ix := range list {
		if note := indexNote(ix, now); note != "" {
			_, _ = fmt.Fprintf(w, "  → %s: %s\n", ix.Name, note)
		}
	}
}

// cell is a table cell: its text, and the style it is shown in.
type cell struct {
	text  string
	style func(...string) string
}

// writeTable lines columns up by their plain text, then styles each cell,
// so colors never shift a column.
func writeTable(w io.Writer, header []string, rows [][]cell) {
	widths := make([]int, len(header))
	for i, h := range header {
		widths[i] = utf8.RuneCountInString(h)
	}
	for _, r := range rows {
		for i, c := range r {
			widths[i] = max(widths[i], utf8.RuneCountInString(c.text))
		}
	}
	line := func(cells []cell) {
		var b strings.Builder
		for i, c := range cells {
			text := c.text
			if c.style != nil {
				text = c.style(text)
			}
			b.WriteString(text)
			if i < len(cells)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-utf8.RuneCountInString(c.text)+2))
			}
		}
		_, _ = fmt.Fprintln(w, b.String())
	}
	head := make([]cell, len(header))
	for i, h := range header {
		head[i] = cell{text: h}
	}
	line(head)
	for _, r := range rows {
		line(r)
	}
}

func indexStyle(s string) func(...string) string {
	switch s {
	case jobs.StateReady:
		return statusOkStyle.Render
	case jobs.StateDisabled, jobs.StateEmpty:
		return nil
	case jobs.StateUnreachable, jobs.StateNeedsRebuild:
		return statusErrStyle.Render
	}
	return statusWaitStyle.Render
}

func stateLabel(s string) string {
	label := strings.ReplaceAll(s, "_", " ")
	if style := indexStyle(s); style != nil {
		return style(label)
	}
	return label
}

func jobStyle(s jobs.State) func(...string) string {
	switch s {
	case jobs.StateSucceeded:
		return statusOkStyle.Render
	case jobs.StateFailed, jobs.StateInterrupted:
		return statusErrStyle.Render
	}
	return statusWaitStyle.Render
}

func describeKind(k jobs.Kind) string {
	switch k {
	case jobs.KindCatchUp:
		return "catch-up"
	case jobs.KindReap:
		return "free"
	}
	return string(k)
}

// indexNote says what an index is doing or needs, "" when it is ready and
// idle.
func indexNote(ix jobs.IndexInfo, now time.Time) string {
	switch {
	case ix.Running != nil:
		j := *ix.Running
		who := "started by the relay"
		if j.Trigger == jobs.TriggerAdmin {
			who = "started by an admin"
		}
		s := fmt.Sprintf("%s %s (job %s, %s", describeKind(j.Kind), progressText(j, now), j.ID, who)
		if j.Reason != "" {
			s += ": " + j.Reason
		}
		return s + ")"
	case ix.State == jobs.StateUnreachable:
		return ix.Reason + " (" + ix.Error + ")"
	case ix.Action != "" && ix.Parked != "":
		return fmt.Sprintf("%s needed (%s); parked: %s", describeKind(ix.Action), ix.Reason, ix.Parked)
	case ix.Action != "" && ix.RetryAt > 0:
		return fmt.Sprintf("%s needed (%s); the last try failed, retrying %s", describeKind(ix.Action), ix.Reason, relativeTimeAt(ix.RetryAt, now))
	case ix.Action != "":
		return fmt.Sprintf("%s needed (%s); the relay starts it on its own", describeKind(ix.Action), ix.Reason)
	case ix.Hint != "":
		return ix.Hint
	case !ix.Enabled:
		return ix.Unavailable
	}
	return ""
}

// relativeTimeAt is relativeTime against a fixed now, future included.
func relativeTimeAt(ts int64, now time.Time) string {
	if ts == 0 {
		return "-"
	}
	d := time.Unix(ts, 0).Sub(now)
	future := d > 0
	if !future {
		d = -d
	}
	var s string
	switch {
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		s = fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		s = fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		s = fmt.Sprintf("%dh", int(d.Hours()))
	default:
		s = fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	if future {
		return "in " + s
	}
	return s + " ago"
}

// progressText is a running job's position: percent, counts and time left.
func progressText(j jobs.Job, now time.Time) string {
	switch {
	case j.Phase == "waiting":
		return "waiting for another index job to finish"
	case j.Phase == "cancelling":
		return "cancelling"
	case strings.HasPrefix(j.Phase, "waiting for"):
		return j.Phase
	}
	var parts []string
	if j.Total > 0 {
		pct := min(float64(j.Done)/float64(j.Total)*100, 100)
		parts = append(parts, fmt.Sprintf("%.0f%%", pct))
		if left := timeLeft(j, now); left > 0 {
			parts = append(parts, "~"+left.String()+" left")
		}
	}
	if j.Applied > 0 {
		parts = append(parts, jobs.Count(j.Applied)+" applied")
	}
	if j.Phase != "" && j.Phase != "scan" {
		parts = append([]string{j.Phase}, parts...)
	}
	if len(parts) == 0 {
		return "starting"
	}
	return strings.Join(parts, ", ")
}

func timeLeft(j jobs.Job, now time.Time) time.Duration {
	if j.Done <= 0 || j.Total <= j.Done {
		return 0
	}
	elapsed := now.Sub(time.Unix(j.StartedAt, 0))
	left := time.Duration(float64(elapsed) * float64(j.Total-j.Done) / float64(j.Done))
	if left < time.Second {
		return 0
	}
	return left.Round(time.Second)
}

/////////////////////////////////////////////////////////////////////
// rebuild and drop
/////////////////////////////////////////////////////////////////////

// targets resolves an index argument; "all" is every index kind applies
// to, with a reason for each one skipped.
func targets(cmd *cobra.Command, arg string, kind jobs.Kind) (names []string, skipped []skip, err error) {
	if arg != "all" {
		return []string{arg}, nil, nil
	}
	list, err := fetchIndexes(cmd)
	if err != nil {
		return nil, nil, err
	}
	for _, ix := range list {
		switch {
		case kind == jobs.KindRebuild && !ix.Enabled:
			skipped = append(skipped, skip{ix.Name, ix.Unavailable})
		case kind == jobs.KindDrop && ix.Enabled:
			skipped = append(skipped, skip{ix.Name, "in use"})
		case kind == jobs.KindDrop && !ix.HasData && ix.Retired == 0:
			skipped = append(skipped, skip{ix.Name, "holds nothing"})
		default:
			names = append(names, ix.Name)
		}
	}
	return names, skipped, nil
}

type skip struct {
	Index  string `json:"index"`
	Reason string `json:"reason"`
}

func runIndexRebuild(cmd *cobra.Command, args []string) error {
	if fresh, _ := cmd.Flags().GetBool("fresh"); fresh {
		return common.UsageError(cmd, errors.New("--fresh was removed: every rebuild builds a new index and swaps it in"))
	}
	if reverify, _ := cmd.Flags().GetBool("reverify"); reverify {
		return common.UsageError(cmd, errors.New("--reverify was removed: a rebuild never re-fetches NIP-05 or lightning addresses; use ncli relay verify <pubkey> --refresh"))
	}
	return runIndexAction(cmd, args[0], jobs.KindRebuild)
}

func runIndexDrop(cmd *cobra.Command, args []string) error {
	return runIndexAction(cmd, args[0], jobs.KindDrop)
}

func runIndexAction(cmd *cobra.Command, arg string, kind jobs.Kind) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	names, skipped, err := targets(cmd, arg, kind)
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	path := func(name string) string {
		return "/admin/api/indexes/" + url.PathEscape(name) + "/" + string(kind)
	}

	// The plan both validates (a drop of an index in use is refused before
	// any prompt) and says what will happen.
	plans := make([]jobs.Plan, 0, len(names))
	for _, name := range names {
		var p jobs.Plan
		if _, err := apiCall(cmd, http.MethodPost, path(name), map[string]bool{"dry_run": true}, &p); err != nil {
			return common.RuntimeError(cmd, err)
		}
		plans = append(plans, p)
	}
	if dryRun {
		if jsonMode {
			common.PrintJSON(map[string]any{"plans": plans, "skipped": nonNilSkips(skipped)})
			return nil
		}
		for _, p := range plans {
			fmt.Println(planText(p))
		}
		printSkipped(os.Stdout, skipped)
		return nil
	}
	if !jsonMode {
		printSkipped(os.Stderr, skipped)
	}
	if len(names) == 0 {
		if jsonMode {
			common.PrintJSON(map[string]any{"jobs": []jobs.Job{}, "skipped": nonNilSkips(skipped)})
			return nil
		}
		fmt.Printf("nothing to %s\n", kind)
		return nil
	}
	if kind == jobs.KindDrop {
		if err := confirmDrop(cmd, plans); err != nil {
			return err
		}
	}

	var started []jobs.Job
	for i, name := range names {
		if !jsonMode {
			fmt.Fprintln(os.Stderr, planText(plans[i]))
		}
		var job jobs.Job
		if _, err := apiCall(cmd, http.MethodPost, path(name), nil, &job); err != nil {
			return common.RuntimeError(cmd, err)
		}
		if !jsonMode && job.Noop {
			fmt.Fprintln(os.Stderr, job.Summary)
		}
		started = append(started, job)
	}
	return watchJobs(cmd, started, arg == "all", skipped)
}

func nonNilSkips(s []skip) []skip {
	if s == nil {
		return []skip{}
	}
	return s
}

func printSkipped(w io.Writer, skipped []skip) {
	for _, s := range skipped {
		_, _ = fmt.Fprintf(w, "%s: skipped (%s)\n", s.Index, s.Reason)
	}
}

// planText says, in a line, what a rebuild or drop will do.
func planText(p jobs.Plan) string {
	if p.Kind == jobs.KindDrop {
		return fmt.Sprintf("%s: drop — %s", p.Index, p.Effect)
	}
	s := fmt.Sprintf("%s: rebuild — reads %s stored events, %s entries now", p.Index, jobs.Count(p.Scan), jobs.Count(p.Entries))
	if p.Reason != "" && p.State != jobs.StateReady {
		s += " (" + p.Reason + ")"
	}
	return s + "; " + p.Effect
}

func confirmDrop(cmd *cobra.Command, plans []jobs.Plan) error {
	if yes, _ := cmd.Flags().GetBool("yes"); yes {
		return nil
	}
	jsonMode, _ := cmd.Flags().GetBool("json")
	var parts []string
	for _, p := range plans {
		parts = append(parts, fmt.Sprintf("%s (%s entries)", p.Index, jobs.Count(p.Entries)))
	}
	if jsonMode || !term.IsTerminal(int(os.Stdin.Fd())) {
		return common.UsageError(cmd, fmt.Errorf("refusing to drop %s without --yes in a non-interactive session", strings.Join(parts, ", ")))
	}
	prompt := fmt.Sprintf("Drop %s? The stored events are kept and the index is rebuilt from them if turned on again. [y/N] ", strings.Join(parts, ", "))
	if !common.PromptYesNo(bufio.NewReader(os.Stdin), prompt) {
		return common.RuntimeError(cmd, errors.New("aborted; nothing was dropped"))
	}
	return nil
}

/////////////////////////////////////////////////////////////////////
// watching jobs
/////////////////////////////////////////////////////////////////////

// watchJobs reports started jobs as the flags ask: at once (--detach, or
// --json without --wait/--follow), or once they end, with live progress in
// a terminal.
func watchJobs(cmd *cobra.Command, list []jobs.Job, many bool, skipped []skip) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	detach, _ := cmd.Flags().GetBool("detach")
	wait, _ := cmd.Flags().GetBool("wait")
	follow, _ := cmd.Flags().GetBool("follow")
	if detach || (jsonMode && !wait && !follow) {
		if jsonMode {
			printJobs(list, many, skipped)
			return nil
		}
		for _, j := range list {
			if !j.Noop {
				fmt.Printf("%s %s\n", statusOkStyle.Render("▶"), j.Summary)
			}
			if j.ID != "" && !j.Ended() {
				fmt.Fprintf(os.Stderr, "  watch: ncli relay jobs show %s --follow · stop: ncli relay jobs cancel %s\n", j.ID, j.ID)
			}
		}
		return nil
	}
	final, err := followJobs(cmd, list)
	if err != nil || final == nil {
		return err
	}
	if jsonMode && !follow {
		printJobs(final, many, skipped)
	}
	return jobsOutcome(cmd, final)
}

func printJobs(list []jobs.Job, many bool, skipped []skip) {
	if many {
		common.PrintJSON(map[string]any{"jobs": list, "skipped": nonNilSkips(skipped)})
		return
	}
	common.PrintJSON(list[0])
}

// jobsOutcome fails unless every job succeeded.
func jobsOutcome(cmd *cobra.Command, list []jobs.Job) error {
	var failed []string
	input := ""
	for _, j := range list {
		if j.State != jobs.StateSucceeded {
			failed = append(failed, j.Summary)
			input = j.ID
		}
	}
	if len(failed) == 0 {
		return nil
	}
	silenceUsage(cmd)
	return &common.CLIError{Err: errors.New(strings.Join(failed, "; ")), Code: common.CodeInternal, Input: input}
}

func silenceUsage(cmd *cobra.Command) {
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
}

// followJobs polls jobs until each ends. In a terminal it redraws one
// progress line per job on stderr; without one, it prints a line every 5s;
// with --json --follow, one JSON line per change and a last one per job.
// Text mode prints each job's summary as it ends.
func followJobs(cmd *cobra.Command, list []jobs.Job) ([]jobs.Job, error) {
	jsonMode, _ := cmd.Flags().GetBool("json")
	quiet, _ := cmd.Flags().GetBool("quiet")
	follow, _ := cmd.Flags().GetBool("follow")
	timeout, _ := cmd.Flags().GetDuration("timeout")
	stream := jsonMode && follow
	live := !jsonMode && !quiet && term.IsTerminal(int(os.Stderr.Fd()))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var deadline <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		deadline = t.C
	}

	reported := map[int]bool{}
	lastLine := map[int]string{}
	lastPrint := time.Time{}
	drawn := 0
	for {
		running := 0
		var lines []string
		for i, j := range list {
			if !j.Ended() {
				running++
				lines = append(lines, fmt.Sprintf("%s: %s %s", j.Index, describeKind(j.Kind), progressText(j, time.Now())))
			}
			if stream {
				if line := fmt.Sprintf("%s|%d|%d|%s", j.State, j.Done, j.Applied, j.Phase); line != lastLine[i] {
					lastLine[i] = line
					common.PrintJSONLine(j)
				}
			}
			if j.Ended() && !reported[i] {
				reported[i] = true
				if live {
					clearLines(drawn)
					drawn = 0
				}
				if !jsonMode {
					fmt.Printf("%s %s\n", stateIcon(j.State), j.Summary)
				}
			}
		}
		if running == 0 {
			if live {
				clearLines(drawn)
			}
			return list, nil
		}
		switch {
		case live:
			clearLines(drawn)
			for _, l := range lines {
				fmt.Fprintln(os.Stderr, l)
			}
			drawn = len(lines)
		case !jsonMode && !quiet && time.Since(lastPrint) >= 5*time.Second:
			lastPrint = time.Now()
			for _, l := range lines {
				fmt.Fprintln(os.Stderr, l)
			}
		}
		select {
		case <-ctx.Done():
			if live {
				clearLines(drawn)
			}
			for _, j := range list {
				if !j.Ended() {
					fmt.Fprintf(os.Stderr, "stopped watching; job %s keeps running · watch: ncli relay jobs show %s --follow · stop: ncli relay jobs cancel %s\n", j.ID, j.ID, j.ID)
				}
			}
			if !jsonMode {
				return nil, nil
			}
			silenceUsage(cmd)
			return list, &common.CLIError{Err: errors.New("stopped watching; the job keeps running"), Code: common.CodeConflict, Input: firstRunning(list)}
		case <-deadline:
			if live {
				clearLines(drawn)
			}
			silenceUsage(cmd)
			return list, &common.CLIError{Err: fmt.Errorf("still running after %s; the job keeps running", timeout), Code: common.CodeConflict, Input: firstRunning(list)}
		case <-time.After(followInterval):
		}
		for i := range list {
			if list[i].Ended() || list[i].ID == "" {
				continue
			}
			var j jobs.Job
			if _, err := apiPoll(cmd, http.MethodGet, "/admin/api/jobs/"+list[i].ID, nil, &j); err != nil {
				return list, common.RuntimeError(cmd, err)
			}
			list[i] = j
		}
	}
}

// followInterval paces job polling.
const followInterval = time.Second

func firstRunning(list []jobs.Job) string {
	for _, j := range list {
		if !j.Ended() {
			return j.ID
		}
	}
	return ""
}

func clearLines(n int) {
	for range n {
		fmt.Fprint(os.Stderr, "\033[1A\033[2K")
	}
}

func stateIcon(s jobs.State) string {
	if s == jobs.StateSucceeded {
		return statusOkStyle.Render("✔")
	}
	return statusErrStyle.Render("✘")
}

func stateText(s jobs.State) string { return jobStyle(s)(string(s)) }

/////////////////////////////////////////////////////////////////////
// jobs
/////////////////////////////////////////////////////////////////////

func runJobsList(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	index, _ := cmd.Flags().GetString("index")
	path := "/admin/api/jobs"
	if index != "" {
		path += "?index=" + url.QueryEscape(index)
	}
	var out struct {
		Jobs []jobs.Job `json:"jobs"`
	}
	if _, err := apiCall(cmd, http.MethodGet, path, nil, &out); err != nil {
		return common.RuntimeError(cmd, err)
	}
	if out.Jobs == nil {
		out.Jobs = []jobs.Job{}
	}
	if jsonMode {
		common.PrintJSON(out)
		return nil
	}
	renderJobs(os.Stdout, out.Jobs, time.Now())
	return nil
}

func renderJobs(w io.Writer, list []jobs.Job, now time.Time) {
	if len(list) == 0 {
		_, _ = fmt.Fprintln(w, " (no jobs)")
		return
	}
	rows := make([][]cell, 0, len(list))
	for _, j := range list {
		took, summary := "-", j.Summary
		if j.Ended() {
			took = tookText(j)
		} else {
			summary = progressText(j, now)
		}
		rows = append(rows, []cell{{text: j.ID}, {text: j.Index}, {text: describeKind(j.Kind)}, {text: string(j.Trigger)},
			{text: string(j.State), style: jobStyle(j.State)}, {text: relativeTimeAt(j.StartedAt, now)}, {text: took}, {text: summary}})
	}
	writeTable(w, []string{"ID", "INDEX", "KIND", "BY", "STATE", "STARTED", "TOOK", "SUMMARY"}, rows)
}

func tookText(j jobs.Job) string {
	d := time.Duration(j.DurationMs) * time.Millisecond
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return d.Round(100 * time.Millisecond).String()
}

func runJobsShow(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	follow, _ := cmd.Flags().GetBool("follow")
	wait, _ := cmd.Flags().GetBool("wait")
	var job jobs.Job
	if _, err := apiCall(cmd, http.MethodGet, "/admin/api/jobs/"+url.PathEscape(args[0]), nil, &job); err != nil {
		return common.RuntimeError(cmd, err)
	}
	if (follow || wait) && !job.Ended() {
		final, err := followJobs(cmd, []jobs.Job{job})
		if err != nil || final == nil {
			return err
		}
		job = final[0]
		if !jsonMode || follow {
			return jobsOutcome(cmd, final)
		}
		common.PrintJSON(job)
		return jobsOutcome(cmd, final)
	}
	if jsonMode {
		common.PrintJSON(job)
		return nil
	}
	renderJob(os.Stdout, job, time.Now())
	return nil
}

func renderJob(w io.Writer, j jobs.Job, now time.Time) {
	row := func(label, value string) { _, _ = fmt.Fprintf(w, " %-9s %s\n", label+":", value) }
	row("Job", j.ID)
	row("Index", fmt.Sprintf("%s (%s, started by %s)", j.Index, describeKind(j.Kind), j.Trigger))
	if j.Reason != "" {
		row("Why", j.Reason)
	}
	row("State", stateText(j.State))
	if j.Ended() {
		row("Result", j.Summary)
		row("Entries", fmt.Sprintf("%s → %s", jobs.Count(j.EntriesBefore), jobs.Count(j.EntriesAfter)))
		row("Took", tookText(j))
	} else {
		row("Progress", progressText(j, now))
	}
	row("Started", formatUnix(j.StartedAt))
	if j.Error != "" {
		row("Error", statusErrStyle.Render(j.Error))
	}
}

func runJobsCancel(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	detach, _ := cmd.Flags().GetBool("detach")
	var job jobs.Job
	if _, err := apiCall(cmd, http.MethodDelete, "/admin/api/jobs/"+url.PathEscape(args[0]), nil, &job); err != nil {
		return common.RuntimeError(cmd, err)
	}
	if job.Noop || detach {
		if jsonMode {
			common.PrintJSON(job)
			return nil
		}
		if job.Noop {
			fmt.Println(job.Summary)
		} else {
			fmt.Printf("cancelling job %s (%s %s)\n", job.ID, job.Index, describeKind(job.Kind))
		}
		return nil
	}
	if !jsonMode {
		fmt.Fprintf(os.Stderr, "cancelling job %s (%s %s)…\n", job.ID, job.Index, describeKind(job.Kind))
	}
	var final jobs.Job
	if _, err := apiCall(cmd, http.MethodGet, "/admin/api/jobs/"+url.PathEscape(job.ID)+"?wait=30", nil, &final); err != nil {
		return common.RuntimeError(cmd, err)
	}
	if jsonMode {
		common.PrintJSON(final)
	} else if final.Ended() {
		fmt.Printf("%s %s\n", stateIcon(final.State), final.Summary)
	} else {
		fmt.Printf("job %s is still stopping; see ncli relay jobs show %s\n", final.ID, final.ID)
	}
	return nil
}
