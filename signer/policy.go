// Package signer is a local Nostr signer: it holds one key in memory and
// signs for other processes over a unix socket, gated by a policy.
//
// The policy engine is generic -- selectors on method, kind, counterpart
// and tags, plus "attestations" (other signed events a request must carry).
// It knows nothing about any particular NIP's semantics.
package signer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/nip46"
	"github.com/ohstr/nmilat/utils"
	"sigs.k8s.io/yaml"
)

// PolicyKind is the required value of a policy file's `kind:` field.
const PolicyKind = "signer-policy"

const (
	// DefaultMaxClockSkew bounds how far a sign_event's created_at may sit
	// from the signer's clock when no rule overrides it.
	DefaultMaxClockSkew = 10 * time.Minute
	// MaxAttestationAge caps an attestation's max_age. The used-attestation
	// set relies on it to prune safely.
	MaxAttestationAge = 24 * time.Hour
)

// Methods a rule selector may name.
var ruleMethods = map[string]bool{
	nip46.MethodSignEvent:    true,
	nip46.MethodNIP04Encrypt: true,
	nip46.MethodNIP04Decrypt: true,
	nip46.MethodNIP44Encrypt: true,
	nip46.MethodNIP44Decrypt: true,
}

// Duration is a time.Duration written as a Go duration string ("15m").
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string like \"15m\"")
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	if v < 0 {
		return fmt.Errorf("duration %q is negative", s)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// Selector picks the requests a rule applies to. Unset fields match
// anything; set fields must all match.
type Selector struct {
	Methods      []string            `json:"methods,omitempty"`
	Kinds        []int               `json:"kinds,omitempty"`
	Counterparts []string            `json:"counterparts,omitempty"`
	Tags         map[string][]string `json:"tags,omitempty"`

	counterparts map[string]bool
}

func (s *Selector) empty() bool {
	return len(s.Methods) == 0 && len(s.Kinds) == 0 && len(s.Counterparts) == 0 && len(s.Tags) == 0
}

// Bind ties an attestation to the target event. Exactly one form is set:
// Tag+From, Content, or ContentContains.
type Bind struct {
	Tag             string  `json:"tag,omitempty"`
	From            string  `json:"from,omitempty"`
	Content         *string `json:"content,omitempty"`
	ContentContains *string `json:"content_contains,omitempty"`
}

// bindsID reports whether b names the target's id, which fixes the target
// event exactly (and so its created_at).
func (b *Bind) bindsID() bool {
	switch {
	case b.Content != nil:
		return strings.Contains(*b.Content, "{id}")
	case b.ContentContains != nil:
		return strings.Contains(*b.ContentContains, "{id}")
	default:
		return b.From == "id"
	}
}

// AttestationSpec describes one signed event a request must carry.
type AttestationSpec struct {
	Name        string   `json:"name"`
	Kinds       []int    `json:"kinds"`
	Authors     []string `json:"authors,omitempty"`
	AuthorsFile string   `json:"authors_file,omitempty"`
	MaxAge      Duration `json:"max_age"`
	Binds       []Bind   `json:"binds"`

	authors    map[string]bool
	authorsSum string // sha256 of authors_file as read
	bindsID    bool
}

// Require lists what an allow rule needs beyond its selector.
type Require struct {
	Attestations []AttestationSpec `json:"attestations"`
}

// Rule is one policy entry. Exactly one of Allow or Deny is set.
type Rule struct {
	Name         string    `json:"name"`
	Allow        *Selector `json:"allow,omitempty"`
	Deny         *Selector `json:"deny,omitempty"`
	DenyMatching []string  `json:"deny_matching,omitempty"`
	Rate         string    `json:"rate,omitempty"`
	MaxClockSkew *Duration `json:"max_clock_skew,omitempty"`
	Require      *Require  `json:"require,omitempty"`

	denyRe []*regexp.Regexp
	rate   *Rate
}

func (r *Rule) selector() *Selector {
	if r.Allow != nil {
		return r.Allow
	}
	return r.Deny
}

// Policy is a loaded, validated signer policy.
type Policy struct {
	Kind         string    `json:"kind"`
	MaxClockSkew *Duration `json:"max_clock_skew,omitempty"`
	Rules        []Rule    `json:"rules"`

	// SHA256 is the hex digest of the policy file's bytes.
	SHA256 string `json:"-"`
	// Files lists the policy file and every authors_file it read, for
	// change detection; fileSums holds each one's sha256 as read.
	Files    []string `json:"-"`
	fileSums []string
}

// snapshot renders Files with the hashes they had when this policy was
// read, in hashFiles' format.
func (p *Policy) snapshot() string {
	parts := make([]string, len(p.Files))
	for i, f := range p.Files {
		parts[i] = f + "=" + p.fileSums[i]
	}
	return strings.Join(parts, "\n")
}

// Skew is the policy-wide max_clock_skew.
func (p *Policy) Skew() time.Duration {
	if p.MaxClockSkew != nil {
		return time.Duration(*p.MaxClockSkew)
	}
	return DefaultMaxClockSkew
}

func (p *Policy) ruleSkew(r *Rule) time.Duration {
	if r.MaxClockSkew != nil {
		return time.Duration(*r.MaxClockSkew)
	}
	return p.Skew()
}

// UsesAttestations reports whether any rule requires attestations.
func (p *Policy) UsesAttestations() bool {
	for i := range p.Rules {
		if r := p.Rules[i].Require; r != nil && len(r.Attestations) > 0 {
			return true
		}
	}
	return false
}

// LoadOptions tunes policy validation.
type LoadOptions struct {
	// AllowCatchAll permits an allow rule with an empty selector.
	AllowCatchAll bool
	// SignerPub, when set, rejects attestation authors equal to it.
	SignerPub string
}

// LoadPolicy reads and validates the policy at path. Relative
// authors_file paths resolve against the policy file's directory.
func LoadPolicy(path string, opts LoadOptions) (*Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p, err := ParsePolicy(data, filepath.Dir(path), opts)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	p.Files = append([]string{path}, p.Files...)
	p.fileSums = append([]string{p.SHA256}, p.fileSums...)
	return p, nil
}

// ParsePolicy validates a policy document. baseDir resolves relative
// authors_file paths.
func ParsePolicy(data []byte, baseDir string, opts LoadOptions) (*Policy, error) {
	var p Policy
	if err := yaml.UnmarshalStrict(data, &p); err != nil {
		return nil, err
	}
	if !strings.EqualFold(p.Kind, PolicyKind) {
		return nil, fmt.Errorf("kind must be %q, got %q", PolicyKind, p.Kind)
	}
	if len(p.Rules) == 0 {
		return nil, errors.New("rules: at least one rule is required")
	}
	sum := sha256.Sum256(data)
	p.SHA256 = hex.EncodeToString(sum[:])

	names := map[string]bool{}
	for i := range p.Rules {
		r := &p.Rules[i]
		if err := p.validateRule(r, baseDir, opts); err != nil {
			label := r.Name
			if label == "" {
				label = "#" + strconv.Itoa(i+1)
			}
			return nil, fmt.Errorf("rule %s: %w", label, err)
		}
		if names[r.Name] {
			return nil, fmt.Errorf("rule %s: duplicate name", r.Name)
		}
		names[r.Name] = true
	}
	return &p, nil
}

func (p *Policy) validateRule(r *Rule, baseDir string, opts LoadOptions) error {
	if r.Name == "" {
		return errors.New("name is required")
	}
	if (r.Allow == nil) == (r.Deny == nil) {
		return errors.New("exactly one of allow or deny is required")
	}
	sel := r.selector()
	if r.Allow != nil && sel.empty() && !opts.AllowCatchAll {
		return errors.New("allow rule with an empty selector matches every request; narrow it or start the signer with --allow-catch-all")
	}
	if err := validateSelector(sel); err != nil {
		return err
	}
	if r.Deny != nil && (len(r.DenyMatching) > 0 || r.Rate != "" || r.Require != nil || r.MaxClockSkew != nil) {
		return errors.New("deny rules take only a selector")
	}

	for i, pat := range r.DenyMatching {
		re, err := regexp.Compile(pat)
		if err != nil {
			return fmt.Errorf("deny_matching #%d: %w", i+1, err)
		}
		r.denyRe = append(r.denyRe, re)
	}
	if r.Rate != "" {
		rate, err := ParseRate(r.Rate)
		if err != nil {
			return err
		}
		r.rate = &rate
	}

	if r.Require != nil {
		if len(r.Require.Attestations) == 0 {
			return errors.New("require: attestations is empty")
		}
		if !onlySignEvent(sel) {
			return errors.New("require: attestations needs a selector limited to sign_event")
		}
		seen := map[string]bool{}
		for i := range r.Require.Attestations {
			a := &r.Require.Attestations[i]
			if err := validateAttestation(a, baseDir, opts); err != nil {
				return fmt.Errorf("attestation %s: %w", attLabel(a, i), err)
			}
			if seen[a.Name] {
				return fmt.Errorf("attestation %s: duplicate name", a.Name)
			}
			seen[a.Name] = true
			if a.AuthorsFile != "" {
				p.Files = append(p.Files, resolvePath(baseDir, a.AuthorsFile))
				p.fileSums = append(p.fileSums, a.authorsSum)
			}
		}
	}
	return nil
}

func attLabel(a *AttestationSpec, i int) string {
	if a.Name != "" {
		return a.Name
	}
	return "#" + strconv.Itoa(i+1)
}

func validateSelector(s *Selector) error {
	for _, m := range s.Methods {
		if !ruleMethods[m] {
			return fmt.Errorf("unknown method %q (want sign_event, nip04_encrypt, nip04_decrypt, nip44_encrypt or nip44_decrypt)", m)
		}
	}
	eventFields := len(s.Kinds) > 0 || len(s.Tags) > 0
	if len(s.Methods) == 0 {
		switch {
		case eventFields && len(s.Counterparts) > 0:
			return errors.New("kinds/tags apply to sign_event and counterparts to encrypt/decrypt; split them into two rules")
		case eventFields:
			s.Methods = []string{nip46.MethodSignEvent}
		case len(s.Counterparts) > 0:
			s.Methods = []string{nip46.MethodNIP04Encrypt, nip46.MethodNIP04Decrypt, nip46.MethodNIP44Encrypt, nip46.MethodNIP44Decrypt}
		}
	} else {
		for _, m := range s.Methods {
			if eventFields && m != nip46.MethodSignEvent {
				return fmt.Errorf("kinds/tags only apply to sign_event, not %s", m)
			}
			if len(s.Counterparts) > 0 && m == nip46.MethodSignEvent {
				return errors.New("counterparts only apply to encrypt/decrypt, not sign_event")
			}
		}
	}
	for name, vals := range s.Tags {
		if name == "" || len(vals) == 0 {
			return fmt.Errorf("tags: %q needs at least one value", name)
		}
	}
	if len(s.Counterparts) > 0 {
		s.counterparts = map[string]bool{}
		for _, c := range s.Counterparts {
			hexKey, err := ParsePubkey(c)
			if err != nil {
				return fmt.Errorf("counterparts: %w", err)
			}
			s.counterparts[hexKey] = true
		}
	}
	return nil
}

func onlySignEvent(s *Selector) bool {
	return len(s.Methods) == 1 && s.Methods[0] == nip46.MethodSignEvent
}

var placeholderRe = regexp.MustCompile(`\{([^{}]*)\}`)

func validateAttestation(a *AttestationSpec, baseDir string, opts LoadOptions) error {
	if a.Name == "" {
		return errors.New("name is required")
	}
	if len(a.Kinds) == 0 {
		return errors.New("kinds is required")
	}
	if a.MaxAge <= 0 {
		return errors.New("max_age is required")
	}
	if time.Duration(a.MaxAge) > MaxAttestationAge {
		return fmt.Errorf("max_age %s exceeds the %s cap", time.Duration(a.MaxAge), MaxAttestationAge)
	}
	if len(a.Binds) == 0 {
		return errors.New("binds is required: an unbound attestation would approve any event")
	}

	a.authors = map[string]bool{}
	for _, au := range a.Authors {
		hexKey, err := ParsePubkey(au)
		if err != nil {
			return fmt.Errorf("authors: %w", err)
		}
		a.authors[hexKey] = true
	}
	if a.AuthorsFile != "" {
		keys, sum, err := readAuthorsFile(resolvePath(baseDir, a.AuthorsFile))
		if err != nil {
			return err
		}
		a.authorsSum = sum
		for _, k := range keys {
			a.authors[k] = true
		}
	}
	if len(a.authors) == 0 {
		return errors.New("authors or authors_file must name at least one key")
	}
	if opts.SignerPub != "" && a.authors[strings.ToLower(opts.SignerPub)] {
		return errors.New("authors include the signer's own key, which never counts as an attestation")
	}

	for i := range a.Binds {
		if err := validateBind(&a.Binds[i]); err != nil {
			return fmt.Errorf("binds #%d: %w", i+1, err)
		}
		if a.Binds[i].bindsID() {
			a.bindsID = true
		}
	}
	return nil
}

func validateBind(b *Bind) error {
	forms := 0
	if b.Tag != "" || b.From != "" {
		forms++
	}
	if b.Content != nil {
		forms++
	}
	if b.ContentContains != nil {
		forms++
	}
	if forms != 1 {
		return errors.New("use exactly one of {tag, from}, content or content_contains")
	}
	switch {
	case b.Content != nil:
		return validateTemplate(*b.Content)
	case b.ContentContains != nil:
		return validateTemplate(*b.ContentContains)
	}
	if b.Tag == "" || b.From == "" {
		return errors.New("tag and from are both required")
	}
	if b.From != "id" {
		if u, ok := strings.CutPrefix(b.From, "tag:"); !ok || u == "" {
			return fmt.Errorf("from %q: want \"id\" or \"tag:<name>\"", b.From)
		}
	}
	return nil
}

func validateTemplate(t string) error {
	if strings.TrimSpace(t) == "" {
		return errors.New("template is empty")
	}
	tagName := ""
	for _, m := range placeholderRe.FindAllStringSubmatch(t, -1) {
		ph := m[1]
		if ph == "id" {
			continue
		}
		u, ok := strings.CutPrefix(ph, "tag:")
		if !ok || u == "" {
			return fmt.Errorf("unknown placeholder {%s} (want {id} or {tag:<name>})", ph)
		}
		if tagName != "" && tagName != u {
			return errors.New("a template may reference only one tag name")
		}
		tagName = u
	}
	return nil
}

func resolvePath(baseDir, p string) string {
	if filepath.IsAbs(p) || baseDir == "" {
		return p
	}
	return filepath.Join(baseDir, p)
}

// readAuthorsFile reads one npub or hex pubkey per line. Blank lines and
// lines starting with # are skipped; anything else that isn't a key fails
// the load.
func readAuthorsFile(path string) ([]string, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("authors_file: %w", err)
	}
	sum := sha256.Sum256(data)
	var keys []string
	for i, line := range bytes.Split(data, []byte("\n")) {
		s := strings.TrimSpace(string(line))
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		k, err := ParsePubkey(s)
		if err != nil {
			return nil, "", fmt.Errorf("authors_file %s line %d: %w", path, i+1, err)
		}
		keys = append(keys, k)
	}
	return keys, hex.EncodeToString(sum[:]), nil
}

