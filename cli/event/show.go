package event

import (
	"fmt"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip19"
	"github.com/spf13/cobra"
)

// showView is one event rendered for reading.
type showView struct {
	Input   string       `json:"input"`
	Status  string       `json:"status"`
	Checks  []check      `json:"checks"`
	Kind    kindInfo     `json:"kind"`
	Author  string       `json:"author"`
	Created string       `json:"created"`
	Source  string       `json:"source,omitempty"`
	Links   links        `json:"links"`
	Tags    []tagView    `json:"tags"`
	Event   *nip01.Event `json:"event"`
}

// tagView is a tag with any key or event it points at spelled as NIP-19.
type tagView struct {
	Tag  []string `json:"tag"`
	Ref  string   `json:"ref,omitempty"`
	Note string   `json:"note,omitempty"`
}

func newShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show [ref...]",
		Short: "Show events as readable cards",
		Long: `Prints each event with its kind's name, author as an npub, time, the
relay it came from, links (note/nevent/naddr), tags with their keys and
events spelled as npub/nevent/naddr, the content, and a verify badge.

--json prints the same view, plus the raw event, as structured JSON.`,
		Example: `  ncli event show nevent1...
  ncli event show -f event.json
  ncli event show naddr1... --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			refs, single, err := collectRefs(cmd, args)
			if err != nil {
				return err
			}
			res, closeAuth, err := newResolver(cmd)
			if err != nil {
				return err
			}
			defer closeAuth()

			var lookups []*lookup
			_ = common.WithSpinner(cmd, fmt.Sprintf("looking up %d event(s)", len(refs)), func() error {
				lookups = res.resolve(ctx, refs, false)
				return nil
			})
			hits, missing := found(lookups)
			now := time.Now()
			views := make([]showView, 0, len(hits))
			for _, l := range hits {
				views = append(views, buildShowView(l, now))
			}
			if jsonMode(cmd) {
				if len(views) > 0 || len(missing) == 0 {
					if err := printShaped(cmd, views, single); err != nil {
						return err
					}
				}
			} else {
				for i, v := range views {
					if i > 0 {
						outf(cmd, "\n")
					}
					renderShow(cmd, v, now)
				}
			}
			return missingError(cmd, res, missing)
		},
	}
}

func buildShowView(l *lookup, now time.Time) showView {
	best := l.best()
	ev := best.Event
	checks := checkEvent(ev, l.Ref, expect{Kind: -1, Now: now})
	v := showView{
		Input:   l.Ref.Input,
		Status:  verdict(checks),
		Checks:  checks,
		Kind:    describeKind(ev.Kind),
		Author:  npub(ev.PubKey),
		Created: time.Unix(int64(ev.CreatedAt), 0).UTC().Format(time.RFC3339),
		Links:   eventLinks(ev, hintsFor(l, nil)),
		Event:   ev,
		Tags:    make([]tagView, 0, len(ev.Tags)),
	}
	if best.Source != "stdin" && best.Source != "arg" {
		v.Source = best.Source
	}
	for _, t := range ev.Tags {
		v.Tags = append(v.Tags, describeTag(t))
	}
	return v
}

// describeTag spells a tag's pointer, if it has one, as NIP-19.
func describeTag(t []string) tagView {
	tv := tagView{Tag: t}
	if len(t) < 2 {
		return tv
	}
	hint := ""
	if len(t) >= 3 {
		hint = t[2]
	}
	switch t[0] {
	case "p", "P":
		if s, err := nip19.EncodePublicKey(t[1]); err == nil {
			tv.Ref = s
		}
	case "e", "E", "q":
		var relays []string
		if strings.HasPrefix(hint, "ws") {
			relays = []string{hint}
		}
		if s, err := nip19.EncodeEvent(nip19.EventPointer{ID: t[1], Relays: relays}); err == nil {
			tv.Ref = s
		}
		if len(t) >= 4 && t[3] != "" {
			tv.Note = t[3]
		}
	case "a", "A":
		if r, ok := parseCoordinate(t[1]); ok {
			var relays []string
			if strings.HasPrefix(hint, "ws") {
				relays = []string{hint}
			}
			if s, err := nip19.EncodeAddr(nip19.EntityPointer{Identifier: r.D, PublicKey: r.Author, Kind: r.Kind, Relays: relays}); err == nil {
				tv.Ref = s
			}
		}
	}
	return tv
}

func renderShow(cmd *cobra.Command, v showView, now time.Time) {
	badge := "✓ valid"
	if v.Status != "valid" {
		badge = "✗ " + v.Status + " (" + strings.Join(failures(v.Checks), ", ") + ")"
	}
	outf(cmd, "kind %d · %s    %s\n", v.Event.Kind, v.Kind, badge)
	outf(cmd, "id       %s\n", v.Event.ID)
	if v.Links.Nevent != "" {
		outf(cmd, "nevent   %s\n", v.Links.Nevent)
	}
	if v.Links.Naddr != "" {
		outf(cmd, "naddr    %s\n", v.Links.Naddr)
	}
	outf(cmd, "author   %s\n", v.Author)
	outf(cmd, "created  %s (%s)\n", v.Created, ago(now, v.Event.CreatedAt))
	if v.Source != "" {
		outf(cmd, "source   %s\n", v.Source)
	}
	if len(v.Tags) > 0 {
		outf(cmd, "tags\n")
		for _, t := range v.Tags {
			line := "  " + strings.Join(t.Tag, "  ")
			if t.Ref != "" {
				line = fmt.Sprintf("  %s  %s", t.Tag[0], t.Ref)
				if t.Note != "" {
					line += "  " + t.Note
				}
			}
			outf(cmd, "%s\n", line)
		}
	}
	if v.Event.Content != "" {
		outf(cmd, "content\n")
		for _, line := range strings.Split(v.Event.Content, "\n") {
			outf(cmd, "  %s\n", line)
		}
	}
}

func ago(now time.Time, ts uint64) string {
	d := now.Sub(time.Unix(int64(ts), 0))
	future := d < 0
	if future {
		d = -d
	}
	var s string
	switch {
	case d < time.Minute:
		s = fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		s = fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		s = fmt.Sprintf("%dh", int(d.Hours()))
	default:
		s = fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	if future {
		return "in " + s
	}
	return s + " ago"
}
