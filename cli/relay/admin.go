package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip11"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FAFAFA")).
			Background(lipgloss.Color("#7D56F4")).
			Padding(0, 1).
			MarginBottom(1)

	headerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7D56F4")).
			Bold(true).
			Underline(true)

	statusOkStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#04B575"))
	statusWaitStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD100"))
	statusErrStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444"))
)

// addRemoteAdminCommands adds "stats", "reindex", and "clear" directly onto
// cmd (the relay root command) -- NIP-98 authenticated HTTP requests to a
// relay that's already running. These used to nest under a "relay admin"
// group, but that was the only grouping under "relay" and didn't cover
// anything not already implied by the subcommand names themselves, so it
// was flattened away: "ncli relay admin stats" -> "ncli relay stats".
func addRemoteAdminCommands(cmd *cobra.Command) {
	statsCmd := &cobra.Command{
		Use:   "stats",
		Short: "Display live relay metrics and worker status",
		Long:  `Fetch and display live reindexer and verification worker metrics from a running relay.`,
		RunE:  runStats,
	}
	cmd.AddCommand(statsCmd)

	reindexCmd := &cobra.Command{
		Use:   "reindex",
		Short: "Trigger a reindex on the running relay",
		Long:  `Trigger a reindex job on a running relay without restarting it.`,
		RunE:  common.RequireSubcommand,
	}
	reindexCmd.AddCommand(&cobra.Command{Use: "search", Short: "Reindex profiles to the search index", Long: `Trigger a search-index reindex on the running relay.`, RunE: runReindexSearch})
	reindexCmd.AddCommand(&cobra.Command{Use: "zaps", Short: "Reindex zap stats", Long: `Trigger a zap-stats reindex on the running relay.`, RunE: runReindexZaps})
	cmd.AddCommand(reindexCmd)

	clearCmd := &cobra.Command{
		Use:   "clear",
		Short: "Clear indexes on the running relay",
		Long:  `Clear an index on a running relay without restarting it.`,
		RunE:  common.RequireSubcommand,
	}
	clearCmd.AddCommand(&cobra.Command{Use: "search", Short: "Delete all profiles from the search index", Long: `Delete every profile from the running relay's search index.`, RunE: runClearSearch})
	clearCmd.AddCommand(&cobra.Command{Use: "zaps", Short: "Delete all zap counters", Long: `Delete every zap counter from the running relay.`, RunE: runClearZaps})
	cmd.AddCommand(clearCmd)

	addMembershipAdminCommands(cmd)
}

