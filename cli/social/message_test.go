package social

import (
	"testing"

	"github.com/ohstr/nmilat/nip01"
)

func TestFromEventThreading(t *testing.T) {
	for name, c := range map[string]struct {
		ev            nip01.Event
		channel       string
		reply, root   string
		group, subj   string
		mentionsCount int
	}{
		"nip-17 dm": {
			ev:      nip01.Event{Kind: 14, Tags: [][]string{{"p", "AA"}, {"e", "r1", "wss://x"}, {"subject", "s"}}},
			channel: "dm", reply: "r1", subj: "s", mentionsCount: 1,
		},
		"nip-10 marked": {
			ev:      nip01.Event{Kind: 1, Tags: [][]string{{"e", "root", "", "root", "pk"}, {"e", "par", "", "reply", "pk"}}},
			channel: "dm", reply: "par", root: "root",
		},
		"nip-10 reply to root": {
			ev:      nip01.Event{Kind: 1, Tags: [][]string{{"e", "root", "", "root"}}},
			channel: "dm", reply: "root", root: "root",
		},
		"nip-22 comment": {
			ev: nip01.Event{Kind: 1111, Tags: [][]string{
				{"E", "root", "wss://r", "rootpk"}, {"K", "11"},
				{"e", "par", "wss://r", "parpk"}, {"k", "1111"}, {"p", "parpk"}, {"h", "team"},
			}},
			channel: "group", reply: "par", root: "root", group: "team", mentionsCount: 1,
		},
		"group chat quote": {
			ev:      nip01.Event{Kind: 9, Tags: [][]string{{"h", "team"}, {"q", "msg"}}},
			channel: "group", reply: "msg", group: "team",
		},
		"mention only": {
			ev:      nip01.Event{Kind: 1, Tags: [][]string{{"e", "x", "", "mention"}}},
			channel: "dm",
		},
	} {
		m := FromEvent(&c.ev, c.channel)
		if m.ReplyTo != c.reply || m.Root != c.root || m.Group != c.group || m.Subject != c.subj || len(m.Mentions) != c.mentionsCount {
			t.Errorf("%s: %+v", name, m)
		}
		if m.Mentions == nil {
			t.Errorf("%s: nil mentions", name)
		}
	}
}
