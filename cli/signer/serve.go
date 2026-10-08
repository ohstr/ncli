package signer

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/ncli/signer"
	"github.com/ohstr/nmilat/nip49"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func newServeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the signer on a unix socket",
		Long: `Loads the key once, keeps it in memory, and answers sign/encrypt/decrypt
requests on --socket as the policy allows. Every decision is printed to
stdout as one JSON line (never content or key material); --denials-file
also appends denials alone.

--identity is a vault label (password from --password-file or
NCLI_VAULT_PASSWORD) or file:<path> holding an ncryptsec (same password
sources). A plaintext nsec is refused, on the command line or in a file.

SIGHUP reloads the policy and its authors files; --watch also reloads when
their content changes. A bad policy on reload keeps the active one.

A policy that requires attestations needs --state-dir, so used ones can't
be replayed after a restart.`,
		Example: `  ncli signer serve --socket /run/signer/agent.sock --policy policy.yaml \
    --identity file:/etc/signer-key/key.ncryptsec --password-file /etc/signer-key/password \
    --state-dir /var/lib/signer --watch --allow-uid 1000`,
		Args: func(cmd *cobra.Command, args []string) error {
			if err := common.NoArgs(cmd, args); err != nil {
				return err
			}
			if err := cmd.ValidateRequiredFlags(); err != nil {
				return common.InvocationOrHelp(cmd, args, err)
			}
			return nil
		},
		RunE: runServe,
	}
	f := cmd.Flags()
	f.String("socket", "", "Unix socket path (or bunker+unix:// URI) to listen on (required)")
	f.String("policy", "", "Signer policy file (kind: signer-policy) (required)")
	f.String("identity", "", "Vault label, or file:<path> to an ncryptsec (falls back to NCLI_SIGNER_IDENTITY, then the vault's sole entry)")
	f.String("password-file", "", "File holding the vault/ncryptsec password (else NCLI_VAULT_PASSWORD)")
	f.String("state-dir", "", "Directory persisting used attestations (required when the policy uses attestations)")
	f.String("denials-file", "", "Append denial records (NDJSON) to this file")
	f.Bool("allow-catch-all", false, "Allow an allow rule with an empty selector")
	f.Bool("watch", false, "Reload when the policy or an authors file changes")
	f.Duration("watch-interval", 5*time.Second, "Poll interval for --watch")
	f.IntSlice("allow-uid", nil, "Only accept callers with this uid (repeatable; Linux only)")
	f.IntSlice("allow-gid", nil, "Only accept callers with this primary gid (repeatable; Linux only)")
	f.String("socket-mode", "0660", "Socket file mode (octal)")
	f.Int("socket-uid", -1, "Chown the socket to this uid")
	f.Int("socket-gid", -1, "Chown the socket to this gid")
	_ = cmd.MarkFlagRequired("socket")
	_ = cmd.MarkFlagRequired("policy")
	_ = cmd.MarkFlagFilename("policy", "yaml", "yml")
	return cmd
}

func runServe(cmd *cobra.Command, _ []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	f := cmd.Flags()
	socketFlag, _ := f.GetString("socket")
	policyPath, _ := f.GetString("policy")
	identity, _ := f.GetString("identity")
	passwordFile, _ := f.GetString("password-file")
	stateDir, _ := f.GetString("state-dir")
	denialsPath, _ := f.GetString("denials-file")
	allowCatchAll, _ := f.GetBool("allow-catch-all")
	watch, _ := f.GetBool("watch")
	watchInterval, _ := f.GetDuration("watch-interval")
	allowUIDs, _ := f.GetIntSlice("allow-uid")
	allowGIDs, _ := f.GetIntSlice("allow-gid")
	modeFlag, _ := f.GetString("socket-mode")
	socketUID, _ := f.GetInt("socket-uid")
	socketGID, _ := f.GetInt("socket-gid")

	socketPath, err := signer.ParseURI(socketFlag)
	if err != nil {
		return common.InvalidInputError(cmd, socketFlag, err)
	}
	mode, err := strconv.ParseUint(modeFlag, 8, 32)
	if err != nil || mode > 0o777 {
		return common.InvalidInputError(cmd, modeFlag, fmt.Errorf("--socket-mode must be an octal file mode like 0660"))
	}
	if watch && watchInterval <= 0 {
		return common.InvalidInputError(cmd, watchInterval.String(), errors.New("--watch-interval must be positive"))
	}
	if (len(allowUIDs) > 0 || len(allowGIDs) > 0) && !signer.PeerCredSupported {
		return common.UnsupportedError(cmd, "", signer.ErrPeerCredUnsupported)
	}
	if _, err := signer.LoadPolicy(policyPath, signer.LoadOptions{AllowCatchAll: allowCatchAll}); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return common.NotFoundError(cmd, policyPath, err)
		}
		return common.InvalidInputError(cmd, policyPath, err)
	}

	privKeyHex, guardExtra, err := resolveServeKey(cmd, jsonMode, identity, passwordFile)
	if err != nil {
		return err
	}

	cfg := signer.Config{
		PrivKeyHex:  privKeyHex,
		GuardExtra:  guardExtra,
		PolicyPath:  policyPath,
		LoadOptions: signer.LoadOptions{AllowCatchAll: allowCatchAll},
		StateDir:    stateDir,
		AllowUIDs:   allowUIDs,
		AllowGIDs:   allowGIDs,
		Decisions:   os.Stdout,
		Logf:        func(format string, args ...any) { log.Info().Msgf(format, args...) },
		Version:     common.ReadBuildInfo().Version,
	}
	if denialsPath != "" {
		df, err := os.OpenFile(denialsPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return common.RuntimeError(cmd, fmt.Errorf("--denials-file: %w", err))
		}
		defer func() { _ = df.Close() }()
		cfg.Denials = df
	}

	srv, err := signer.New(cfg)
	if err != nil {
		if strings.Contains(err.Error(), "--state-dir") {
			return common.UsageError(cmd, err)
		}
		return common.InvalidInputError(cmd, policyPath, err)
	}
	defer func() { _ = srv.Close() }()

	l, err := signer.Listen(socketPath, os.FileMode(mode), socketUID, socketGID)
	if err != nil {
		if errors.Is(err, signer.ErrAlreadyListening) {
			return common.ConflictError(cmd, socketPath, err)
		}
		return common.RuntimeError(cmd, err)
	}

	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-hup:
				_ = srv.Reload()
			}
		}
	}()
	if watch {
		go srv.Watch(ctx, watchInterval)
	}

	p := srv.Policy()
	log.Info().
		Str("socket", socketPath).
		Str("pubkey", srv.PubKey()).
		Str("policy_sha256", p.SHA256).
		Int("rules", len(p.Rules)).
		Msg("signer listening")

	if err := srv.Serve(ctx, l); err != nil {
		return common.RuntimeError(cmd, err)
	}
	log.Info().Msg("signer stopped")
	return nil
}

