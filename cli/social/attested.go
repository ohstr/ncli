package social

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nipOA"
)

// Attestation is an event's NIP-OA auth tag, checked: the owner it names
// (hex, "" when the tag names no valid key) and whether it holds for this
// event -- the owner's signature over the event's key and the conditions,
// and the conditions met by the event's kind and created_at.
type Attestation struct {
	Owner   string
	Valid   bool
	Problem string // why it isn't valid
}

// CheckAttestation checks ev's auth tag; nil when it has none. An event with
// two or more auth tags has no valid one (NIP-OA).
func CheckAttestation(ev *nip01.Event) *Attestation {
	var raw []string
	n := 0
	for _, t := range ev.Tags {
		if len(t) > 0 && t[0] == nipOA.TagName {
			n++
			raw = t
		}
	}
	if n == 0 {
		return nil
	}
	a := &Attestation{}
	if len(raw) > 1 && hexKey.MatchString(raw[1]) {
		a.Owner = raw[1]
	}
	tag, err := nipOA.ParseAuthTag(ev.Tags, strings.ToLower(ev.PubKey))
	switch {
	case err != nil:
		a.Problem = err.Error()
	case !tag.Conditions.EvaluateKind(ev.Kind) || !tag.Conditions.EvaluateTimeClauses(ev.CreatedAt):
		a.Problem = "nipoa: the event doesn't meet the conditions " + fmt.Sprintf("%q", tag.Conditions.Raw)
	default:
		a.Owner, a.Valid = tag.OwnerPubkey, true
	}
	return a
}

var hexKey = regexp.MustCompile(`^[0-9a-f]{64}$`)

// AttestFlagsHelp describe --trust-owners and --attest-label.
const (
	TrustOwnersFlagHelp = "Owners whose NIP-OA attestations show as such: comma-separated npubs, hex, nprofiles, nip-05 or vault labels; also NCLI_TRUST_OWNERS"
	AttestLabelFlagHelp = `The word shown for a key a trusted owner attested, as "[<word> of <owner>]" (default "agent"); also NCLI_ATTEST_LABEL`
)

// AttestView is how text output shows attestations: the owners the reader
// trusts (hex) and the word for a key one of them attested.
type AttestView struct {
	Owners map[string]bool
	Word   string
}

// AddAttestFlags adds --trust-owners and --attest-label to cmd.
func AddAttestFlags(cmd *cobra.Command) {
	cmd.Flags().String("trust-owners", "", TrustOwnersFlagHelp)
	cmd.Flags().String("attest-label", "", AttestLabelFlagHelp)
}

var attestWord = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,23}$`)

// AttestViewOf reads --trust-owners (else NCLI_TRUST_OWNERS) and
// --attest-label (else NCLI_ATTEST_LABEL, else "agent"): a word of 1 to 24
// lowercase ASCII letters, digits and dashes.
func AttestViewOf(cmd *cobra.Command) (AttestView, error) {
	v := AttestView{Owners: map[string]bool{}, Word: "agent"}
	owners, from := "", "--trust-owners"
	if f := cmd.Flags().Lookup("trust-owners"); f != nil {
		owners = f.Value.String()
	}
	if owners == "" {
		owners, from = viper.GetString("trust_owners"), "NCLI_TRUST_OWNERS"
	}
	for _, o := range strings.Split(owners, ",") {
		if o = strings.TrimSpace(o); o == "" {
			continue
		}
		pk, err := keyresolve.Pubkey(cmd, o)
		if err != nil {
			msg := fmt.Errorf("%s: %q is no key (an npub, hex, nprofile, nip-05 or vault label)", from, common.RedactSecretInput(o))
			var ce *common.CLIError
			if errors.As(err, &ce) {
				ce.Err = fmt.Errorf("%w: %w", msg, ce.Err)
				return AttestView{}, ce
			}
			return AttestView{}, fmt.Errorf("%w: %w", msg, err)
		}
		v.Owners[strings.ToLower(pk)] = true
	}
	word, from := "", "--attest-label"
	if f := cmd.Flags().Lookup("attest-label"); f != nil {
		word = f.Value.String()
	}
	if word == "" {
		word, from = viper.GetString("attest_label"), "NCLI_ATTEST_LABEL"
	}
	if word != "" {
		if !attestWord.MatchString(word) {
			return AttestView{}, common.InvalidInputError(cmd, from, fmt.Errorf("%s: %q: 1 to 24 lowercase letters, digits or dashes", from, common.RedactSecretInput(word)))
		}
		v.Word = word
	}
	return v, nil
}

// Apply marks m's attestation trusted when it is valid and its owner is
// one the reader trusts, and sets the word text output shows for it.
func (v AttestView) Apply(m *Message) {
	m.AttestTrusted = Trusted(m.AttestedBy, m.AttestValid, v.Owners)
	m.attestWord = v.Word
}

// Trusted: valid, by an owner in owners.
func Trusted(owner string, valid *bool, owners map[string]bool) bool {
	return valid != nil && *valid && owners[owner]
}

// AttestColumn is what text output prints for an attestation, in a column
// of its own right after the author: "" for none; "[<word> of <owner npub>]"
// for a valid one by a trusted owner; "[attested by <owner npub>, owner not
// trusted]" for a valid one by any other; "[invalid attestation]" for a tag
// that doesn't hold. Only the client writes it: no field of the event's
// prints there, and the word is the reader's (AttestView).
func AttestColumn(owner string, valid *bool, trusted bool, word string) string {
	switch {
	case valid == nil:
		return ""
	case !*valid:
		return "[invalid attestation]"
	case trusted:
		if word == "" {
			word = "agent"
		}
		return "[" + word + " of " + Inline(Npub(owner)) + "]"
	}
	return "[attested by " + Inline(Npub(owner)) + ", owner not trusted]"
}

func setAttestation(m *Message, ev *nip01.Event) {
	if a := CheckAttestation(ev); a != nil {
		valid := a.Valid
		m.AttestedBy, m.AttestValid = a.Owner, &valid
	}
}
