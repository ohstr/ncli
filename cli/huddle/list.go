package huddle

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func newListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the huddles that are live on a relay right now",
		Long: `List the rooms that currently have someone in them.

Rooms are created on join and dropped when the last peer leaves, so this is
every room that exists: there is no durable list of rooms, and a room nobody is
in is not a room. Nothing ended or empty is reported.

Each row carries the protocol version the room is pinned to by whoever opened
it. A build speaking a different version is refused at join, so a mismatch here
is the reason a join will fail.

An identity is only needed when the relay restricts huddles to its members; on
an open relay the list needs no credentials.`,
		Example: `  ncli huddle list --relay wss://relay.example
  ncli huddle list --relay ws://localhost:7777 --identity satoshi
  ncli huddle list --relay wss://relay.example --json`,
		Args: common.NoArgs,
		RunE: runList,
	}

	cmd.Flags().String("relay", "", "Relay hosting the huddles (falls back to the first configured prefs relay)")

	return cmd
}

func runList(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")

	relayFlag, _ := cmd.Flags().GetString("relay")
	relayURL, err := resolveRelay(cmd, relayFlag)
	if err != nil {
		return err
	}

	endpoint, err := RoomsEndpoint(relayURL)
	if err != nil {
		return common.InvalidInputError(cmd, relayURL.String(), err)
	}

	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, endpoint, nil)
	if err != nil {
		return common.RuntimeError(cmd, err)
	}

	// Signing is best-effort unless an identity was actually asked for: an open
	// relay needs none, and making every caller name a key to read a public
	// list would be a worse default than letting the relay say no.
	identityFlag, _ := cmd.Flags().GetString("identity")
	if privKeyHex, identityErr := resolveListIdentity(cmd, identityFlag); identityErr != nil {
		return identityErr
	} else if privKeyHex != "" {
		header, err := common.GenerateNIP98Header(privKeyHex, endpoint, http.MethodGet, nil)
		if err != nil {
			// Key material never goes in the error's input field.
			return &common.CLIError{Err: fmt.Errorf("failed to sign the request: %w", err), Code: common.CodeInvalidInput}
		}
		req.Header.Set("Authorization", header)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	var resp *http.Response
	err = common.WithSpinner(cmd, fmt.Sprintf("Listing huddles on %s", relayURL.Host), func() error {
		var doErr error
		resp, doErr = client.Do(req)
		return doErr
	})
	if err != nil {
		return common.NetworkError(cmd, endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return listError(cmd, relayURL, endpoint, resp)
	}

	var payload struct {
		Rooms []struct {
			ID              string `json:"id"`
			Peers           int    `json:"peers"`
			ProtocolVersion uint8  `json:"protocol_version"`
		} `json:"rooms"`
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return common.NetworkError(cmd, endpoint, err)
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return common.RuntimeError(cmd, fmt.Errorf("%s returned an unreadable room list: %w", relayURL.Host, err))
	}

	if jsonMode {
		common.PrintJSON(json.RawMessage(body))
		return nil
	}

	if len(payload.Rooms) == 0 {
		fmt.Println("(no live huddles)")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ROOM\tPEERS\tPROTOCOL")
	for _, r := range payload.Rooms {
		_, _ = fmt.Fprintf(tw, "%s\t%d\tv%d\n", r.ID, r.Peers, r.ProtocolVersion)
	}
	_ = tw.Flush()
	return nil
}

// resolveListIdentity resolves a signing key only when one was actually asked
// for -- via --identity or huddle.identity. Returning "" with no error means
// "go unauthenticated", which is right for a relay that does not restrict
// huddles to members. A named identity that cannot sign is still an error:
// falling back to an anonymous request would hide the problem behind whatever
// the relay answered.
func resolveListIdentity(cmd *cobra.Command, identityFlag string) (string, error) {
	if identityFlag == "" && viper.GetString("huddle.identity") == "" {
		return "", nil
	}
	return resolveIdentity(cmd, identityFlag, "huddle.identity")
}

// listError turns a failed room-list response into the one error line worth
// reading. The 404 is by far the most common: a relay with huddles switched
// off never mounts the route at all.
func listError(cmd *cobra.Command, relayURL *url.URL, endpoint string, resp *http.Response) error {
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	message := strings.TrimSpace(string(detail))

	switch resp.StatusCode {
	case http.StatusNotFound:
		return common.RuntimeError(cmd, fmt.Errorf(
			"%s has no huddle endpoint: the relay is not running with huddles enabled", relayURL.Host))
	case http.StatusUnauthorized:
		hint := "pass --identity to sign the request"
		if resp.Request != nil && resp.Request.Header.Get("Authorization") != "" {
			hint = "the relay rejected this identity's signature"
		}
		return common.AuthError(cmd, fmt.Errorf("%s restricts huddles to its members: %s", relayURL.Host, hint))
	case http.StatusForbidden:
		return common.AuthError(cmd, fmt.Errorf("%s restricts huddles to its members, and this identity is not one", relayURL.Host))
	}

	if message == "" {
		message = resp.Status
	}
	return &common.CLIError{
		Err:   fmt.Errorf("%s could not list huddles: %s", relayURL.Host, message),
		Code:  common.CodeNetwork,
		Input: endpoint,
	}
}

// RoomsEndpoint builds the room-list URL for the relay at base. Exported for
// the same reason Endpoint is: a test should build the URL the command builds,
// not a second guess at the path shape.
func RoomsEndpoint(base *url.URL) (string, error) {
	u := *base
	switch u.Scheme {
	case "wss", "https":
		u.Scheme = "https"
	case "ws", "http":
		u.Scheme = "http"
	default:
		return "", errors.New("relay URL must be ws://, wss://, http:// or https://")
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/huddle/rooms"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}
