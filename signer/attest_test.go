package signer

import (
	"strings"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip46"
)

type attFixture struct {
	signerPriv, signerPub string
	approverPriv          string
	approverPub           string
	strangerPriv          string
}

func newAttFixture(t *testing.T) attFixture {
	var f attFixture
	f.signerPriv, f.signerPub = testKey(t, "signer")
	f.approverPriv, f.approverPub = testKey(t, "approver")
	f.strangerPriv, _ = testKey(t, "stranger")
	return f
}

// guardedPolicy requires one kind-9 attestation from the approver, with
// binds given as a YAML flow sequence.
func (f attFixture) guardedPolicy(t *testing.T, binds string, extraAuthors ...string) *Policy {
	authors := append([]string{f.approverPub}, extraAuthors...)
	return mustParse(t, `kind: signer-policy
rules:
  - name: guarded
    allow: {kinds: [30618]}
    max_clock_skew: 2m
    require:
      attestations:
        - name: ok
          kinds: [9]
          authors: [%s]
          max_age: 15m
          binds: %s
  - name: plain
    allow: {kinds: [1]}
    max_clock_skew: 2m
`, strings.Join(authors, ", "), binds)
}

func (f attFixture) eval(p *Policy, tgt *nip01.Event, now, start time.Time, used func(string) bool, atts ...*nip01.Event) Decision {
	return Evaluate(Request{
		Method:       nip46.MethodSignEvent,
		Event:        tgt,
		Attestations: atts,
		Now:          now,
		StartTime:    start,
		SignerPub:    f.signerPub,
	}, p, used)
}

func TestAttestationChecks(t *testing.T) {
	f := newAttFixture(t)
	p := f.guardedPolicy(t, "[{tag: e, from: id}]", f.signerPub)
	tgt := target(t, f.signerPub, 30618, t0, "", []string{"d", "repo"})
	eTag := []string{"e", tgt.ID}

	good := signed(t, f.approverPriv, 9, t0, "approve", eTag)
	tampered := signed(t, f.approverPriv, 9, t0, "approve", eTag)
	tampered.Content = "approve!"

	cases := []struct {
		name   string
		att    *nip01.Event
		start  time.Time
		used   func(string) bool
		reason string
	}{
		{"valid", good, time.Time{}, noneUsed, ""},
		{"bad signature", tampered, time.Time{}, noneUsed, "bad signature"},
		{"self-signed even when listed", signed(t, f.signerPriv, 9, t0, "approve", eTag), time.Time{}, noneUsed, "signed by the signer itself"},
		{"wrong kind", signed(t, f.approverPriv, 1, t0, "approve", eTag), time.Time{}, noneUsed, "wrong kind 1"},
		{"wrong author", signed(t, f.strangerPriv, 9, t0, "approve", eTag), time.Time{}, noneUsed, "author not allowed"},
		{"stale", signed(t, f.approverPriv, 9, t0.Add(-16*time.Minute), "approve", eTag), time.Time{}, noneUsed, "stale"},
		{"future-dated", signed(t, f.approverPriv, 9, t0.Add(3*time.Minute), "approve", eTag), time.Time{}, noneUsed, "future-dated"},
		{"before start", signed(t, f.approverPriv, 9, t0.Add(-5*time.Minute), "approve", eTag), t0, noneUsed, "created before the signer started"},
		{"within skew of start", signed(t, f.approverPriv, 9, t0.Add(-time.Minute), "approve", eTag), t0, noneUsed, ""},
		{"already used", good, time.Time{}, func(id string) bool { return id == good.ID }, "already used"},
		{"wrong event id", signed(t, f.approverPriv, 9, t0, "approve", []string{"e", strings.Repeat("0", 64)}), time.Time{}, noneUsed, "bind #1 not satisfied"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := f.eval(p, tgt, t0, c.start, c.used, c.att)
			if c.reason == "" {
				if !d.Allow || len(d.Consumed) != 1 || d.Consumed[0] != c.att.ID {
					t.Fatalf("want allow consuming %s, got %+v", c.att.ID, d)
				}
				return
			}
			if d.Allow || !strings.Contains(d.Reason, c.reason) || !strings.HasPrefix(d.Reason, `attestation "ok": `) {
				t.Fatalf("got %+v, want deny containing %q", d, c.reason)
			}
		})
	}
}

func TestAttestationTagCopyBind(t *testing.T) {
	f := newAttFixture(t)
	p := f.guardedPolicy(t, `[{tag: d, from: "tag:d"}]`)

	tgt := target(t, f.signerPub, 30618, t0, "", []string{"d", "repo"})
	if d := f.eval(p, tgt, t0, time.Time{}, noneUsed, signed(t, f.approverPriv, 9, t0, "", []string{"d", "repo"})); !d.Allow {
		t.Errorf("matching tag: %+v", d)
	}
	if d := f.eval(p, tgt, t0, time.Time{}, noneUsed, signed(t, f.approverPriv, 9, t0, "", []string{"d", "other"})); d.Allow {
		t.Errorf("different tag value: %+v", d)
	}
	bare := target(t, f.signerPub, 30618, t0, "")
	d := f.eval(p, bare, t0, time.Time{}, noneUsed, signed(t, f.approverPriv, 9, t0, "", []string{"d", "repo"}))
	if d.Allow || !strings.Contains(d.Reason, `target has no "d" tag`) {
		t.Errorf("target missing the copied tag: %+v", d)
	}
}

