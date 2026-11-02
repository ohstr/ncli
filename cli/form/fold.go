package form

import (
	"errors"
	"slices"
	"strings"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/social"
	"github.com/ohstr/nmilat/nippf"
	"github.com/spf13/cobra"
)

// View is one form as these commands show it: the form, where it lives, its
// state and closing result, the response kept per responder, the tally,
// mine, and who hasn't answered. Every rule behind it is nippf's.
type View struct {
	Form       *nippf.Form       `json:"form"`
	Where      string            `json:"where"` // private, group or public
	State      nippf.State       `json:"state"`
	Role       string            `json:"role,omitempty"` // author, responder, observer; "" for none
	CanRespond bool              `json:"can_respond"`
	Result     *nippf.Result     `json:"result,omitempty"`
	Responses  []*nippf.Response `json:"responses"`
	Tally      *nippf.Tally      `json:"tally"`
	Mine       *nippf.Response   `json:"mine,omitempty"`
	Waiting    []string          `json:"waiting,omitempty"`
	// Labels: the form event's t tags, as its author set them. They are the
	// author's word, like the rest of the form: a label names what the
	// author says it is, never who
	// wrote it -- that is Form.Author.
	Labels []string `json:"labels,omitempty"`
	// AttestedBy, AttestValid: the form event's NIP-OA auth tag, checked
	// (social.CheckAttestation); AttestTrusted: valid, by an owner the
	// reader trusts (social.AttestView).
	AttestedBy    string `json:"attested_by,omitempty"`
	AttestValid   *bool  `json:"attest_valid,omitempty"`
	AttestTrusted bool   `json:"attest_trusted,omitempty"`
	attestWord    string
	// last: the created_at of my latest response or withdrawal (0: none),
	// so the next one is timed after it and counts (ties go to the lowest id).
	last int64
}

// fold reads items into views, oldest form first. A response or result
// counts only when it came the way its form did: privately for a private
// form, in the same group for a group form, signed for a public one.
// invalid: forms that failed nippf's validity rules.
func fold(items []item, me string, now int64) (views []*View, invalid int) {
	me = strings.ToLower(me)
	type reply struct {
		private bool
		group   string
	}
	var forms []*nippf.Form
	labels := map[string][]string{}
	attests := map[string]*social.Attestation{}
	var responses []*nippf.Response
	var results []*nippf.Result
	respWay := map[*nippf.Response]reply{}
	resWay := map[*nippf.Result]reply{}
	for _, it := range items {
		switch it.ev.Kind {
		case nippf.KindForm:
			f, err := nippf.ParseForm(it.ev, it.private)
			if err != nil {
				invalid++
				continue
			}
			if !slices.ContainsFunc(forms, func(g *nippf.Form) bool { return g.ID == f.ID }) {
				forms = append(forms, f)
				labels[f.ID] = formLabels(it.ev.Tags)
				attests[f.ID] = social.CheckAttestation(it.ev)
			}
		case nippf.KindResponse:
			if r, err := nippf.ParseResponse(it.ev); err == nil {
				responses = append(responses, r)
				respWay[r] = reply{it.private, r.Group}
			}
		case nippf.KindResult:
			if r, err := nippf.ParseResult(it.ev); err == nil {
				results = append(results, r)
				resWay[r] = reply{it.private, r.Group}
			}
		}
	}
	sameWay := func(f *nippf.Form, w reply) bool {
		return w.private == f.Private && (f.Group == "" || w.group == f.Group)
	}
	for _, f := range forms {
		var rs []*nippf.Response
		seen := map[string]bool{}
		for _, r := range responses {
			if r.Form == f.ID && sameWay(f, respWay[r]) && !seen[r.ID] {
				seen[r.ID] = true
				rs = append(rs, r)
			}
		}
		var res []*nippf.Result
		for _, r := range results {
			if r.Form == f.ID && sameWay(f, resWay[r]) {
				res = append(res, r)
			}
		}
		closing := f.Closing(res)
		selected := f.Select(rs, closing)
		v := &View{Form: f, State: f.State(closing, now), Result: closing, Responses: selected, Tally: f.Tally(selected), Labels: labels[f.ID]}
		if a := attests[f.ID]; a != nil {
			valid := a.Valid
			v.AttestedBy, v.AttestValid = a.Owner, &valid
		}
		switch {
		case f.Private:
			v.Where = "private"
		case f.Group != "":
			v.Where = "group"
		default:
			v.Where = "public"
		}
		switch {
		case f.Author == me:
			v.Role = "author"
		case slices.Contains(f.Responders(), me):
			v.Role = "responder"
		case slices.Contains(f.Observers(), me):
			v.Role = "observer"
		}
		v.CanRespond = !f.ReadOnly && f.Allowed(me) && v.State == nippf.StateOpen
		for _, r := range rs {
			if r.Author == me && r.CreatedAt > v.last {
				v.last = r.CreatedAt
			}
		}
		answered := map[string]bool{}
		for _, r := range selected {
			answered[r.Author] = true
			if r.Author == me {
				v.Mine = r
			}
		}
		for _, p := range f.Responders() {
			if !answered[p] {
				v.Waiting = append(v.Waiting, p)
			}
		}
		if v.Responses == nil {
			v.Responses = []*nippf.Response{}
		}
		views = append(views, v)
	}
	slices.SortStableFunc(views, func(a, b *View) int {
		if a.Form.CreatedAt != b.Form.CreatedAt {
			return int(a.Form.CreatedAt - b.Form.CreatedAt)
		}
		return strings.Compare(a.Form.ID, b.Form.ID)
	})
	return views, invalid
}

// formLabels: a form event's t tags' values, each once, in order (the
// event's id covers them: a form's labels are its author's).
func formLabels(tags [][]string) []string { return social.TagLabels(tags) }

// pick finds the form arg names among views: its id in full (hex, note or
// nevent), or the start of its hex id (6 characters or more), once.
func pick(cmd *cobra.Command, views []*View, arg string) (*View, error) {
	want := strings.ToLower(strings.TrimSpace(arg))
	prefix := len(want) >= 6 && len(want) < 64 && isHex(want)
	if !prefix {
		id, err := social.EventID(cmd, arg)
		if err != nil {
			return nil, err
		}
		want = id
	}
	var match []*View
	for _, v := range views {
		if v.Form.ID == want || (prefix && strings.HasPrefix(v.Form.ID, want)) {
			match = append(match, v)
		}
	}
	switch len(match) {
	case 0:
		return nil, common.NotFoundError(cmd, arg, errors.New("no such form here (form list shows the ones you can read)"))
	case 1:
		return match[0], nil
	}
	return nil, common.InvalidInputError(cmd, arg, errors.New("more than one form starts so: give more of its id"))
}

func isHex(s string) bool {
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
