package ncli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/delegate"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip19"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/term"
)

var idCmd = &cobra.Command{
	Use:   "id [identifier]",
	Short: "Generate or inspect a Nostr identity",
	Long: `With no argument, generates a brand-new Nostr keypair, displays every
form of it (hex, nsec, npub -- the only time the private key is ever shown
in plaintext unless later revealed from the vault), and offers to save it
to the local vault.

With an argument, resolves identifier -- a saved vault label, an npub, a
hex pubkey, an nsec, an nprofile, or a nip-05 "name@domain" address -- and
displays its forms plus whether it's already saved in the vault.

--json switches to structured JSON output on stdout and disables every
interactive prompt: saving only happens with --save, the label only comes
from --label (falling back to the npub if omitted), and the vault password
only comes from the NCLI_VAULT_PASSWORD environment variable.

See "ncli id delegate" to mint a NIP-26 delegation token from an identity.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return runIDGenerate(cmd)
		}
		return runIDInspect(cmd, args[0])
	},
}

var idListCmd = &cobra.Command{
	Use:   "list",
	Short: "List saved vault identities",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runIDList(cmd)
	},
}

func init() {
	RootCmd.AddCommand(idCmd)
	idCmd.AddCommand(idListCmd)
	idCmd.AddCommand(delegate.NewDelegateCommand())

	// Local (not persistent) flags: idCmd's own RunE reads them the same
	// way either way, but persistent flags are inherited by every child --
	// which would leak --save/--label/--reveal (meaningless for "delegate")
	// into its help text and collide with delegate's own local --json flag.
	// "list" doesn't inherit these either, so it re-declares the two it
	// actually uses below.
	idCmd.Flags().Bool("save", false, "generate: save the new identity to the vault")
	idCmd.Flags().String("label", "", "generate: label to save the new identity under")
	idCmd.Flags().Bool("reveal", false, "inspect/list: decrypt and include the private key of vault-saved identities")

	idListCmd.Flags().Bool("reveal", false, "Decrypt and include the private key of vault-saved identities")
}

// classifyIdentifierError picks InvalidInputError (a malformed npub/nsec/
// hex/nprofile string) or NetworkError (a nip-05 "name@domain" lookup that
// failed to resolve, e.g. DNS/HTTP) for a client.ResolveIdentifier/
// ResolveFindIdentifier failure -- both funnel into one error return
// without a distinguishable type, and a "name@domain"-shaped identifier's
// failure is overwhelmingly a network problem, not a malformed-string one.
func classifyIdentifierError(cmd *cobra.Command, identifier string, err error) error {
	input := common.RedactSecretInput(identifier)
	if strings.Contains(identifier, "@") {
		return common.NetworkError(cmd, input, err)
	}
	return common.InvalidInputError(cmd, input, err)
}

// resolveVaultPassword sources the vault password from NCLI_VAULT_PASSWORD
// if set (a universal override, in any mode), otherwise errors in JSON
// mode (never prompts -- an agent driving this over a pipe has no TTY to
// prompt), otherwise prompts interactively with promptText.
func resolveVaultPassword(jsonMode bool, promptText string) (string, error) {
	if pw := viper.GetString("vault.password"); pw != "" {
		return pw, nil
	}
	if jsonMode {
		return "", errors.New("vault password required; set NCLI_VAULT_PASSWORD")
	}
	return promptPassword(promptText)
}

func runIDInspect(cmd *cobra.Command, arg string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	reveal, _ := cmd.Flags().GetBool("reveal")

	result, err := client.ResolveIdentifier(arg)
	if err != nil {
		return classifyIdentifierError(cmd, arg, err)
	}

	if reveal && result.PrivKeyHex == "" {
		if !result.InVault {
			return common.NotFoundError(cmd, common.RedactSecretInput(arg), errors.New("identity not saved in vault, nothing to reveal"))
		}

		password, err := resolveVaultPassword(jsonMode, "Vault password: ")
		if err != nil {
			return common.UsageError(cmd, err)
		}
		vaultPrivKeyHex, err := client.UnlockVaultIdentity(password)
		if err != nil {
			return common.AuthError(cmd, err)
		}

		entry, found, err := client.FindVaultEntry(result.Npub)
		if err != nil {
			return common.RuntimeError(cmd, err)
		}
		if !found {
			return common.NotFoundError(cmd, result.Npub, fmt.Errorf("vault entry disappeared during reveal"))
		}
		privHex, err := client.DecryptVaultEntry(vaultPrivKeyHex, *entry)
		if err != nil {
			return common.RuntimeError(cmd, err)
		}
		result.PrivKeyHex = privHex
		nsec, err := nip19.EncodePrivateKey(privHex)
		if err != nil {
			return common.RuntimeError(cmd, err)
		}
		result.Nsec = nsec
	}

	if jsonMode {
		common.PrintJSON(result)
		return nil
	}

	fmt.Println("hex pubkey:", result.PubKeyHex)
	fmt.Println("npub:      ", result.Npub)
	if result.Nip05 != "" {
		fmt.Println("nip-05:    ", result.Nip05, "(resolved)")
	}
	if len(result.Relays) > 0 {
		fmt.Println("relays:    ", strings.Join(result.Relays, ", "))
	}
	if result.PrivKeyHex != "" {
		fmt.Println("hex privkey:", result.PrivKeyHex)
		fmt.Println("nsec:       ", result.Nsec)
	}
	// Vault status is printed last and only when the identity is actually
	// saved -- an unsaved identity has nothing vault-related worth reporting.
	if result.InVault {
		fmt.Printf("vault:      saved (label: %s)\n", result.VaultLabel)
	}
	return nil
}

func runIDGenerate(cmd *cobra.Command) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	save, _ := cmd.Flags().GetBool("save")
	labelFlag, _ := cmd.Flags().GetString("label")

	id, err := client.GenerateIdentity()
	if err != nil {
		return common.RuntimeError(cmd, err)
	}

	if jsonMode {
		if !save {
			common.PrintJSON(map[string]any{
				"priv_hex": id.PrivKeyHex, "pub_hex": id.PubKeyHex,
				"nsec": id.Nsec, "npub": id.Npub, "saved": false,
			})
			return nil
		}

		entry, err := saveIdentity(cmd, jsonMode, id, labelFlag)
		if err != nil {
			return common.RuntimeError(cmd, err)
		}
		common.PrintJSON(map[string]any{
			"priv_hex": id.PrivKeyHex, "pub_hex": id.PubKeyHex,
			"nsec": id.Nsec, "npub": id.Npub, "saved": true, "label": entry.Label,
		})
		return nil
	}

	fmt.Println("hex privkey:", id.PrivKeyHex)
	fmt.Println("hex pubkey: ", id.PubKeyHex)
	fmt.Println("nsec:       ", id.Nsec)
	fmt.Println("npub:       ", id.Npub)
	fmt.Println()
	fmt.Println("This is the only time the nsec above will be shown in plaintext -- store it somewhere safe.")

	stdin := bufio.NewReader(os.Stdin)
	if !save && !promptYesNo(stdin, "Save this identity to your vault? [y/N] ") {
		return nil
	}

	label := labelFlag
	if label == "" {
		label, err = promptLine(stdin, "Label for this identity (optional, press Enter to use the npub): ")
		if err != nil {
			return common.RuntimeError(cmd, err)
		}
	}

	entry, err := saveIdentity(cmd, jsonMode, id, label)
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	log.Info().Msgf("identity saved to vault (label: %s)", entry.Label)
	return nil
}

// saveIdentity unlocks (or creates) the vault and adds id under label,
// narrating progress via log.Info in text mode. label may be blank --
// client.AddVaultEntry defaults it to the identity's own npub.
func saveIdentity(cmd *cobra.Command, jsonMode bool, id *client.Identity, label string) (*client.VaultEntry, error) {
	exists, err := client.VaultExists()
	if err != nil {
		return nil, err
	}

	var vaultPrivKeyHex string
	if !exists {
		password, err := resolveNewVaultPassword(jsonMode)
		if err != nil {
			return nil, err
		}
		if !jsonMode {
			log.Info().Msg("creating vault identity...")
		}
		vaultNpub, privHex, err := client.CreateVaultIdentity(password)
		if err != nil {
			return nil, fmt.Errorf("failed to create vault: %w", err)
		}
		if !jsonMode {
			log.Info().Str("npub", vaultNpub).Msg("vault identity created")
		}
		vaultPrivKeyHex = privHex
	} else {
		password, err := resolveVaultPassword(jsonMode, "Vault password: ")
		if err != nil {
			return nil, err
		}
		vaultPrivKeyHex, err = client.UnlockVaultIdentity(password)
		if err != nil {
			return nil, fmt.Errorf("failed to unlock vault: %w", err)
		}
	}

	if !jsonMode {
		log.Info().Msg("saving identity...")
	}
	entry, err := client.AddVaultEntry(vaultPrivKeyHex, label, id.PrivKeyHex)
	if err != nil {
		return nil, fmt.Errorf("failed to save identity: %w", err)
	}
	return entry, nil
}

// resolveNewVaultPassword sources the password to protect a brand-new
// vault identity. When it comes from NCLI_VAULT_PASSWORD, the
// confirm-by-retyping step is skipped -- there's no risk of a mistyped
// confirmation when the value came from one authoritative source rather
// than two rounds of human typing.
func resolveNewVaultPassword(jsonMode bool) (string, error) {
	if pw := viper.GetString("vault.password"); pw != "" {
		return pw, nil
	}
	if jsonMode {
		return "", errors.New("vault password required; set NCLI_VAULT_PASSWORD")
	}
	return promptNewPassword()
}

func runIDList(cmd *cobra.Command) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	reveal, _ := cmd.Flags().GetBool("reveal")

	entries, err := client.LoadVaultEntries()
	if err != nil {
		return common.RuntimeError(cmd, err)
	}

	var vaultPrivKeyHex string
	if reveal && len(entries) > 0 {
		password, err := resolveVaultPassword(jsonMode, "Vault password: ")
		if err != nil {
			return common.UsageError(cmd, err)
		}
		vaultPrivKeyHex, err = client.UnlockVaultIdentity(password)
		if err != nil {
			return common.AuthError(cmd, err)
		}
	}

	type listEntry struct {
		Label      string `json:"label"`
		Npub       string `json:"npub"`
		CreatedAt  string `json:"created_at"`
		PrivKeyHex string `json:"priv_hex,omitempty"`
		Nsec       string `json:"nsec,omitempty"`
	}
	rows := make([]listEntry, 0, len(entries))
	for _, e := range entries {
		row := listEntry{Label: e.Label, Npub: e.Npub, CreatedAt: e.CreatedAt}
		if reveal {
			privHex, err := client.DecryptVaultEntry(vaultPrivKeyHex, e)
			if err != nil {
				return common.RuntimeError(cmd, fmt.Errorf("failed to decrypt %q: %w", e.Label, err))
			}
			nsec, err := nip19.EncodePrivateKey(privHex)
			if err != nil {
				return common.RuntimeError(cmd, err)
			}
			row.PrivKeyHex = privHex
			row.Nsec = nsec
		}
		rows = append(rows, row)
	}

	if jsonMode {
		common.PrintJSON(map[string]any{"identities": rows})
		return nil
	}

	if len(rows) == 0 {
		fmt.Println("no identities saved in vault")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	if reveal {
		fmt.Fprintln(w, "LABEL\tNPUB\tCREATED\tPRIV_HEX\tNSEC")
		for _, r := range rows {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Label, r.Npub, r.CreatedAt, r.PrivKeyHex, r.Nsec)
		}
	} else {
		fmt.Fprintln(w, "LABEL\tNPUB\tCREATED")
		for _, r := range rows {
			fmt.Fprintf(w, "%s\t%s\t%s\n", r.Label, r.Npub, r.CreatedAt)
		}
	}
	w.Flush()
	return nil
}

func promptYesNo(r *bufio.Reader, prompt string) bool {
	fmt.Print(prompt)
	line, _ := r.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

func promptLine(r *bufio.Reader, prompt string) (string, error) {
	fmt.Print(prompt)
	line, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, os.ErrClosed) && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// promptPassword reads a password with echo disabled. This requires stdin
// to be an actual terminal (it issues a termios ioctl); piped/non-
// interactive stdin will error here -- only reached when --json is unset
// and NCLI_VAULT_PASSWORD is unset, i.e. the deliberately-interactive-only
// path (the same trade-off ssh-keygen/sudo make).
func promptPassword(prompt string) (string, error) {
	fmt.Print(prompt)
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", err
	}
	return string(pw), nil
}

func promptNewPassword() (string, error) {
	pw, err := promptPassword("Set a vault password: ")
	if err != nil {
		return "", err
	}
	if pw == "" {
		return "", errors.New("password must not be empty")
	}
	confirm, err := promptPassword("Confirm vault password: ")
	if err != nil {
		return "", err
	}
	if pw != confirm {
		return "", errors.New("passwords did not match")
	}
	return pw, nil
}
