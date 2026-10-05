package groups

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/spf13/cobra"
)

// signAndPublish attaches `previous` references, signs ev with privKeyHex,
// and publishes it to relayURL, returning the publish report for the
// caller to present. Shared by every ncli groups write command so they
// can't drift into inconsistent signing/publish shapes; most callers just
// hand the report to printPublishReport, but one (invite) has its own
// extra field to report alongside it.
func signAndPublish(ctx context.Context, cmd *cobra.Command, relayURL *url.URL, groupID string, ev *nip01.Event, privKeyHex string) (*client.PublishReport, error) {
	if err := attachPreviousTags(ctx, relayURL, groupID, ev); err != nil {
		if errors.Is(err, client.ErrNoReachableTargets) {
			return nil, common.NetworkError(cmd, relayURL.String(), err)
		}
		return nil, common.RuntimeError(cmd, err)
	}

	if err := ev.Sign(privKeyHex); err != nil {
		return nil, common.RuntimeError(cmd, err)
	}

	targets, err := client.TargetsFromRelayList([]string{relayURL.String()})
	if err != nil {
		return nil, common.RuntimeError(cmd, err)
	}

	var report *client.PublishReport
	err = common.WithSpinner(cmd, fmt.Sprintf("Publishing to %s", relayURL.Host), func() error {
		var pErr error
		report, pErr = client.PublishToTargets(ctx, targets, []*nip01.Event{ev})
		return pErr
	})
	if err != nil {
		if errors.Is(err, client.ErrNoReachableTargets) {
			return nil, common.NetworkError(cmd, relayURL.String(), err)
		}
		return nil, common.RuntimeError(cmd, err)
	}
	return report, nil
}

// publishGroupEvent is signAndPublish plus the default report presentation
// (text by default, structured JSON under --json, matching "ncli publish")
// -- the common case, for every write command with nothing beyond the
// publish outcome itself to report.
func publishGroupEvent(ctx context.Context, cmd *cobra.Command, relayURL *url.URL, groupID string, ev *nip01.Event, privKeyHex string) error {
	report, err := signAndPublish(ctx, cmd, relayURL, groupID, ev, privKeyHex)
	if err != nil {
		return err
	}
	printPublishReport(cmd, report)
	return failedPublishErr(cmd, report)
}

// printPublishReport renders report the same way "ncli publish" does: text
// by default, structured JSON under --json.
func printPublishReport(cmd *cobra.Command, report *client.PublishReport) {
	jsonMode, _ := cmd.Flags().GetBool("json")
	if jsonMode {
		common.PrintJSON(report)
		return
	}
	// Same stdout-only-holds-the-result convention as "ncli publish".
	for _, r := range report.Results {
		if r.Accepted {
			fmt.Printf("published %s to %s\n", r.ID, r.Relay)
		} else {
			fmt.Printf("failed %s to %s: %s\n", r.ID, r.Relay, r.Error)
		}
	}
}

// failedPublishErr turns a report with any failed attempt into the
// command's own error return, nil otherwise.
func failedPublishErr(cmd *cobra.Command, report *client.PublishReport) error {
	if report.AllSucceeded() {
		return nil
	}
	return common.RuntimeError(cmd, fmt.Errorf("%d of %d publish attempts failed", report.Failed, report.Attempted))
}