// addMembershipAdminCommands adds "members", "invites", and "roles" onto
// cmd -- the NIP-43 membership admin surface. Same NIP-98-over-HTTP shape
// as stats/reindex/clear above; split into its own function only because
// there are three parent command groups' worth of children, not because
// the underlying mechanism differs at all.
func addMembershipAdminCommands(cmd *cobra.Command) {
	membersCmd := &cobra.Command{
		Use:   "members",
		Short: "Manage NIP-43 relay membership",
		Long:  `Manage NIP-43 relay membership (list/show/add/remove) on a running relay.`,
		RunE:  common.RequireSubcommand,
	}
	membersCmd.AddCommand(&cobra.Command{
		Use: "list", Short: "List all members",
		Long: `List every NIP-43 member currently enrolled on the running relay.`,
		RunE: runMembersList,
	})
	membersCmd.AddCommand(&cobra.Command{
		Use: "show <pubkey>", Short: "Show one member's record",
		Long: `Show a single pubkey's NIP-43 membership record on the running relay.`,
		Args: cobra.ExactArgs(1), RunE: runMembersShow,
	})
	membersAddCmd := &cobra.Command{
		Use: "add <pubkey>", Short: "Enroll a pubkey as a member",
		Long: `Enroll a pubkey as a NIP-43 member directly -- the admin bypass of the
self-service invite-code join flow, no invite claim required.`,
		Args: cobra.ExactArgs(1), RunE: runMembersAdd,
	}
	membersAddCmd.Flags().StringArray("role", nil, "role id to assign (repeatable)")
	membersCmd.AddCommand(membersAddCmd)
	membersCmd.AddCommand(&cobra.Command{
		Use: "remove <pubkey>", Short: "Remove a member",
		Long: `Remove a pubkey's NIP-43 membership from the running relay.`,
		Args: cobra.ExactArgs(1), RunE: runMembersRemove,
	})
	cmd.AddCommand(membersCmd)

	invitesCmd := &cobra.Command{
		Use:   "invites",
		Short: "Manage NIP-43 invite codes",
		Long:  `Manage NIP-43 invite codes (create/list/revoke) on a running relay.`,
		RunE:  common.RequireSubcommand,
	}
	invitesCreateCmd := &cobra.Command{
		Use: "create", Short: "Issue a new invite code",
		Long: `Issue a new NIP-43 invite code on the running relay -- for handing out
out-of-band (a signup email, a Discord invite flow) without the invitee
needing a working Nostr client yet.`,
		RunE: runInvitesCreate,
	}
	invitesCreateCmd.Flags().Duration("ttl", 0, "how long the code stays valid (default: relay's configured default)")
	invitesCreateCmd.Flags().Int("max-uses", 0, "maximum number of times the code may be used (0 = unlimited)")
	invitesCreateCmd.Flags().StringArray("role", nil, "role id granted on join (repeatable)")
	invitesCmd.AddCommand(invitesCreateCmd)
	invitesCmd.AddCommand(&cobra.Command{
		Use: "list", Short: "List all invite codes",
		Long: `List every currently-stored NIP-43 invite code on the running relay.`,
		RunE: runInvitesList,
	})
	invitesCmd.AddCommand(&cobra.Command{
		Use: "revoke <code>", Short: "Revoke an invite code",
		Long: `Revoke a NIP-43 invite code on the running relay, so it can no longer be used to join.`,
		Args: cobra.ExactArgs(1), RunE: runInvitesRevoke,
	})
	cmd.AddCommand(invitesCmd)

	rolesCmd := &cobra.Command{
		Use:   "roles",
		Short: "Manage NIP-43 role definitions",
		Long:  `Manage NIP-43 role definitions (list/create) on a running relay.`,
		RunE:  common.RequireSubcommand,
	}
	rolesCmd.AddCommand(&cobra.Command{
		Use: "list", Short: "List all role definitions",
		Long: `List every NIP-43 role definition on the running relay.`,
		RunE: runRolesList,
	})
	rolesCreateCmd := &cobra.Command{
		Use: "create <id>", Short: "Create a role definition",
		Long: `Create a NIP-43 role definition on the running relay. NIP-43 defines no
"delete" event for a role -- once created, a role id can only be
superseded (re-run "create" with the same id and new label/description/
color/order), never truly removed.`,
		Args: cobra.ExactArgs(1), RunE: runRolesCreate,
	}
	rolesCreateCmd.Flags().String("label", "", "human-readable role label")
	rolesCreateCmd.Flags().String("description", "", "role description")
	rolesCreateCmd.Flags().Int("color", 0, "hue 0-360")
	rolesCreateCmd.Flags().Int("order", 0, "sort order")
	rolesCmd.AddCommand(rolesCreateCmd)
	cmd.AddCommand(rolesCmd)
}

// getAdminConfig reads the remote-admin connection/auth settings from
// viper's already-loaded config state -- root's InitConfig (cli/ncli/root.go)
// loads it once before any command's RunE runs, so reloading it here on
// every request would just redundantly re-read the same file. Logs "using
// config file" itself (mirroring initConfig in command.go) since
// stats/reindex/clear have no PreRunE of their own to do it -- cobra only
// runs a command's own (non-persistent) PreRunE, and a PersistentPreRunE
// here would shadow rootCmd's own (which does the actual config loading).
func getAdminConfig() (string, int, string, error) {
	if used := viper.ConfigFileUsed(); used != "" {
		ev := log.Info().Str("config", used)
		if common.ActiveRelayContext != "" {
			ev = ev.Str("relay_context", common.ActiveRelayContext)
		}
		ev.Msg("using config file")
	}

	port := viper.GetInt("port")
	if port == 0 {
		port = 5500 // Default
	}

	privKey := viper.GetString("nip11.privkey")
	if privKey == "" {
		return "", 0, "", errors.New("nip11.privkey is required to sign these requests")
	}

	pubKey := viper.GetString("nip11.pubkey")
	if pubKey == "" {
		// Try to derive it if missing from config but privkey exists
		derived, err := nip11.DerivePubKey(privKey)
		if err == nil {
			pubKey = derived
		}
	}

	return privKey, port, pubKey, nil
}