// resolveServeKey loads the signing key without ever accepting it in
// plaintext. It returns the key and any further encodings of it the guard
// should refuse (the ncryptsec it came from).
func resolveServeKey(cmd *cobra.Command, jsonMode bool, identity, passwordFile string) (string, []string, error) {
	password := ""
	if passwordFile != "" {
		data, err := os.ReadFile(passwordFile)
		if err != nil {
			return "", nil, common.NotFoundError(cmd, passwordFile, fmt.Errorf("--password-file: %w", err))
		}
		password = strings.TrimRight(string(data), "\r\n")
		viper.Set("vault.password", password)
	}

	if path, ok := strings.CutPrefix(identity, "file:"); ok {
		return loadKeyFile(cmd, path, password)
	}
	if strings.HasPrefix(identity, "nsec1") || strings.HasPrefix(identity, "ncryptsec1") {
		return "", nil, common.InvalidInputError(cmd, "", errors.New("refusing a private key on the command line, where other processes can read it; use a vault label or file:<ncryptsec file>"))
	}

	if identity == "" {
		identity = viper.GetString("signer.identity")
	}
	if identity == "" {
		entries, err := client.LoadVaultEntries()
		if err != nil {
			return "", nil, common.RuntimeError(cmd, err)
		}
		switch len(entries) {
		case 0:
			return "", nil, common.InvocationError(cmd, errors.New("--identity is required (or set NCLI_SIGNER_IDENTITY): no vault identity to fall back to"))
		case 1:
			identity = entries[0].Label
		default:
			return "", nil, common.InvocationError(cmd, fmt.Errorf("--identity is required (or set NCLI_SIGNER_IDENTITY): the vault has %d saved identities", len(entries)))
		}
	}

	resolved, err := client.ResolveIdentifier(identity)
	if err != nil {
		return "", nil, keyresolve.ClassifyIdentifierError(cmd, identity, err)
	}
	if !resolved.InVault {
		return "", nil, common.InvalidInputError(cmd, identity, errors.New("--identity must be a vault label or file:<ncryptsec file>"))
	}
	priv, err := keyresolve.ResolveSigningKey(cmd, jsonMode, resolved)
	if err != nil {
		return "", nil, err
	}
	return priv, nil, nil
}

func loadKeyFile(cmd *cobra.Command, path, password string) (string, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, common.NotFoundError(cmd, path, err)
	}
	key := ""
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			key = line
			break
		}
	}
	if !strings.HasPrefix(key, "ncryptsec1") {
		if looksLikePrivateKey(key) {
			return "", nil, common.InvalidInputError(cmd, path, errors.New("refusing a plaintext private key file; encrypt it as an ncryptsec (NIP-49)"))
		}
		return "", nil, common.InvalidInputError(cmd, path, errors.New("key file must hold an ncryptsec"))
	}
	if password == "" {
		password = viper.GetString("vault.password")
	}
	if password == "" {
		return "", nil, common.UsageError(cmd, errors.New("ncryptsec password required: --password-file or NCLI_VAULT_PASSWORD"))
	}
	priv, err := nip49.Decrypt(key, password)
	if err != nil {
		return "", nil, common.AuthError(cmd, fmt.Errorf("decrypting %s: %w", path, err))
	}
	return priv, []string{key}, nil
}

func looksLikePrivateKey(s string) bool {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "nsec1") || strings.HasPrefix(s, "ncryptsec1") {
		return true
	}
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}
