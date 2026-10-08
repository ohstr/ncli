package signer

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ohstr/nmilat/nip01"
)

// attestContext is the request-wide input to CheckAttestation.
type attestContext struct {
	Target    *nip01.Event
	Now       time.Time
	StartTime time.Time // zero: no before-start limit
	Skew      time.Duration
	SignerPub string
	Used      func(id string) bool
}

// CheckAttestation reports why att does not satisfy spec for the target,
// or nil if it does. Checks run in a fixed order so the reason is stable.
func checkAttestation(att *nip01.Event, spec *AttestationSpec, c attestContext) error {
	if att == nil {
		return errors.New("missing")
	}
	if err := att.Verify(); err != nil {
		return errors.New("bad signature")
	}
	if strings.EqualFold(att.PubKey, c.SignerPub) {
		return errors.New("signed by the signer itself")
	}
	if !containsInt(spec.Kinds, att.Kind) {
		return fmt.Errorf("wrong kind %d", att.Kind)
	}
	if !spec.authors[strings.ToLower(att.PubKey)] {
		return errors.New("author not allowed")
	}
	created := unixTime(att.CreatedAt)
	if created.After(c.Now.Add(c.Skew)) {
		return errors.New("future-dated")
	}
	if c.Now.Sub(created) > time.Duration(spec.MaxAge) {
		return fmt.Errorf("stale (older than %s)", time.Duration(spec.MaxAge))
	}
	if !c.StartTime.IsZero() && created.Before(c.StartTime.Add(-c.Skew)) {
		return errors.New("created before the signer started")
	}
	if c.Used != nil && c.Used(att.ID) {
		return errors.New("already used")
	}
	for i := range spec.Binds {
		if err := checkBind(&spec.Binds[i], att, c.Target); err != nil {
			return fmt.Errorf("bind #%d not satisfied: %w", i+1, err)
		}
	}
	if spec.bindsID && created.Add(c.Skew).Before(unixTime(c.Target.CreatedAt)) {
		return errors.New("predates the target event")
	}
	return nil
}

func checkBind(b *Bind, att, target *nip01.Event) error {
	switch {
	case b.Content != nil:
		want, err := expandTemplate(*b.Content, target)
		if err != nil {
			return err
		}
		got := strings.TrimSpace(att.Content)
		for _, w := range want {
			if got != strings.TrimSpace(w) {
				return errors.New("content does not match")
			}
		}
		return nil
	case b.ContentContains != nil:
		want, err := expandTemplate(*b.ContentContains, target)
		if err != nil {
			return err
		}
		for _, w := range want {
			if !strings.Contains(att.Content, w) {
				return errors.New("content does not contain the expected text")
			}
		}
		return nil
	}

	var want []string
	if b.From == "id" {
		want = []string{target.ID}
	} else {
		u := strings.TrimPrefix(b.From, "tag:")
		want = tagValues(target, u)
		if len(want) == 0 {
			return fmt.Errorf("target has no %q tag", u)
		}
	}
	have := map[string]bool{}
	for _, v := range tagValues(att, b.Tag) {
		have[v] = true
	}
	for _, w := range want {
		if !have[w] {
			return fmt.Errorf("missing tag [%q, %q]", b.Tag, w)
		}
	}
	return nil
}

// expandTemplate substitutes {id} and {tag:U}. A {tag:U} template expands
// once per value of the target's U tag.
func expandTemplate(t string, target *nip01.Event) ([]string, error) {
	base := strings.ReplaceAll(t, "{id}", target.ID)
	m := placeholderRe.FindStringSubmatch(base)
	if m == nil {
		return []string{base}, nil
	}
	u := strings.TrimPrefix(m[1], "tag:")
	vals := tagValues(target, u)
	if len(vals) == 0 {
		return nil, fmt.Errorf("target has no %q tag", u)
	}
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = strings.ReplaceAll(base, "{tag:"+u+"}", v)
	}
	return out, nil
}

// tagValues returns the first value of every tag named name.
func tagValues(ev *nip01.Event, name string) []string {
	var out []string
	for _, t := range ev.Tags {
		if len(t) >= 2 && t[0] == name {
			out = append(out, t[1])
		}
	}
	return out
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func unixTime(s uint64) time.Time {
	return time.Unix(int64(s), 0)
}
