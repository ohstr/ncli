package space

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

// signAndPublish signs ev with privKeyHex and publishes it to relayURL,
// reporting the outcome the same way "ncli publish" does (text by default,
// structured JSON under --json).
func signAndPublish(ctx context.Context, cmd *cobra.Command, relayURL *url.URL, ev *nip01.Event, privKeyHex string) error {
	if err := ev.Sign(privKeyHex); err != nil {
		return common.RuntimeError(cmd, err)
	}

	targets, err := client.TargetsFromRelayList([]string{relayURL.String()})
	if err != nil {
		return common.RuntimeError(cmd, err)
	}

	var report *client.PublishReport
	err = common.WithSpinner(cmd, fmt.Sprintf("Publishing to %s", relayURL.Host), func() error {
		var pErr error
		report, pErr = client.PublishToTargets(ctx, targets, []*nip01.Event{ev})
		return pErr
	})
	if err != nil {
		if errors.Is(err, client.ErrNoReachableTargets) {
			return common.NetworkError(cmd, relayURL.String(), err)
		}
		return common.RuntimeError(cmd, err)
	}

	jsonMode, _ := cmd.Flags().GetBool("json")
	if jsonMode {
		common.PrintJSON(report)
	} else {
		// Same stdout-only-holds-the-result convention as "ncli publish".
		for _, r := range report.Results {
			if r.Accepted {
				fmt.Printf("published %s to %s\n", r.ID, r.Relay)
			} else {
				fmt.Printf("failed %s to %s: %s\n", r.ID, r.Relay, r.Error)
			}
		}
	}

	if !report.AllSucceeded() {
		return common.RuntimeError(cmd, fmt.Errorf("%d of %d publish attempts failed", report.Failed, report.Attempted))
	}
	return nil
}
