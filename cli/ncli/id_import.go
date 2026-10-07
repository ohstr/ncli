package ncli

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/flokiorg/go-flokicoin/chainutil/bech32"
	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/nip49"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/scrypt"
	"golang.org/x/term"
	"golang.org/x/text/unicode/norm"
)

var idImportCmd = &cobra.Command{
	Use:   "import",
	Short: "Save an existing private key to the vault",
	Long: `Save an existing private key (nsec, hex or ncryptsec) to the vault.

The key is read from stdin, --file, or a hidden prompt -- never from an
argument, so it stays out of shell history and the process list. Each line
is trimmed; the first one holding a valid key is imported and the rest are
ignored.

A key is never saved twice. Importing one already saved under the same
label, or with no --label, changes nothing and succeeds. Under a different
label it's a conflict, unless --force relabels the existing entry.

An ncryptsec's password comes from NCLI_IMPORT_PASSWORD, or a prompt. With
--json or a piped key nothing prompts: set NCLI_IMPORT_PASSWORD and
NCLI_VAULT_PASSWORD.`,
	Example: `  ncli id import --label alice < alice.key
  pass show nostr/alice | ncli id import --label alice --json
  ncli id import --file keys.txt --label alice
  ncli id import --label alice`,
	// Never echo an argument: it's most likely the key itself.
	Args: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return common.InvocationError(cmd, errors.New("id import takes no arguments; pass the key on stdin or with --file"))
		}
		return nil
	},
	RunE: runIDImport,
}

func init() {
	idCmd.AddCommand(idImportCmd)
	idImportCmd.Flags().String("label", "", "Label to save the key under (defaults to its npub)")
	idImportCmd.Flags().StringP("file", "f", "", "Read the key from this file (- for stdin)")
	idImportCmd.Flags().Bool("force", false, "Relabel the key if it's already saved under another label")
}

func runIDImport(cmd *cobra.Command, _ []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	label, _ := cmd.Flags().GetString("label")
	file, _ := cmd.Flags().GetString("file")
	force, _ := cmd.Flags().GetBool("force")
	label = strings.TrimSpace(label)

	stdinTTY := term.IsTerminal(int(os.Stdin.Fd()))
	// Prompts read the terminal on stdin, so a piped key rules them out.
	interactive := !jsonMode && stdinTTY

	var src io.Reader
	switch {
	case file == "-":
		src = os.Stdin
		interactive = false
	case file != "":
		f, err := os.Open(file)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return common.NotFoundError(cmd, file, err)
			}
			return common.InvalidInputError(cmd, file, err)
		}
		defer func() { _ = f.Close() }()
		src = f
	case !stdinTTY:
		src = os.Stdin
	case jsonMode:
		return common.UsageError(cmd, errors.New("no key given; pipe it on stdin or pass --file (--json never prompts)"))
	default:
		_, _ = fmt.Fprint(os.Stderr, "Private key (nsec, hex or ncryptsec): ")
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		_, _ = fmt.Fprintln(os.Stderr)
		if err != nil {
			return common.RuntimeError(cmd, err)
		}
		src = strings.NewReader(string(b))
	}

	key, skipped, err := scanImportKey(src)
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	if skipped > 0 && !jsonMode {
		log.Warn().Msgf("skipped %d line(s) that held no valid key", skipped)
	}
	if key == "" {
		// No input value: whatever was read may be key material.
		return common.InvalidInputError(cmd, "", errors.New("no valid private key found (want nsec, 64-char hex or ncryptsec)"))
	}

	privHex, err := decodeImportKey(cmd, key, interactive)
	if err != nil {
		return err
	}
	id, err := client.IdentityFromPrivKey(privHex)
	if err != nil {
		return common.InvalidInputError(cmd, "", err)
	}

	entries, err := client.LoadVaultEntries()
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	var existing *client.VaultEntry
	for i := range entries {
		if entries[i].Npub == id.Npub {
			existing = &entries[i]
			break
		}
	}

	status, prevLabel := "imported", ""
	var entry *client.VaultEntry
	switch {
	case existing != nil && (label == "" || strings.EqualFold(label, existing.Label)):
		status, entry = "unchanged", existing
	case existing != nil && !force:
		return common.ConflictError(cmd, existing.Label,
			fmt.Errorf("key already saved as %q; pass --force to relabel it %q", existing.Label, label))
	case existing != nil:
		// Same check as any vault write, though relabeling decrypts nothing.
		if _, err := keyresolve.UnlockOrCreateVault(cmd, !interactive); err != nil {
			return err
		}
		prevLabel = existing.Label
		entry, err = client.RelabelVaultEntry(id.Npub, label)
		if err != nil {
			if errors.Is(err, client.ErrLabelExists) {
				return common.ConflictError(cmd, label, err)
			}
			return common.RuntimeError(cmd, err)
		}
		status = "relabeled"
	default:
		vaultPrivHex, err := keyresolve.UnlockOrCreateVault(cmd, !interactive)
		if err != nil {
			return err
		}
		entry, err = client.AddVaultEntry(vaultPrivHex, label, id.PrivKeyHex)
		if err != nil {
			if errors.Is(err, client.ErrLabelExists) {
				return common.ConflictError(cmd, label, err)
			}
			return common.RuntimeError(cmd, err)
		}
	}

	if jsonMode {
		out := map[string]any{
			"npub": id.Npub, "pub_hex": id.PubKeyHex,
			"label": entry.Label, "status": status, "skipped_lines": skipped,
		}
		if prevLabel != "" {
			out["previous_label"] = prevLabel
		}
		common.PrintJSON(out)
		return nil
	}

	fmt.Println("hex pubkey:", id.PubKeyHex)
	fmt.Println("npub:      ", id.Npub)
	switch status {
	case "imported":
		fmt.Printf("vault:      imported (label: %s)\n", entry.Label)
	case "relabeled":
		fmt.Printf("vault:      relabeled %s -> %s\n", prevLabel, entry.Label)
	default:
		fmt.Printf("vault:      already saved (label: %s)\n", entry.Label)
	}
	return nil
}

