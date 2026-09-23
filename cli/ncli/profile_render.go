package ncli

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// The profile card reuses relay stats' palette (cli/relay/admin.go) so the
// two commands look like the same program.
var (
	profileTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#FAFAFA")).
				Background(lipgloss.Color("#7D56F4")).
				Padding(0, 1)

	profileNameStyle    = lipgloss.NewStyle().Bold(true)
	profileSectionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#7D56F4")).Bold(true)
	profileDimStyle     = lipgloss.NewStyle().Faint(true)
	profileOkStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#04B575"))
	profileWarnStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD100"))
	profileErrStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444"))
)

// notSet is what every empty field renders as. Printing the row anyway, with
// a visible placeholder, keeps the card's shape identical between a rich
// profile and an empty one -- so "this person published no relay list" reads
// differently from "this section didn't load".
var notSet = profileDimStyle.Render("— not set")

func renderProfile(v *profileView) {
	fmt.Println(profileTitleStyle.Render(" ncli profile "))
	fmt.Println()

	name := v.Metadata.Display()
	if name == "" {
		name = shortNpub(v.Npub)
	}
	fmt.Printf("  %s\n", profileNameStyle.Render(name))
	if about := strings.TrimSpace(firstLine(aboutOf(v))); about != "" {
		fmt.Printf("  %s\n", about)
	} else if v.Metadata == nil {
		fmt.Printf("  %s\n", profileDimStyle.Render("(no profile metadata published)"))
	}
	fmt.Println()

	fmt.Printf("  %s\n", profileSectionStyle.Render("Identity"))
	fmt.Printf("    %-12s %s\n", "nip-05", renderNip05(v.Nip05))
	fmt.Printf("    %-12s %s\n", "npub", v.Npub)
	fmt.Printf("    %-12s %s\n", "pubkey", v.PubKeyHex)
	fmt.Printf("    %-12s %s\n", "lightning", orNotSet(lightningOf(v), "⚡ "))
	fmt.Printf("    %-12s %s\n", "website", orNotSet(websiteOf(v), ""))
	fmt.Println()

	fmt.Printf("  %s      %s\n", profileSectionStyle.Render("Following"), renderFollowing(v))
	fmt.Println()

	renderRelaySection(v)
	fmt.Println()
	renderBlossomSection(v)
	fmt.Println()

	fmt.Printf("  %s\n", profileDimStyle.Render(footer(v)))
}

func renderRelaySection(v *profileView) {
	if len(v.Relays) == 0 {
		fmt.Printf("  %s         %s\n", profileSectionStyle.Render("Relays"),
			profileDimStyle.Render("— not published (no kind:10002)"))
		return
	}
	fmt.Printf("  %s\n", profileSectionStyle.Render(fmt.Sprintf("Relays (%d)", len(v.Relays))))
	for _, r := range v.Relays {
		fmt.Printf("    %s  %s\n", relayMarker(r.Read, r.Write), r.URL)
	}
}

func renderBlossomSection(v *profileView) {
	if len(v.Blossom) == 0 {
		fmt.Printf("  %s        %s\n", profileSectionStyle.Render("Blossom"),
			profileDimStyle.Render("— not published (no kind:10063)"))
		return
	}
	fmt.Printf("  %s\n", profileSectionStyle.Render(fmt.Sprintf("Blossom (%d)", len(v.Blossom))))
	for _, s := range v.Blossom {
		fmt.Printf("    •  %s\n", s)
	}
}

// relayMarker renders NIP-65's read/write markers: both directions, read
// only, or write only.
func relayMarker(read, write bool) string {
	switch {
	case read && write:
		return "↕"
	case read:
		return "↓"
	case write:
		return "↑"
	default:
		return "·"
	}
}

func renderNip05(s *nip05Status) string {
	if s == nil || s.Address == "" {
		return notSet
	}
	switch {
	case s.Error != "":
		return profileWarnStyle.Render("⚠ ") + s.Address + profileDimStyle.Render(" (could not verify)")
	case s.Verified:
		return profileOkStyle.Render("✔ ") + s.Address
	default:
		return profileErrStyle.Render("✘ ") + s.Address + profileDimStyle.Render(" (does not match this pubkey)")
	}
}

func renderFollowing(v *profileView) string {
	if v.Following == nil {
		return profileDimStyle.Render("— not published")
	}
	return withThousands(fmt.Sprintf("%d", *v.Following))
}

func footer(v *profileView) string {
	relays := fmt.Sprintf("queried %d relay(s)", v.Queried)
	if v.UpdatedAt == 0 {
		return relays + " · no kind:0 found"
	}
	return relays + " · profile updated " + time.Unix(int64(v.UpdatedAt), 0).Format("2006-01-02")
}

func orNotSet(value, prefix string) string {
	if value == "" {
		return notSet
	}
	return prefix + value
}

func lightningOf(v *profileView) string {
	if v.Metadata == nil {
		return ""
	}
	return v.Metadata.LightningAddress()
}

func websiteOf(v *profileView) string {
	if v.Metadata == nil {
		return ""
	}
	return v.Metadata.Website
}

func aboutOf(v *profileView) string {
	if v.Metadata == nil {
		return ""
	}
	return v.Metadata.About
}

// firstLine keeps a multi-paragraph "about" from taking over the card.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// A follow count is grouped with miner.go's withThousands rather than a
// second copy of the same logic.
