// Package delegate implements the `ncli delegate` interactive wizard
// for creating and signing NIP-26 delegation tokens.
package delegate

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/flokiorg/go-flokicoin/crypto"
	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip26"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/term"
)

func NewDelegateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delegate",
		Short: "Generate a NIP-26 delegation token",
		Long: `Launch an interactive wizard that creates and signs NIP-26 delegation
tokens. With --issuer set (via flag or the NCLI_DELEGATE_ISSUER environment
variable), skips the wizard entirely and generates the token
non-interactively instead -- suitable for scripts or an AI agent.

--issuer and --delegatee both accept a vault label, an nsec, an npub, a hex
pubkey, an nprofile, or a nip-05 address -- the same identifier shapes "id
sign --identity" accepts (resolved via NCLI_VAULT_PASSWORD for a vault
label, same as "id sign"). Both must resolve to a private key: a
pubkey-only identity (npub/hex/nprofile/nip-05, not vault-saved) has
nothing to sign or derive a delegatee key from and is rejected.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// No config reload here: root's InitConfig (cli/ncli/root.go)
			// already loaded it once via the nearest ancestor's
			// PersistentPreRun, before this RunE runs.
			issuer, _ := cmd.Flags().GetString("issuer")
			if issuer == "" {
				issuer = viper.GetString("delegate.issuer")
			}
			if issuer == "" {
				// The wizard takes over the terminal via bubbletea, which
				// needs a real tty on both ends -- an agent invoking this
				// non-interactively (or any --json caller, which must never
				// get a TUI instead of a structured result/error) would
				// otherwise hang or get garbled output deep inside
				// bubbletea's screen init, the same failure mode apply's
				// headless() check (client/client.go) and id.go's
				// resolveVaultPassword exist to avoid.
				jsonMode, _ := cmd.Flags().GetBool("json")
				interactive := term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
				if jsonMode || !interactive {
					return common.UsageError(cmd, errors.New("--issuer is required (or set NCLI_DELEGATE_ISSUER) when not running interactively"))
				}
				if err := RunWizard(); err != nil {
					return common.RuntimeError(cmd, err)
				}
				return nil
			}

			return runNonInteractive(cmd, issuer)
		},
	}

	cmd.Flags().String("issuer", "", "Issuer identity to sign with -- vault label, nsec, npub, hex, nprofile, or nip-05 (must resolve to a private key); also settable via NCLI_DELEGATE_ISSUER. Providing this skips the interactive wizard.")
	cmd.Flags().String("delegatee", "", "Delegatee identity being granted authority -- same accepted shapes as --issuer (must resolve to a private key)")
	cmd.Flags().String("kinds", "", `Comma-separated event kinds to delegate (default: "25521", Top Zapped)`)
	cmd.Flags().Int("duration", 365, "Validity duration in days")
	return cmd
}

// runNonInteractive mirrors model.process()'s logic (the wizard's signing
// step) without any TUI involved, for scripted/agent use.
func runNonInteractive(cmd *cobra.Command, issuer string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")

	delegatee, _ := cmd.Flags().GetString("delegatee")
	if delegatee == "" {
		return common.UsageError(cmd, errors.New("--delegatee is required"))
	}

	issuerPrivKeyHex, issuerPubHex, err := resolveDelegationKey(cmd, jsonMode, issuer)
	if err != nil {
		return err
	}
	_, delegateePubHex, err := resolveDelegationKey(cmd, jsonMode, delegatee)
	if err != nil {
		return err
	}

	kindsFlag, _ := cmd.Flags().GetString("kinds")
	kinds := []string{"25521"}
	if kindsFlag != "" {
		kinds = nil
		for _, p := range strings.Split(kindsFlag, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if _, err := strconv.Atoi(p); err != nil {
				return common.InvalidInputError(cmd, p, fmt.Errorf("invalid kind %q: must be an integer", p))
			}
			kinds = append(kinds, p)
		}
		if len(kinds) == 0 {
			return common.InvalidInputError(cmd, kindsFlag, errors.New("--kinds must contain at least one integer kind"))
		}
	}

	durationDays, _ := cmd.Flags().GetInt("duration")
	if durationDays <= 0 {
		durationDays = 365
	}

	expiry := time.Now().Unix() + int64(durationDays*24*3600)
	conditions := fmt.Sprintf("kind=%s&created_at<%d", strings.Join(kinds, ","), expiry)

	token, err := nip26.SignDelegationToken(issuerPrivKeyHex, delegateePubHex, conditions)
	if err != nil {
		// input intentionally left blank -- issuerPrivKeyHex is private-key
		// material and must never be echoed back, even malformed.
		return common.InvalidInputError(cmd, "", err)
	}

	if jsonMode {
		common.PrintJSON(map[string]any{
			"issuer_pubkey":    issuerPubHex,
			"delegatee_pubkey": delegateePubHex,
			"conditions":       conditions,
			"token":            token,
		})
		return nil
	}

	fmt.Println("Delegation token generated.")
	fmt.Println()
	fmt.Println("issuer:    ", issuerPubHex)
	fmt.Println("delegatee: ", delegateePubHex)
	fmt.Println("conditions:", conditions)
	fmt.Println("token:     ", token)
	fmt.Println()
	fmt.Println("Attach to events signed by the delegatee's key as a tag:")
	fmt.Printf("  [\"delegation\", %q, %q, %q]\n", issuerPubHex, conditions, token)
	return nil
}

// resolveDelegationKey resolves keyOrIdentity -- a vault label, an npub/
// hex/nprofile/nip-05, or a raw nsec/hex private key -- to the private key
// and pubkey needed to build a delegation token, the same identifier shapes
// "id sign --identity" accepts. A pubkey-only identity (not vault-saved)
// has no private key and is rejected: unlike "miner mine --identity", which
// tolerates a pubkey-only identity and just leaves the event unsigned, both
// the issuer and delegatee side here always need real key material -- the
// issuer to actually sign, the delegatee because a bare pubkey with no
// vault/nsec behind it isn't distinguishable from a typo.
func resolveDelegationKey(cmd *cobra.Command, jsonMode bool, keyOrIdentity string) (privKeyHex, pubKeyHex string, err error) {
	resolved, err := client.ResolveIdentifier(keyOrIdentity)
	if err != nil {
		return "", "", keyresolve.ClassifyIdentifierError(cmd, keyOrIdentity, err)
	}

	privKeyHex, err = keyresolve.ResolveSigningKey(cmd, jsonMode, resolved)
	if err != nil {
		return "", "", err
	}
	if privKeyHex == "" {
		return "", "", common.AuthError(cmd, fmt.Errorf("identity %q has no private key available", common.RedactSecretInput(keyOrIdentity)))
	}
	return privKeyHex, resolved.PubKeyHex, nil
}

// Model for the wizard
type state int

const (
	stateIssuerKey state = iota
	stateDelegateeKey
	stateKinds
	stateCustomKinds
	stateDuration
	stateResult
)

type model struct {
	state             state
	issuerInput       textinput.Model
	delegateeInput    textinput.Model
	durationInput     textinput.Model
	customKindInput   textinput.Model
	kinds             []kindOption
	cursor            int
	selectedKinds     map[int]struct{}
	err               error
	genToken          string
	genConditions     string
	genDelegateePub   string
	realDelegateePriv string
	realIssuerPriv    string
}

type kindOption struct {
	name string
	kind string
}

func initialModel() model {
	ti := textinput.New()
	ti.Placeholder = "nsec... or hex..."
	ti.Focus()
	ti.CharLimit = 156
	ti.Width = 64

	dei := textinput.New()
	dei.Placeholder = "nsec... or hex..."
	dei.CharLimit = 156
	dei.Width = 64

	ki := textinput.New()
	ki.Placeholder = "10002, 30023"
	ki.CharLimit = 100
	ki.Width = 40

	di := textinput.New()
	di.Placeholder = "365"
	di.SetValue("365")
	di.CharLimit = 5
	di.Width = 10

	return model{
		state:           stateIssuerKey,
		issuerInput:     ti,
		delegateeInput:  dei,
		customKindInput: ki,
		durationInput:   di,
		kinds: []kindOption{
			{"Metadata", "0"},
			{"Text Note", "1"},
			{"Contact List", "3"},
			{"Direct Message", "4"},
			{"Zap Request", "9734"},
			{"Top Zapped Stats", "25521"},
			{"Other (manual entry)", "custom"},
		},
		selectedKinds: map[int]struct{}{5: {}}, // Default to Top Zapped
	}
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			return m, tea.Quit
		case "enter":
			switch m.state {
			case stateIssuerKey:
				if m.issuerInput.Value() == "" {
					return m, nil
				}
				m.state = stateDelegateeKey
				m.delegateeInput.Focus()
			case stateDelegateeKey:
				if m.delegateeInput.Value() == "" {
					return m, nil
				}
				m.state = stateKinds
			case stateKinds:
				hasOther := false
				for idx := range m.selectedKinds {
					if m.kinds[idx].kind == "custom" {
						hasOther = true
						break
					}
				}
				if hasOther {
					m.state = stateCustomKinds
					m.customKindInput.Focus()
				} else {
					m.state = stateDuration
					m.durationInput.Focus()
				}
			case stateCustomKinds:
				m.state = stateDuration
				m.durationInput.Focus()
			case stateDuration:
				return m.process()
			}
		case "up", "k":
			if m.state == stateKinds && m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.state == stateKinds && m.cursor < len(m.kinds)-1 {
				m.cursor++
			}
		case "space":
			if m.state == stateKinds {
				if _, ok := m.selectedKinds[m.cursor]; ok {
					delete(m.selectedKinds, m.cursor)
				} else {
					m.selectedKinds[m.cursor] = struct{}{}
				}
			}
		}
	}

	// Update inputs
	switch m.state {
	case stateIssuerKey:
		m.issuerInput, cmd = m.issuerInput.Update(msg)
	case stateDelegateeKey:
		m.delegateeInput, cmd = m.delegateeInput.Update(msg)
	case stateCustomKinds:
		m.customKindInput, cmd = m.customKindInput.Update(msg)
	case stateDuration:
		m.durationInput, cmd = m.durationInput.Update(msg)
	}

	return m, cmd
}

func (m model) process() (tea.Model, tea.Cmd) {
	m.realIssuerPriv = common.NormalizeKey(m.issuerInput.Value())
	m.realDelegateePriv = common.NormalizeKey(m.delegateeInput.Value())

	delegateePubHex, err := derivePubkey(m.realDelegateePriv)
	if err != nil {
		m.err = err
		return m, nil
	}
	m.genDelegateePub = delegateePubHex

	durationDays, _ := strconv.Atoi(m.durationInput.Value())
	if durationDays == 0 {
		durationDays = 365
	}

	var kinds []string
	for idx := range m.selectedKinds {
		k := m.kinds[idx].kind
		if k != "custom" {
			kinds = append(kinds, k)
		}
	}

	if m.customKindInput.Value() != "" {
		parts := strings.Split(m.customKindInput.Value(), ",")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if _, err := strconv.Atoi(p); err == nil {
				kinds = append(kinds, p)
			}
		}
	}

	now := time.Now().Unix()
	expiry := now + int64(durationDays*24*3600)
	conditions := fmt.Sprintf("kind=%s&created_at<%d", strings.Join(kinds, ","), expiry)
	m.genConditions = conditions

	token, err := nip26.SignDelegationToken(m.realIssuerPriv, delegateePubHex, conditions)
	if err != nil {
		m.err = err
		return m, nil
	}
	m.genToken = token
	m.state = stateResult
	return m, nil
}

func (m model) View() string {
	var s string

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#7D56F4")).
		MarginBottom(1)

	header := titleStyle.Render("Nostr Delegation Wizard (NIP-26)")

	switch m.state {
	case stateIssuerKey:
		s = fmt.Sprintf(
			"%s\n\nStep 1: Enter your main identity private key\n%s\n\n(This stays local and is used only to sign the token)",
			header,
			m.issuerInput.View(),
		)
	case stateDelegateeKey:
		s = fmt.Sprintf(
			"%s\n\nStep 2: Enter the delegatee identity's private key\n%s",
			header,
			m.delegateeInput.View(),
		)
	case stateKinds:
		var kindList strings.Builder
		kindList.WriteString(header + "\n\nStep 3: Select event kinds to delegate (Space to toggle, Enter to continue):\n\n")
		for i, choice := range m.kinds {
			cursor := " "
			if m.cursor == i {
				cursor = ">"
			}
			checked := " "
			if _, ok := m.selectedKinds[i]; ok {
				checked = "x"
			}
			kindList.WriteString(fmt.Sprintf("%s [%s] %s\n", cursor, checked, choice.name))
		}
		s = kindList.String()
	case stateCustomKinds:
		s = fmt.Sprintf(
			"%s\n\nStep 3b: Enter comma-separated additional kinds\n%s",
			header,
			m.customKindInput.View(),
		)
	case stateDuration:
		s = fmt.Sprintf(
			"%s\n\nStep 4: Enter validity duration (days)\n%s",
			header,
			m.durationInput.View(),
		)
	case stateResult:
		if m.err != nil {
			s = fmt.Sprintf("%s\n\n[Error] %v\n\nPress Q to quit.", header, m.err)
		} else {
			resStyle := lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#04B575")).
				Padding(1).
				MarginTop(1)

			issuerPub := tryToPubkey(m.realIssuerPriv)
			content := fmt.Sprintf(
				"[✅] Delegation Token Generated!\n\nissuer:     %s\ndelegatee:  %s\nconditions: %s\ntoken:      %s\n\nAttach to events signed by the delegatee's key as a tag:\n[\"delegation\", %q, %q, %q]",
				issuerPub,
				m.genDelegateePub,
				m.genConditions,
				m.genToken,
				issuerPub,
				m.genConditions,
				m.genToken,
			)
			s = header + "\n" + resStyle.Render(content) + "\n\nPress ESC or Q to exit."
		}
	}

	return s + "\n"
}

func RunWizard() error {
	p := tea.NewProgram(initialModel())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("wizard failed: %w", err)
	}
	return nil
}

func derivePubkey(privHex string) (string, error) {
	b, err := hex.DecodeString(privHex)
	if err != nil {
		return "", err
	}
	if len(b) != 32 {
		return "", errors.New("invalid private key length")
	}

	_, pubKey := crypto.PrivKeyFromBytes(b)
	publicKeyBytes := pubKey.SerializeCompressed()
	return hex.EncodeToString(publicKeyBytes[1:]), nil
}

func tryToPubkey(privHex string) string {
	pk, err := derivePubkey(privHex)
	if err != nil {
		return "unknown"
	}
	return pk
}