// adminRequest issues a NIP-98-authenticated admin HTTP request with no
// body -- the shape every pre-existing stats/reindex/clear caller needs.
func adminRequest(method, path string) (map[string]interface{}, error) {
	return adminRequestBody(method, path, nil)
}

// adminRequestBody is adminRequest's superset: body, if non-nil, is
// JSON-encoded and sent as the request payload (used by members/invites/
// roles admin commands that POST a body, e.g. "members add"). Kept as one
// function rather than a second HTTP client -- every admin command, with or
// without a body, shares the same signing/timeout/error-classification
// path.
func adminRequestBody(method, path string, body interface{}) (map[string]interface{}, error) {
	privKey, port, _, err := getAdminConfig()
	if err != nil {
		// nip11.privkey missing from config -- a missing-required-config
		// mistake, classified here (rather than at each of stats/reindex/
		// clear's own RunE) since wrapCLIError keeps this code even when
		// the caller re-wraps it via common.RuntimeError.
		return nil, &common.CLIError{Err: err, Code: common.CodeUsage}
	}

	url := fmt.Sprintf("http://localhost:%d%s", port, path)

	header, err := common.GenerateNIP98Header(privKey, url, method)
	if err != nil {
		// Malformed nip11.privkey -- input left blank, it's private-key
		// material.
		return nil, &common.CLIError{Err: fmt.Errorf("failed to sign admin request: %w", err), Code: common.CodeInvalidInput}
	}

	var bodyReader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to encode request body: %w", err)
		}
		bodyReader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", header)
	if bodyReader != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, &common.CLIError{
			Err:   fmt.Errorf("request failed (is the relay running on port %d?): %w", port, err),
			Code:  common.CodeNetwork,
			Input: url,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		code := common.CodeInternal
		switch {
		case resp.StatusCode == http.StatusBadRequest:
			code = common.CodeInvalidInput
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			code = common.CodeAuth
		case resp.StatusCode == http.StatusNotFound:
			code = common.CodeNotFound
		case resp.StatusCode == http.StatusConflict:
			code = common.CodeConflict
		case resp.StatusCode == http.StatusNotImplemented:
			// The relay is reachable and the request itself is well-formed,
			// but the feature it targets isn't turned on (e.g. NIP-43
			// membership.enabled is false) -- an operator setup issue, not
			// something retrying will ever fix on its own.
			code = common.CodeUsage
		case resp.StatusCode >= 500:
			// A relay-side failure, not this request's fault -- worth
			// retrying once the relay recovers.
			code = common.CodeNetwork
		}
		return nil, &common.CLIError{
			Err:   fmt.Errorf("admin error (%d): %s", resp.StatusCode, string(respBody)),
			Code:  code,
			Input: url,
		}
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return result, nil
}

func runStats(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")

	stats, err := adminRequest("GET", "/admin/worker/stats")
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	if jsonMode {
		common.PrintJSON(stats)
		return nil
	}

	fmt.Println(titleStyle.Render(" ncli relay: stats "))
	fmt.Printf("\n%s\n", headerStyle.Render("Reindexers"))
	renderReindexStats("Search", stats["search_reindex"])
	renderReindexStats("Zaps", stats["zaps_reindex"])

	fmt.Printf("\n%s\n", headerStyle.Render("Verification Worker"))
	if wStats, ok := stats["verification_worker"].(map[string]interface{}); ok {
		status := statusOkStyle.Render("🟢 ACTIVE")
		fmt.Printf(" Status:          %s\n", status)
		fmt.Printf(" Uptime:          %v\n", wStats["uptime"])
		fmt.Printf(" Queue Depth:     %v\n", wStats["queue_depth"])
		fmt.Printf(" Pending Jobs:    %v\n", wStats["pending_jobs"])
		fmt.Printf(" Total Enqueued:  %v\n", wStats["total_enqueued"])
		fmt.Printf(" Total Processed: %v\n", wStats["total_processed"])

		errVal := wStats["total_errors"]
		errStr := fmt.Sprintf("%v", errVal)
		if errVal != nil && errStr != "0" && errStr != "0.0" {
			errStr = statusErrStyle.Render(errStr)
		} else {
			errStr = statusOkStyle.Render("0")
		}
		fmt.Printf(" Total Errors:    %s\n", errStr)
		fmt.Printf(" Speed:           %v items/sec\n", wStats["items_per_sec"])
	} else {
		fmt.Printf(" %s Worker stats unavailable\n", statusWaitStyle.Render("🟡"))
	}
	fmt.Println()
	return nil
}

