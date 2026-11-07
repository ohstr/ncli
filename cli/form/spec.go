package form

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ohstr/nmilat/nippf"
)

// Spec is what a form is created from: the JSON an asker writes (the shape
// skills/ncli-forms documents). It only carries the form's
// parts; every validity rule is nippf's, applied when the form is built.
type Spec struct {
	Title      string         `json:"title"`
	Context    string         `json:"context,omitempty"`
	E          stringList     `json:"e,omitempty"`
	A          stringList     `json:"a,omitempty"`
	Responders []string       `json:"responders,omitempty"`
	Observers  []string       `json:"observers,omitempty"`
	Quorum     int            `json:"quorum,omitempty"`
	Priority   string         `json:"priority,omitempty"`
	EndsAt     int64          `json:"endsAt,omitempty"`
	Expiration int64          `json:"expiration,omitempty"`
	Results    string         `json:"results,omitempty"`
	Questions  []SpecQuestion `json:"questions"`
}

// SpecQuestion is one question of a Spec, in index order.
type SpecQuestion struct {
	Text     string       `json:"text"`
	Type     string       `json:"type"`
	Why      string       `json:"why,omitempty"`
	Options  []SpecOption `json:"options,omitempty"`
	Optional bool         `json:"optional,omitempty"`
	Default  stringList   `json:"default,omitempty"`
	Min      scalar       `json:"min,omitempty"`
	Max      scalar       `json:"max,omitempty"`
}

// SpecOption is one option of a choice question.
type SpecOption struct {
	ID           string `json:"id"`
	Label        string `json:"label"`
	Rec          bool   `json:"rec,omitempty"`
	NoteRequired bool   `json:"noteRequired,omitempty"`
	Detail       string `json:"detail,omitempty"`
}

// stringList reads a string or a list of strings (numbers kept as written).
type stringList []string

func (l *stringList) UnmarshalJSON(b []byte) error {
	var one scalar
	if err := one.UnmarshalJSON(b); err == nil {
		*l = stringList{string(one)}
		return nil
	}
	var many []scalar
	if err := json.Unmarshal(b, &many); err != nil {
		return errors.New("a string or a list of strings")
	}
	*l = nil
	for _, s := range many {
		*l = append(*l, string(s))
	}
	return nil
}

// scalar reads a string or a number, as the number is written.
type scalar string

func (s *scalar) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	var str string
	if err := json.Unmarshal(b, &str); err == nil {
		*s = scalar(str)
		return nil
	}
	var n json.Number
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(&n); err != nil {
		return errors.New("a string or a number")
	}
	*s = scalar(n.String())
	return nil
}

// ReadSpec reads a Spec; an unknown key is refused (a typo would otherwise
// drop a part of the form silently).
func ReadSpec(r io.Reader) (*Spec, error) {
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	var s Spec
	if err := d.Decode(&s); err != nil {
		return nil, fmt.Errorf("form spec: %w", err)
	}
	if d.More() {
		return nil, errors.New("form spec: more than one JSON value")
	}
	return &s, nil
}

// Form turns the spec into a form being built (no id, author or time yet).
// resolve turns a responder or observer (npub, hex, …) into hex. The form's
// validity is checked when it is turned into an event (nippf's Form.Event).
func (s *Spec) Form(resolve func(string) (string, error)) (*nippf.Form, error) {
	f := &nippf.Form{
		Title:      s.Title,
		Context:    s.Context,
		Events:     []string(s.E),
		Addrs:      []string(s.A),
		Quorum:     s.Quorum,
		EndsAt:     s.EndsAt,
		Expiration: s.Expiration,
	}
	switch s.Priority {
	case "":
	case "high":
		f.Priority = true
	default:
		return nil, fmt.Errorf("priority %q: only \"high\"", s.Priority)
	}
	switch s.Results {
	case "":
	case "all":
		f.ResultsAll = true
	default:
		return nil, fmt.Errorf("results %q: only \"all\"", s.Results)
	}
	if s.Quorum < 0 {
		return nil, fmt.Errorf("quorum %d: a positive number", s.Quorum)
	}
	for _, group := range []struct {
		keys []string
		role string
	}{{s.Responders, nippf.RoleResponder}, {s.Observers, nippf.RoleObserver}} {
		for _, k := range group.keys {
			hex, err := resolve(k)
			if err != nil {
				return nil, fmt.Errorf("%s %q: %w", group.role, k, err)
			}
			if hasParty(f, hex) {
				return nil, fmt.Errorf("%s %q is listed twice", group.role, k)
			}
			f.Parties = append(f.Parties, nippf.Party{PubKey: hex, Role: group.role})
		}
	}
	for i, q := range s.Questions {
		fq := nippf.Question{Index: i, Text: q.Text, Type: q.Type, Why: q.Why, Optional: q.Optional,
			Min: string(q.Min), Max: string(q.Max), Defaults: []string(q.Default)}
		for _, o := range q.Options {
			fq.Options = append(fq.Options, nippf.Option{ID: o.ID, Label: o.Label, Rec: o.Rec, NoteRequired: o.NoteRequired, Detail: o.Detail})
		}
		f.Questions = append(f.Questions, fq)
	}
	return f, nil
}

// AddResponders adds each key not already a party as a responder (a
// private form's --to).
func AddResponders(f *nippf.Form, keys []string) {
	for _, k := range keys {
		if !hasParty(f, k) {
			f.Parties = append(f.Parties, nippf.Party{PubKey: k, Role: nippf.RoleResponder})
		}
	}
}

func hasParty(f *nippf.Form, hex string) bool {
	for _, p := range f.Parties {
		if strings.EqualFold(p.PubKey, hex) {
			return true
		}
	}
	return false
}
