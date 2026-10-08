package signer

import (
	"fmt"
	"strings"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip46"
)

// Request is everything a policy decision depends on.
type Request struct {
	Method string
	// Event is the sign_event target, with PubKey set to the signer and ID
	// computed.
	Event *nip01.Event
	// Counterpart is the peer pubkey (hex) of an encrypt/decrypt request.
	Counterpart string
	// Plaintext is an *_encrypt request's input, checked by deny_matching.
	Plaintext    string
	Attestations []*nip01.Event
	Now          time.Time
	// StartTime is when the signer started; attestations created earlier
	// never count. Zero disables the check.
	StartTime time.Time
	SignerPub string
}

// Decision is Evaluate's verdict.
type Decision struct {
	Allow  bool
	Rule   string
	Reason string
	// Consumed lists the attestation ids to mark used if the request
	// goes through.
	Consumed []string
	// Rate is the matched rule's limit, nil if none.
	Rate *Rate
}

func deny(rule, format string, args ...any) Decision {
	return Decision{Rule: rule, Reason: fmt.Sprintf(format, args...)}
}

// Evaluate decides req against p. It is a pure function of its inputs;
// used reports whether an attestation id was already consumed. Rate limits
// and the key guard are applied by the caller.
func Evaluate(req Request, p *Policy, used func(id string) bool) Decision {
	if !ruleMethods[req.Method] {
		return deny("", "unsupported method %s", req.Method)
	}
	if req.Method == nip46.MethodSignEvent {
		if req.Event == nil {
			return deny("", "missing event")
		}
		if !strings.EqualFold(req.Event.PubKey, req.SignerPub) {
			return deny("", "event pubkey does not match the signer")
		}
		if unixTime(req.Event.CreatedAt).After(req.Now.Add(p.Skew())) {
			return deny("", "event created_at is in the future")
		}
	}

	var rule *Rule
	for i := range p.Rules {
		if matches(p.Rules[i].selector(), &req) {
			rule = &p.Rules[i]
			break
		}
	}
	if rule == nil {
		return deny("", "no rule matches")
	}
	if rule.Deny != nil {
		return deny(rule.Name, "denied by rule")
	}

	if reason := matchDenyPatterns(rule, &req); reason != "" {
		return deny(rule.Name, "%s", reason)
	}

	var consumed []string
	maxAge := p.ruleSkew(rule)
	if req.Method == nip46.MethodSignEvent && rule.Require != nil {
		ctx := attestContext{
			Target:    req.Event,
			Now:       req.Now,
			StartTime: req.StartTime,
			Skew:      p.ruleSkew(rule),
			SignerPub: req.SignerPub,
			Used:      used,
		}
		picked, reason := assignAttestations(rule.Require.Attestations, req.Attestations, ctx)
		if reason != "" {
			return deny(rule.Name, "%s", reason)
		}
		for i, spec := range rule.Require.Attestations {
			consumed = append(consumed, req.Attestations[picked[i]].ID)
			if spec.bindsID && time.Duration(spec.MaxAge) > maxAge {
				maxAge = time.Duration(spec.MaxAge)
			}
		}
	}

	if req.Method == nip46.MethodSignEvent {
		created := unixTime(req.Event.CreatedAt)
		if created.After(req.Now.Add(p.ruleSkew(rule))) {
			return deny(rule.Name, "event created_at is in the future")
		}
		if req.Now.Sub(created) > maxAge {
			return deny(rule.Name, "event created_at is too old (limit %s)", maxAge)
		}
	}

	return Decision{Allow: true, Rule: rule.Name, Consumed: consumed, Rate: rule.rate}
}

func matches(s *Selector, req *Request) bool {
	if len(s.Methods) > 0 && !containsString(s.Methods, req.Method) {
		return false
	}
	if s.counterparts != nil && !s.counterparts[strings.ToLower(req.Counterpart)] {
		return false
	}
	if req.Method != nip46.MethodSignEvent {
		return true
	}
	if len(s.Kinds) > 0 && !containsInt(s.Kinds, req.Event.Kind) {
		return false
	}
	for name, patterns := range s.Tags {
		if !anyTagMatches(tagValues(req.Event, name), patterns) {
			return false
		}
	}
	return true
}

func anyTagMatches(values, patterns []string) bool {
	for _, v := range values {
		for _, p := range patterns {
			if prefix, ok := strings.CutSuffix(p, "*"); ok {
				if strings.HasPrefix(v, prefix) {
					return true
				}
			} else if v == p {
				return true
			}
		}
	}
	return false
}

func matchDenyPatterns(rule *Rule, req *Request) string {
	if len(rule.denyRe) == 0 {
		return ""
	}
	for i, re := range rule.denyRe {
		if req.Method == nip46.MethodSignEvent {
			if re.MatchString(req.Event.Content) {
				return fmt.Sprintf("content matches deny_matching pattern #%d", i+1)
			}
			for _, t := range req.Event.Tags {
				for _, v := range t {
					if re.MatchString(v) {
						return fmt.Sprintf("tag value matches deny_matching pattern #%d", i+1)
					}
				}
			}
		} else if re.MatchString(req.Plaintext) {
			return fmt.Sprintf("plaintext matches deny_matching pattern #%d", i+1)
		}
	}
	return ""
}

// assignAttestations matches each spec to a distinct attestation,
// backtracking so an event that fits several specs doesn't starve a later
// one. It returns the chosen index per spec, or the reason the first
// unsatisfiable spec failed.
func assignAttestations(specs []AttestationSpec, atts []*nip01.Event, ctx attestContext) ([]int, string) {
	// ok[s][a] is nil when attestation a satisfies spec s.
	ok := make([][]error, len(specs))
	for s := range specs {
		ok[s] = make([]error, len(atts))
		for a := range atts {
			ok[s][a] = checkAttestation(atts[a], &specs[s], ctx)
		}
	}

	picked := make([]int, len(specs))
	taken := make([]bool, len(atts))
	var solve func(s int) bool
	solve = func(s int) bool {
		if s == len(specs) {
			return true
		}
		for a := range atts {
			if taken[a] || ok[s][a] != nil {
				continue
			}
			taken[a], picked[s] = true, a
			if solve(s + 1) {
				return true
			}
			taken[a] = false
		}
		return false
	}
	if solve(0) {
		return picked, ""
	}

	for s := range specs {
		var reason error
		satisfiable := false
		for a := range atts {
			if ok[s][a] == nil {
				satisfiable = true
				break
			}
			// Prefer the failure of an attestation that at least has the
			// right kind: it's the one the caller meant.
			if reason == nil || containsInt(specs[s].Kinds, atts[a].Kind) {
				reason = ok[s][a]
			}
		}
		if satisfiable {
			continue
		}
		if reason == nil {
			return nil, fmt.Sprintf("attestation %q required", specs[s].Name)
		}
		return nil, fmt.Sprintf("attestation %q: %v", specs[s].Name, reason)
	}
	return nil, "attestations: one event cannot satisfy more than one requirement"
}

func containsString(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