func renderReindexStats(label string, data interface{}) {
	m, ok := data.(map[string]interface{})
	if !ok || m == nil {
		fmt.Printf(" %-6s : %s stats unavailable\n", label, statusWaitStyle.Render("🟡"))
		return
	}

	status := statusWaitStyle.Render("🟡 IDLE")
	if isRunning, _ := m["is_running"].(bool); isRunning {
		status = statusOkStyle.Render("🟢 BUSY")
	}

	speed := ""
	if v, exists := m["items_per_sec"]; exists {
		speed = fmt.Sprintf(" | Speed: %v items/sec", v)
	}

	dur := ""
	if v, exists := m["duration"]; exists {
		dur = fmt.Sprintf(" | Time: %v", v)
	}

	fmt.Printf(" %-6s : %s | Total Processed: %v%s%s\n", label, status, m["total_processed"], dur, speed)
}

func runReindexSearch(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	resp, err := adminRequest("POST", "/admin/reindex/search")
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	if jsonMode {
		common.PrintJSON(resp)
		return nil
	}
	fmt.Println(titleStyle.Render(" ncli relay: triggering search reindex "))
	fmt.Printf("%s %v\n", statusOkStyle.Render("✔"), resp["status"])
	fmt.Println("Tip: Run `ncli relay stats` to track progress.")
	return nil
}

func runReindexZaps(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	resp, err := adminRequest("POST", "/admin/reindex/zaps")
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	if jsonMode {
		common.PrintJSON(resp)
		return nil
	}
	fmt.Println(titleStyle.Render(" ncli relay: triggering zaps reindex "))
	fmt.Printf("%s %v\n", statusOkStyle.Render("✔"), resp["status"])
	fmt.Println("Tip: Run `ncli relay stats` to track progress.")
	return nil
}

func runClearSearch(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	resp, err := adminRequest("DELETE", "/admin/search")
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	if jsonMode {
		common.PrintJSON(resp)
		return nil
	}
	fmt.Println(titleStyle.Render(" ncli relay: clearing search index "))
	fmt.Printf("%s %v\n", statusOkStyle.Render("✔"), resp["status"])
	return nil
}

func runClearZaps(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	resp, err := adminRequest("DELETE", "/admin/zaps")
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	if jsonMode {
		common.PrintJSON(resp)
		return nil
	}
	fmt.Println(titleStyle.Render(" ncli relay: clearing zaps index "))
	fmt.Printf("%s %v\n", statusOkStyle.Render("✔"), resp["status"])
	return nil
}

/////////////////////////////////////////////////////////////////////
// Membership admin: rendering helpers
/////////////////////////////////////////////////////////////////////

// truncPubkey renders a hex pubkey as its first/last 8 hex chars -- a
// pubkey isn't secret, so this format (recognizable at a glance, easy to
// typo-check both ends) is preferred over showing the full 64 chars in a
// table row.
func truncPubkey(pubkey string) string {
	if len(pubkey) <= 20 {
		return pubkey
	}
	return pubkey[:8] + "…" + pubkey[len(pubkey)-8:]
}

// truncCode renders an invite code as only its first 8 chars -- unlike a
// pubkey, a claim code is bearer secret material (anyone holding it can
// join), so a table view intentionally shows just enough to distinguish
// rows, never the whole thing. Use --json to get the full code.
func truncCode(code string) string {
	if len(code) <= 8 {
		return code
	}
	return code[:8] + "…"
}