// ParsePubkey accepts an npub or 64-char hex pubkey and returns lowercase hex.
func ParsePubkey(s string) (string, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "npub1") {
		h, err := nip19.DecodePublicKey(s)
		if err != nil {
			return "", fmt.Errorf("invalid npub %q", s)
		}
		return strings.ToLower(h), nil
	}
	if err := utils.Validate32Key(s); err != nil {
		return "", fmt.Errorf("invalid pubkey %q (want npub or 64-char hex)", s)
	}
	return strings.ToLower(s), nil
}

// Rate is a per-rule limit of N requests per Per.
type Rate struct {
	N   int
	Per time.Duration
}

// ParseRate parses "60/m", "10/s" or "500/h".
func ParseRate(s string) (Rate, error) {
	n, unit, ok := strings.Cut(strings.TrimSpace(s), "/")
	if !ok {
		return Rate{}, fmt.Errorf("rate %q: want N/s, N/m or N/h", s)
	}
	count, err := strconv.Atoi(n)
	if err != nil || count <= 0 {
		return Rate{}, fmt.Errorf("rate %q: count must be a positive integer", s)
	}
	var per time.Duration
	switch unit {
	case "s":
		per = time.Second
	case "m":
		per = time.Minute
	case "h":
		per = time.Hour
	default:
		return Rate{}, fmt.Errorf("rate %q: unit must be s, m or h", s)
	}
	return Rate{N: count, Per: per}, nil
}