// scanImportKey returns the first trimmed line of r that holds a well-formed
// nsec, 64-char hex or ncryptsec, and how many non-blank lines it skipped.
// It stops reading at the first match.
func scanImportKey(r io.Reader) (key string, skipped int, err error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if isImportKey(line) {
			return line, skipped, nil
		}
		skipped++
	}
	return "", skipped, sc.Err()
}

func isImportKey(s string) bool {
	switch {
	case strings.HasPrefix(s, "nsec1"):
		_, err := nip19.DecodePrivateKey(s)
		return err == nil
	case strings.HasPrefix(s, "ncryptsec1"):
		_, err := ncryptsecBytes(s)
		return err == nil
	default:
		if len(s) != 64 {
			return false
		}
		_, err := client.IdentityFromPrivKey(strings.ToLower(s))
		return err == nil
	}
}

// decodeImportKey turns a key isImportKey accepted into private-key hex.
func decodeImportKey(cmd *cobra.Command, key string, interactive bool) (string, error) {
	switch {
	case strings.HasPrefix(key, "nsec1"):
		privHex, err := nip19.DecodePrivateKey(key)
		if err != nil {
			return "", common.InvalidInputError(cmd, "", err)
		}
		return privHex, nil
	case strings.HasPrefix(key, "ncryptsec1"):
		password := viper.GetString("import.password")
		if password == "" {
			if !interactive {
				return "", common.UsageError(cmd, errors.New("ncryptsec password required; set NCLI_IMPORT_PASSWORD"))
			}
			var err error
			if password, err = keyresolve.PromptPassword("ncryptsec password: "); err != nil {
				return "", common.RuntimeError(cmd, err)
			}
		}
		privHex, err := decryptNcryptsec(key, password)
		if err != nil {
			return "", common.AuthError(cmd, err)
		}
		return privHex, nil
	default:
		return strings.ToLower(key), nil
	}
}

// ncryptsecBytes decodes a NIP-49 ncryptsec: version(1) logN(1) salt(16)
// nonce(24) [key_security(1)] ciphertext(48). nmilat's own encoder omits
// key_security, so both lengths are accepted.
func ncryptsecBytes(s string) ([]byte, error) {
	hrp, data, err := bech32.DecodeNoLimit(s)
	if err != nil {
		return nil, err
	}
	b, err := bech32.ConvertBits(data, 5, 8, false)
	if err != nil {
		return nil, err
	}
	if hrp != "ncryptsec" || (len(b) != 90 && len(b) != 91) || b[0] != 0x02 {
		return nil, errors.New("malformed ncryptsec")
	}
	return b, nil
}

func decryptNcryptsec(s, password string) (string, error) {
	b, err := ncryptsecBytes(s)
	if err != nil {
		return "", err
	}
	if len(b) == 90 {
		return nip49.Decrypt(s, password)
	}

	// Spec form: NFKC password, key_security byte as associated data.
	key, err := scrypt.Key([]byte(norm.NFKC.String(password)), b[2:18], 1<<b[1], 8, 1, 32)
	if err != nil {
		return "", err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return "", err
	}
	priv, err := aead.Open(nil, b[18:42], b[43:], b[42:43])
	if err != nil {
		return "", errors.New("decryption failed (bad password?)")
	}
	return hex.EncodeToString(priv), nil
}