func TestAttestationContentBinds(t *testing.T) {
	f := newAttFixture(t)
	tgt := target(t, f.signerPub, 30618, t0, "", []string{"d", "a"}, []string{"d", "b"})

	exact := f.guardedPolicy(t, `[{content: "approve {id}"}]`)
	for _, c := range []struct {
		content string
		allow   bool
	}{
		{"approve " + tgt.ID, true},
		{"  approve " + tgt.ID + "\n", true},
		{"approve " + tgt.ID + " please", false},
		{"approve " + tgt.ID[:12], false},
		{"Approve " + tgt.ID, false},
	} {
		d := f.eval(exact, tgt, t0, time.Time{}, noneUsed, signed(t, f.approverPriv, 9, t0, c.content))
		if d.Allow != c.allow {
			t.Errorf("exact %q: %+v", c.content, d)
		}
	}

	contains := f.guardedPolicy(t, `[{content_contains: "{id}"}]`)
	if d := f.eval(contains, tgt, t0, time.Time{}, noneUsed, signed(t, f.approverPriv, 9, t0, "lgtm, ship "+tgt.ID+" now")); !d.Allow {
		t.Errorf("contains: %+v", d)
	}
	if d := f.eval(contains, tgt, t0, time.Time{}, noneUsed, signed(t, f.approverPriv, 9, t0, "lgtm")); d.Allow {
		t.Errorf("contains miss: %+v", d)
	}

	perTag := f.guardedPolicy(t, `[{content_contains: "ok {tag:d}"}]`)
	if d := f.eval(perTag, tgt, t0, time.Time{}, noneUsed, signed(t, f.approverPriv, 9, t0, "ok a; ok b")); !d.Allow {
		t.Errorf("every tag value present: %+v", d)
	}
	if d := f.eval(perTag, tgt, t0, time.Time{}, noneUsed, signed(t, f.approverPriv, 9, t0, "ok a")); d.Allow {
		t.Errorf("one tag value missing: %+v", d)
	}
}

func TestAttestationsMustBeDistinct(t *testing.T) {
	f := newAttFixture(t)
	p := mustParse(t, `kind: signer-policy
rules:
  - name: two-of
    allow: {kinds: [30618]}
    require:
      attestations:
        - {name: first, kinds: [9], authors: [%[1]s], max_age: 15m, binds: [{tag: e, from: id}]}
        - {name: second, kinds: [9, 1985], authors: [%[1]s], max_age: 15m, binds: [{tag: e, from: id}]}
`, f.approverPub)
	tgt := target(t, f.signerPub, 30618, t0, "")
	a := signed(t, f.approverPriv, 9, t0, "", []string{"e", tgt.ID})
	b := signed(t, f.approverPriv, 1985, t0, "", []string{"e", tgt.ID})

	if d := f.eval(p, tgt, t0, time.Time{}, noneUsed, a); d.Allow || !strings.Contains(d.Reason, "more than one requirement") {
		t.Errorf("one event for two specs: %+v", d)
	}
	// b only fits "second"; backtracking must still give "first" to a.
	d := f.eval(p, tgt, t0, time.Time{}, noneUsed, a, b)
	if !d.Allow || len(d.Consumed) != 2 {
		t.Errorf("two distinct events: %+v", d)
	}
	if d := f.eval(p, tgt, t0, time.Time{}, noneUsed); d.Allow || d.Reason != `attestation "first" required` {
		t.Errorf("none supplied: %+v", d)
	}
}

func TestTargetAge(t *testing.T) {
	f := newAttFixture(t)
	idBound := f.guardedPolicy(t, `[{content: "approve {id}"}]`)
	tagBound := f.guardedPolicy(t, `[{tag: d, from: "tag:d"}]`)
	now := t0

	// No attestation: the rule's 2m skew applies.
	if d := f.eval(idBound, target(t, f.signerPub, 1, now.Add(-90*time.Second), ""), now, time.Time{}, noneUsed); !d.Allow {
		t.Errorf("within rule skew: %+v", d)
	}
	if d := f.eval(idBound, target(t, f.signerPub, 1, now.Add(-3*time.Minute), ""), now, time.Time{}, noneUsed); d.Allow || !strings.Contains(d.Reason, "too old (limit 2m0s)") {
		t.Errorf("past rule skew: %+v", d)
	}

	// An id-bound approval made 8m after the draft extends the window to
	// the attestation's max_age.
	old := target(t, f.signerPub, 30618, now.Add(-10*time.Minute), "", []string{"d", "repo"})
	approve := signed(t, f.approverPriv, 9, now.Add(-2*time.Minute), "approve "+old.ID)
	if d := f.eval(idBound, old, now, time.Time{}, noneUsed, approve); !d.Allow {
		t.Errorf("id-bound extends age: %+v", d)
	}

	// A tag-only bind doesn't fix the event, so it doesn't extend anything.
	label := signed(t, f.approverPriv, 9, now.Add(-2*time.Minute), "", []string{"d", "repo"})
	if d := f.eval(tagBound, old, now, time.Time{}, noneUsed, label); d.Allow || !strings.Contains(d.Reason, "too old") {
		t.Errorf("tag bind must not extend age: %+v", d)
	}

	// Still never in the future beyond the rule's skew.
	future := target(t, f.signerPub, 30618, now.Add(3*time.Minute), "")
	if d := f.eval(idBound, future, now, time.Time{}, noneUsed, signed(t, f.approverPriv, 9, now.Add(90*time.Second), "approve "+future.ID)); d.Allow || !strings.Contains(d.Reason, "future") {
		t.Errorf("future target: %+v", d)
	}

	// An approval older than the draft can't have seen it.
	draft := target(t, f.signerPub, 30618, now.Add(-time.Minute), "")
	early := signed(t, f.approverPriv, 9, now.Add(-10*time.Minute), "approve "+draft.ID)
	if d := f.eval(idBound, draft, now, time.Time{}, noneUsed, early); d.Allow || !strings.Contains(d.Reason, "predates the target event") {
		t.Errorf("approval predating target: %+v", d)
	}
}
