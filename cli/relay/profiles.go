package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	nmconfig "github.com/ohstr/nmilat/config"
	"github.com/ohstr/nmilat/relay"
	"github.com/ohstr/nmilat/utils"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// checkVerificationConfig validates cache.profiles.checks and
// cache.profiles.scoring up front, so a typo fails startup instead of
// being ignored.
func checkVerificationConfig(cfg *RelayConfig) error {
	if _, err := verificationSettings(cfg); err != nil {
		return err
	}
	if _, err := scoringOverride(cfg.profiles().Scoring); err != nil {
		return &common.CLIError{Err: fmt.Errorf("cache.profiles.scoring: %w", err), Code: common.CodeInvalidInput}
	}
	return nil
}

// verificationSettings turns cache.profiles.checks into nmilat's settings;
// unset fields keep nmilat's defaults.
func verificationSettings(cfg *RelayConfig) (relay.VerificationConfig, error) {
	out := relay.VerificationConfig{UserAgent: "ncli/" + common.ReadBuildInfo().Version}
	v := cfg.profiles().Checks
	if v == nil {
		return out, nil
	}
	durations := []struct {
		key string
		raw string
		dst *time.Duration
	}{
		{"cache.profiles.checks.ttl", v.TTL, &out.TTL},
		{"cache.profiles.checks.perHostInterval", v.PerHostInterval, &out.PerHostInterval},
	}
	if v.Retry != nil {
		durations = append(durations,
			struct {
				key string
				raw string
				dst *time.Duration
			}{"cache.profiles.checks.retry.initial", v.Retry.Initial, &out.RetryInitial},
			struct {
				key string
				raw string
				dst *time.Duration
			}{"cache.profiles.checks.retry.max", v.Retry.Max, &out.RetryMax},
		)
		out.MaxAttempts = v.Retry.MaxAttempts
	}
	for _, d := range durations {
		if d.raw == "" {
			continue
		}
		if d.raw == "0" {
			*d.dst = 0
			continue
		}
		parsed, err := parseConfigDuration(d.key, d.raw)
		if err != nil {
			return out, err
		}
		*d.dst = parsed
	}
	// nmilat reads a zero TTL as "default"; "0" here means never.
	if v.TTL != "" && out.TTL == 0 {
		out.TTL = -1
	}
	return out, nil
}

// scoringOverride decodes cache.profiles.scoring over the built-in weights. Only
// known sections and keys are accepted.
func scoringOverride(raw map[string]any) (*nmconfig.SearchConfig, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	for section := range raw {
		switch section {
		case "scoring", "verification", "social":
		default:
			return nil, fmt.Errorf("unknown section %q (want scoring, verification or social)", section)
		}
	}
	data, err := yaml.Marshal(raw)
	if err != nil {
		return nil, err
	}
	next := nmconfig.Get().Search
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&next); err != nil {
		return nil, err
	}
	return &next, nil
}

// applyScoring installs cache.profiles.scoring before the relay serves anything.
func applyScoring(cfg *RelayConfig) {
	next, err := scoringOverride(cfg.profiles().Scoring)
	if err != nil || next == nil {
		return
	}
	nmconfig.Configure(func(c *nmconfig.AppConfig) { c.Search = *next })
	log.Info().Msg("profile score weights overridden by cache.profiles.scoring")
}

// profileReport is what the admin API answers about one profile.
type profileReport struct {
	Pubkey  string                `json:"pubkey"`
	Score   int64                 `json:"score"`
	Base    int64                 `json:"base_score"`
	Checks  int64                 `json:"check_score"`
	Social  int64                 `json:"social_score"`
	Nip05   relay.CheckState      `json:"nip05"`
	Lud16   relay.CheckState      `json:"lud16"`
	Picture relay.CheckState      `json:"picture"`
	Lnurl   string                `json:"lnurl_pubkey,omitempty"`
	Stats   *relay.ProfileStats   `json:"stats"`
	Metrics *relay.ProfileMetrics `json:"-"`
}

func newProfileReport(store *relay.EventStore, pubkey string) (*profileReport, error) {
	m, err := store.GetProfileMetrics(pubkey)
	if err != nil {
		return nil, err
	}
	st, err := store.GetProfileStats(pubkey)
	if errors.Is(err, relay.ErrIndexDisabled) {
		st, err = nil, nil // stats are off on this relay
	}
	if err != nil {
		return nil, err
	}
	return &profileReport{
		Pubkey: pubkey, Score: m.TotalScore(), Base: m.BaseScore, Checks: m.CheckScore(), Social: m.SocialScore,
		Nip05: m.Nip05, Lud16: m.Lud16, Picture: m.Picture, Lnurl: m.Lud16NostrPubkey, Stats: st, Metrics: m,
	}, nil
}

// verifiedNip05 is pubkey's NIP-05 address if this relay has checked it
// out, else "".
func verifiedNip05(store *relay.EventStore, pubkey string) string {
	m, err := store.GetProfileMetrics(pubkey)
	if err != nil || m == nil || m.Nip05.Status != relay.CheckOK {
		return ""
	}
	return m.Nip05.Value
}

