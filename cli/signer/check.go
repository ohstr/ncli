package signer

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/ncli/signer"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip46"
	"github.com/ohstr/nmilat/nipLS"
	"github.com/spf13/cobra"
)

func newCheckCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Dry-run a signer policy against events, offline",
		Long: `Evaluates each event in --events (one object or an array) against
--policy as "signer serve" would, and prints the decision, rule and reason.
Exits 7 (auth) if any request is denied, so it can gate CI.

The used-attestation set starts empty and the built-in key guard is
skipped (no private key here). --start-time enables the "attestation
created before the signer started" check; --now pins the clock so
committed fixtures stay valid.

For encrypt/decrypt rules pass --method and --counterpart instead of
--events.`,
		Example: `  ncli signer check --policy policy.yaml -e event.json --pubkey npub1...
  ncli signer check --policy policy.yaml -e state.json --attestations approval.json \
    --now 2026-10-08T12:00:00Z --start-time 2026-10-08T11:00:00Z
  ncli signer check --policy policy.yaml --method nip44_decrypt --counterpart npub1... --pubkey npub1...`,
		Args: func(cmd *cobra.Command, args []string) error {
			if err := common.NoArgs(cmd, args); err != nil {
				return err
			}
			if err := cmd.ValidateRequiredFlags(); err != nil {
				return common.InvocationOrHelp(cmd, args, err)
			}
			return nil
		},
		RunE: runCheck,
	}
	f := cmd.Flags()
	f.String("policy", "", "Signer policy file (required)")
	f.StringP("events", "e", "", "Unsigned event(s) to evaluate, for sign_event")
	f.String("attestations", "", "Signed attestation event(s) sent with every sign_event")
	f.String("method", nip46.MethodSignEvent, "Method to evaluate")
	f.String("counterpart", "", "Peer pubkey for an encrypt/decrypt method")
	f.String("pubkey", "", "Signer pubkey (npub or hex); defaults to the events' own pubkey")
	f.String("now", "", "Evaluate as of this time (RFC3339 or unix seconds; default: now)")
	f.String("start-time", "", "Signer start time (RFC3339 or unix seconds); attestations created before it are rejected")
	f.Bool("allow-catch-all", false, "Allow an allow rule with an empty selector")
	_ = cmd.MarkFlagRequired("policy")
	_ = cmd.MarkFlagFilename("policy", "yaml", "yml")
	return cmd
}