// relativeTime renders a past unix timestamp as a short "Xd/Xh/Xm ago"
// string, or "-" for the zero value (no timestamp).
func relativeTime(unixTs int64) string {
	if unixTs <= 0 {
		return "-"
	}
	d := time.Since(time.Unix(unixTs, 0))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// formatUnix renders a unix timestamp as a fixed-width local date/time, or
// "never" for the zero value -- used for invite expires_at, where an
// operator deciding whether a code is still safe to hand out wants an
// unambiguous absolute time, not a relative one.
func formatUnix(unixTs int64) string {
	if unixTs <= 0 {
		return "never"
	}
	return time.Unix(unixTs, 0).Local().Format("2006-01-02 15:04")
}

func strField(m map[string]interface{}, key string) string {
	s, _ := m[key].(string)
	return s
}

func int64Field(m map[string]interface{}, key string) int64 {
	f, _ := m[key].(float64)
	return int64(f)
}

func stringsField(m map[string]interface{}, key string) []string {
	raw, ok := m[key].([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// joinOrDash comma-joins items, or "-" if there are none -- used for table
// cells (roles, ...) where an empty string alone reads as a rendering bug
// rather than "genuinely none".
func joinOrDash(items []string) string {
	if len(items) == 0 {
		return "-"
	}
	return strings.Join(items, ",")
}

/////////////////////////////////////////////////////////////////////
// Membership admin: members
/////////////////////////////////////////////////////////////////////

func runMembersList(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	resp, err := adminRequest("GET", "/admin/membership/members")
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	if jsonMode {
		common.PrintJSON(resp)
		return nil
	}

	members, _ := resp["members"].([]interface{})
	fmt.Println(titleStyle.Render(" ncli relay: members "))
	if len(members) == 0 {
		fmt.Println(" (no members)")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PUBKEY\tROLES\tJOINED")
	for _, raw := range members {
		m, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", truncPubkey(strField(m, "pubkey")), joinOrDash(stringsField(m, "roles")), relativeTime(int64Field(m, "joined_at")))
	}
	tw.Flush()
	return nil
}

func runMembersShow(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	pubkey := args[0]
	resp, err := adminRequest("GET", "/admin/membership/members/"+pubkey)
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	if jsonMode {
		common.PrintJSON(resp)
		return nil
	}

	fmt.Println(titleStyle.Render(" ncli relay: member "))
	fmt.Printf(" Pubkey: %s\n", strField(resp, "pubkey"))
	fmt.Printf(" Roles:  %s\n", joinOrDash(stringsField(resp, "roles")))
	fmt.Printf(" Joined: %s\n", relativeTime(int64Field(resp, "joined_at")))
	return nil
}

func runMembersAdd(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	pubkey := args[0]
	roles, _ := cmd.Flags().GetStringArray("role")

	resp, err := adminRequestBody("POST", "/admin/membership/members", map[string]interface{}{"pubkey": pubkey, "roles": roles})
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	if jsonMode {
		common.PrintJSON(resp)
		return nil
	}

	fmt.Println(titleStyle.Render(" ncli relay: members add "))
	fmt.Printf("%s enrolled %s", statusOkStyle.Render("✔"), truncPubkey(pubkey))
	if len(roles) > 0 {
		fmt.Printf(" (roles: %s)", strings.Join(roles, ","))
	}
	fmt.Println()
	return nil
}

func runMembersRemove(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	pubkey := args[0]
	resp, err := adminRequest("DELETE", "/admin/membership/members/"+pubkey)
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	if jsonMode {
		common.PrintJSON(resp)
		return nil
	}
	fmt.Println(titleStyle.Render(" ncli relay: members remove "))
	fmt.Printf("%s removed %s\n", statusOkStyle.Render("✔"), truncPubkey(pubkey))
	return nil
}

/////////////////////////////////////////////////////////////////////
// Membership admin: invites
/////////////////////////////////////////////////////////////////////

func runInvitesCreate(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	ttl, _ := cmd.Flags().GetDuration("ttl")
	maxUses, _ := cmd.Flags().GetInt("max-uses")
	roles, _ := cmd.Flags().GetStringArray("role")

	body := map[string]interface{}{"max_uses": maxUses, "roles": roles}
	if ttl > 0 {
		body["ttl"] = ttl.String()
	}

	resp, err := adminRequestBody("POST", "/admin/membership/invites", body)
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	if jsonMode {
		common.PrintJSON(resp)
		return nil
	}

	fmt.Println(titleStyle.Render(" ncli relay: invites create "))
	fmt.Printf(" Code:       %s\n", strField(resp, "code"))
	fmt.Printf(" Expires:    %s\n", formatUnix(int64Field(resp, "expires_at")))
	maxUsesVal := int64Field(resp, "max_uses")
	if maxUsesVal <= 0 {
		fmt.Printf(" Max uses:   unlimited\n")
	} else {
		fmt.Printf(" Max uses:   %d\n", maxUsesVal)
	}
	if roles := stringsField(resp, "roles"); len(roles) > 0 {
		fmt.Printf(" Roles:      %s\n", strings.Join(roles, ","))
	}
	return nil
}

func runInvitesList(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	resp, err := adminRequest("GET", "/admin/membership/invites")
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	if jsonMode {
		common.PrintJSON(resp)
		return nil
	}

	invites, _ := resp["invites"].([]interface{})
	fmt.Println(titleStyle.Render(" ncli relay: invites "))
	if len(invites) == 0 {
		fmt.Println(" (no invite codes)")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "CODE\tUSES\tEXPIRES\tROLES")
	for _, raw := range invites {
		m, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		maxUses := int64Field(m, "max_uses")
		usesCol := fmt.Sprintf("%d/unlimited", int64Field(m, "uses"))
		if maxUses > 0 {
			usesCol = fmt.Sprintf("%d/%d", int64Field(m, "uses"), maxUses)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", truncCode(strField(m, "code")), usesCol, formatUnix(int64Field(m, "expires_at")), joinOrDash(stringsField(m, "roles")))
	}
	tw.Flush()
	return nil
}

func runInvitesRevoke(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	code := args[0]
	resp, err := adminRequest("DELETE", "/admin/membership/invites/"+code)
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	if jsonMode {
		common.PrintJSON(resp)
		return nil
	}
	fmt.Println(titleStyle.Render(" ncli relay: invites revoke "))
	fmt.Printf("%s revoked %s\n", statusOkStyle.Render("✔"), truncCode(code))
	return nil
}

/////////////////////////////////////////////////////////////////////
// Membership admin: roles
/////////////////////////////////////////////////////////////////////

func runRolesList(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	resp, err := adminRequest("GET", "/admin/membership/roles")
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	if jsonMode {
		common.PrintJSON(resp)
		return nil
	}

	roles, _ := resp["roles"].([]interface{})
	fmt.Println(titleStyle.Render(" ncli relay: roles "))
	if len(roles) == 0 {
		fmt.Println(" (no roles defined)")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tLABEL\tDESCRIPTION\tCOLOR\tORDER")
	for _, raw := range roles {
		m, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		color, order := "-", "-"
		if v, ok := m["color"].(float64); ok {
			color = fmt.Sprintf("%d", int(v))
		}
		if v, ok := m["order"].(float64); ok {
			order = fmt.Sprintf("%d", int(v))
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", strField(m, "id"), strField(m, "label"), strField(m, "description"), color, order)
	}
	tw.Flush()
	return nil
}

func runRolesCreate(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	id := args[0]
	label, _ := cmd.Flags().GetString("label")
	description, _ := cmd.Flags().GetString("description")

	body := roleJSON{ID: id, Label: label, Description: description}
	if cmd.Flags().Changed("color") {
		c, _ := cmd.Flags().GetInt("color")
		body.Color = &c
	}
	if cmd.Flags().Changed("order") {
		o, _ := cmd.Flags().GetInt("order")
		body.Order = &o
	}

	resp, err := adminRequestBody("POST", "/admin/membership/roles", body)
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	if jsonMode {
		common.PrintJSON(resp)
		return nil
	}

	fmt.Println(titleStyle.Render(" ncli relay: roles create "))
	fmt.Printf("%s created role %s\n", statusOkStyle.Render("✔"), strField(resp, "id"))
	return nil
}