// profileRoutes serves GET /admin/api/profiles/{pubkey} (stored checks and
// stats) and POST .../verify (check it now, fetching every field).
func profileRoutes(r interface {
	Handle(pattern string, h http.HandlerFunc)
}, store *relay.EventStore, worker *relay.ProfileVerificationWorker) {
	pubkeyOf := func(w http.ResponseWriter, req *http.Request) (string, bool) {
		pk := strings.ToLower(req.PathValue("pubkey"))
		if utils.Validate32Key(pk) != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid pubkey")
			return "", false
		}
		return pk, true
	}
	r.Handle("GET /admin/api/profiles/{pubkey}", func(w http.ResponseWriter, req *http.Request) {
		pk, ok := pubkeyOf(w, req)
		if !ok {
			return
		}
		report, err := newProfileReport(store, pk)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeAPIJSON(w, http.StatusOK, report)
	})
	r.Handle("POST /admin/api/profiles/{pubkey}/verify", func(w http.ResponseWriter, req *http.Request) {
		pk, ok := pubkeyOf(w, req)
		if !ok {
			return
		}
		if _, err := worker.VerifyNow(req.Context(), pk); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, relay.ErrEventNotFound) {
				status = http.StatusNotFound
				err = errors.New("no stored profile (kind 0) for this pubkey")
			}
			writeAPIError(w, status, err.Error())
			return
		}
		report, err := newProfileReport(store, pk)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeAPIJSON(w, http.StatusOK, report)
	})
}

func writeAPIJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAPIError(w http.ResponseWriter, status int, msg string) {
	writeAPIJSON(w, status, map[string]string{"error": msg})
}

func addVerifyCommand(cmd *cobra.Command) {
	verifyCmd := &cobra.Command{
		Use:   "verify <pubkey|npub|nprofile|nip-05...>",
		Short: "Show what a running relay knows about profiles' NIP-05, lightning address and stats",
		Long: `Prints each profile's stored check results (status, when checked, when
next due, retries, last error), its score and its zap/follow stats.
--refresh checks it again now, fetching every field; nothing else
(including index rebuilds) bypasses the cached results.`,
		Example: `  ncli relay verify npub1... --refresh
  ncli relay verify bob@example.com --json`,
		Args: common.MinimumNArgs(1),
		RunE: runVerify,
	}
	verifyCmd.Flags().Bool("refresh", false, "check now, ignoring when the next check is due")
	cmd.AddCommand(verifyCmd)
}

func runVerify(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	refresh, _ := cmd.Flags().GetBool("refresh")

	var reports []profileReport
	for _, arg := range args {
		pubkey, err := verifyTarget(arg)
		if err != nil {
			return &common.CLIError{Err: err, Code: common.CodeInvalidInput, Input: arg}
		}
		method, path := http.MethodGet, "/admin/api/profiles/"+pubkey
		if refresh {
			method, path = http.MethodPost, path+"/verify"
		}
		var report profileReport
		if _, err := apiCall(cmd, method, path, nil, &report); err != nil {
			return common.RuntimeError(cmd, err)
		}
		reports = append(reports, report)
	}

	if jsonMode {
		common.PrintJSON(map[string]any{"profiles": reports})
		return nil
	}
	for i, r := range reports {
		if i > 0 {
			fmt.Println()
		}
		printProfileReport(r)
	}
	return nil
}

// verifyTarget resolves a hex pubkey, npub, nprofile or nip-05 address.
// (ResolveFindIdentifier reads bare hex as an event id.)
func verifyTarget(arg string) (string, error) {
	if hex := strings.ToLower(strings.TrimSpace(arg)); utils.Validate32Key(hex) == nil {
		return hex, nil
	}
	id, err := client.ResolveFindIdentifier(arg)
	if err != nil {
		return "", err
	}
	if id.Author == "" {
		return "", errors.New("not a pubkey, npub, nprofile or nip-05 address")
	}
	return id.Author, nil
}

func printProfileReport(r profileReport) {
	fmt.Printf("%s  score %d (base %d, checks %d, social %d)\n", r.Pubkey, r.Score, r.Base, r.Checks, r.Social)
	for _, c := range []struct {
		name  string
		state relay.CheckState
	}{{"nip05", r.Nip05}, {"lud16", r.Lud16}, {"picture", r.Picture}} {
		fmt.Printf("  %-8s %s\n", c.name, checkLine(c.state))
	}
	if r.Lnurl != "" {
		fmt.Printf("  %-8s %s\n", "lnurl", "receipts signed by "+r.Lnurl)
	}
	if st := r.Stats; st != nil {
		fmt.Printf("  %-8s %d followers, %d following\n", "follows", st.Followers, st.Following)
		fmt.Printf("  %-8s %d received (%d sats from %d), %d sent (%d sats)\n", "zaps",
			st.ZapCntRecd, st.ZapAmtRecd/1000, st.ZappersRecd, st.ZapCntSent, st.ZapAmtSent/1000)
	}
}

func checkLine(s relay.CheckState) string {
	if s.Value == "" {
		return "-"
	}
	status := s.Status
	if status == "" {
		status = "pending"
	}
	line := fmt.Sprintf("%s  %s", s.Value, status)
	if s.CheckedAt > 0 {
		line += ", checked " + time.Unix(s.CheckedAt, 0).UTC().Format(time.RFC3339)
	}
	if s.NextCheckAt > 0 {
		line += ", next " + time.Unix(s.NextCheckAt, 0).UTC().Format(time.RFC3339)
	}
	if s.Attempts > 0 {
		line += fmt.Sprintf(", %d failed attempt(s)", s.Attempts)
	}
	if s.LastError != "" {
		line += " (" + s.LastError + ")"
	}
	return line
}