// CheckResult is one evaluated request.
type CheckResult struct {
	Method       string   `json:"method"`
	EventID      string   `json:"event_id,omitempty"`
	Kind         *int     `json:"kind,omitempty"`
	Counterpart  string   `json:"counterpart,omitempty"`
	Decision     string   `json:"decision"`
	Rule         string   `json:"rule,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Attestations []string `json:"attestations,omitempty"`
}

func runCheck(cmd *cobra.Command, _ []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	f := cmd.Flags()
	policyPath, _ := f.GetString("policy")
	eventsPath, _ := f.GetString("events")
	attPath, _ := f.GetString("attestations")
	method, _ := f.GetString("method")
	counterpartFlag, _ := f.GetString("counterpart")
	pubkeyFlag, _ := f.GetString("pubkey")
	nowFlag, _ := f.GetString("now")
	startFlag, _ := f.GetString("start-time")
	allowCatchAll, _ := f.GetBool("allow-catch-all")

	now := time.Now()
	if nowFlag != "" {
		t, err := parseTime(nowFlag)
		if err != nil {
			return common.InvalidInputError(cmd, nowFlag, fmt.Errorf("--now: %w", err))
		}
		now = t
	}
	var start time.Time
	if startFlag != "" {
		t, err := parseTime(startFlag)
		if err != nil {
			return common.InvalidInputError(cmd, startFlag, fmt.Errorf("--start-time: %w", err))
		}
		start = t
	}

	signing := method == nip46.MethodSignEvent
	switch {
	case signing && eventsPath == "":
		return common.InvocationError(cmd, errors.New("--events is required for sign_event"))
	case !signing && counterpartFlag == "":
		return common.InvocationError(cmd, fmt.Errorf("--counterpart is required for %s", method))
	case !signing && (eventsPath != "" || attPath != ""):
		return common.InvocationError(cmd, fmt.Errorf("--events/--attestations only apply to sign_event"))
	}

	var events []*nip01.Event
	wasArray := false
	if signing {
		var err error
		if events, wasArray, err = client.LoadDraftEvents(eventsPath); err != nil {
			return loadErr(cmd, eventsPath, err)
		}
		if len(events) == 0 {
			return common.InvalidInputError(cmd, eventsPath, errors.New("no events to check"))
		}
	}
	var atts []*nip01.Event
	if attPath != "" {
		var err error
		if atts, err = client.LoadEvents(attPath); err != nil {
			return loadErr(cmd, attPath, err)
		}
	}

	pub := ""
	if pubkeyFlag != "" {
		var err error
		if pub, err = signer.ParsePubkey(pubkeyFlag); err != nil {
			return common.InvalidInputError(cmd, pubkeyFlag, err)
		}
	} else {
		for _, ev := range events {
			if ev.PubKey != "" {
				pub = strings.ToLower(ev.PubKey)
				break
			}
		}
		if pub == "" {
			return common.InvocationError(cmd, errors.New("--pubkey is required when the events carry no pubkey"))
		}
	}

	policy, err := signer.LoadPolicy(policyPath, signer.LoadOptions{AllowCatchAll: allowCatchAll, SignerPub: pub})
	if err != nil {
		return loadErr(cmd, policyPath, err)
	}

	var results []CheckResult
	used := func(string) bool { return false }
	if signing {
		for _, ev := range events {
			res := CheckResult{Method: method, Kind: &ev.Kind}
			var d signer.Decision
			if ev.PubKey != "" && !strings.EqualFold(ev.PubKey, pub) {
				d = signer.Decision{Reason: "event pubkey does not match the signer"}
			} else if err := nipLS.PrepareTarget(ev, pub); err != nil {
				return common.InvalidInputError(cmd, eventsPath, err)
			} else {
				res.EventID = ev.ID
				d = signer.Evaluate(signer.Request{Method: method, Event: ev, Attestations: atts, Now: now, StartTime: start, SignerPub: pub}, policy, used)
			}
			results = append(results, fill(res, d))
		}
	} else {
		counterpart, err := signer.ParsePubkey(counterpartFlag)
		if err != nil {
			return common.InvalidInputError(cmd, counterpartFlag, err)
		}
		d := signer.Evaluate(signer.Request{Method: method, Counterpart: counterpart, Now: now, StartTime: start, SignerPub: pub}, policy, used)
		results = append(results, fill(CheckResult{Method: method, Counterpart: counterpart}, d))
	}

	denied := 0
	for _, r := range results {
		if r.Decision == "deny" {
			denied++
		}
	}
	if jsonMode {
		if wasArray {
			common.PrintJSON(results)
		} else {
			common.PrintJSON(results[0])
		}
	} else {
		for _, r := range results {
			line := fmt.Sprintf("%-5s rule=%s", r.Decision, orDash(r.Rule))
			if r.EventID != "" {
				line += " id=" + r.EventID
			}
			if r.Reason != "" {
				line += "  reason: " + r.Reason
			}
			fmt.Println(line)
		}
	}
	if denied > 0 {
		return common.AuthError(cmd, fmt.Errorf("%d of %d requests denied", denied, len(results)))
	}
	return nil
}

func fill(r CheckResult, d signer.Decision) CheckResult {
	r.Rule, r.Reason = d.Rule, d.Reason
	if d.Allow {
		r.Decision, r.Attestations = "allow", d.Consumed
	} else {
		r.Decision = "deny"
	}
	return r
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func loadErr(cmd *cobra.Command, path string, err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return common.NotFoundError(cmd, path, err)
	}
	return common.InvalidInputError(cmd, path, err)
}

// parseTime accepts RFC3339 or unix seconds.
func parseTime(s string) (time.Time, error) {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, errors.New("want RFC3339 (2026-10-08T12:00:00Z) or unix seconds")
	}
	return t, nil
}
