package event

import (
	"encoding/json"
	"fmt"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip01"
	"github.com/spf13/cobra"
)

// fieldChange is one top-level field that differs.
type fieldChange struct {
	Field string `json:"field"`
	A     any    `json:"a"`
	B     any    `json:"b"`
}

type diffView struct {
	Same        bool          `json:"same"`
	A           string        `json:"a"`
	B           string        `json:"b"`
	Fields      []fieldChange `json:"fields"`
	TagsRemoved [][]string    `json:"tags_removed"`
	TagsAdded   [][]string    `json:"tags_added"`
	Content     []string      `json:"content_diff,omitempty"`
}

func newDiffCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "diff <a> <b>",
		Short: "Compare two events field by field",
		Long: `Shows what differs between two events: top-level fields, tags removed
and added, and a line diff of the content. Typical use is two versions
of a replaceable event, or a draft against what was published.

Both arguments take any event reference or event JSON; "-" reads one
from stdin. Exits 0 either way; --json's "same" says whether they match.`,
		Example: `  ncli event diff old.json naddr1...
  ncli event get naddr1... -s relay.a.example | ncli event diff - naddr1... -s relay.b.example`,
		Args: common.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			res, closeAuth, err := newResolver(cmd)
			if err != nil {
				return err
			}
			defer closeAuth()

			var evs [2]*nip01.Event
			for i, a := range args {
				refs, _, err := collectRefs(cmd, []string{a})
				if err != nil {
					return err
				}
				if len(refs) != 1 {
					return common.InvalidInputError(cmd, a, fmt.Errorf("want one event, got %d", len(refs)))
				}
				l := res.resolve(ctx, refs, false)[0]
				if l.best() == nil {
					return missingError(cmd, res, []*lookup{l})
				}
				evs[i] = l.best().Event
			}

			view := diffEvents(evs[0], evs[1])
			view.A, view.B = args[0], args[1]
			if jsonMode(cmd) {
				return printJSON(cmd, view)
			}
			if view.Same {
				outf(cmd, "identical\n")
				return nil
			}
			for _, f := range view.Fields {
				if f.Field == "content" || f.Field == "tags" {
					continue
				}
				outf(cmd, "%-10s %v → %v\n", f.Field, f.A, f.B)
			}
			for _, t := range view.TagsRemoved {
				outf(cmd, "- tag  %s\n", strings.Join(t, "  "))
			}
			for _, t := range view.TagsAdded {
				outf(cmd, "+ tag  %s\n", strings.Join(t, "  "))
			}
			if len(view.Content) > 0 {
				outf(cmd, "content\n")
				for _, line := range view.Content {
					outf(cmd, "  %s\n", line)
				}
			}
			return nil
		},
	}
}

func diffEvents(a, b *nip01.Event) diffView {
	v := diffView{Fields: []fieldChange{}, TagsRemoved: [][]string{}, TagsAdded: [][]string{}}
	add := func(field string, x, y any) {
		v.Fields = append(v.Fields, fieldChange{Field: field, A: x, B: y})
	}
	if a.ID != b.ID {
		add("id", a.ID, b.ID)
	}
	if a.PubKey != b.PubKey {
		add("pubkey", a.PubKey, b.PubKey)
	}
	if a.Kind != b.Kind {
		add("kind", a.Kind, b.Kind)
	}
	if a.CreatedAt != b.CreatedAt {
		add("created_at", a.CreatedAt, b.CreatedAt)
	}
	if a.Sig != b.Sig {
		add("sig", a.Sig, b.Sig)
	}

	keyed := func(tags [][]string) map[string]int {
		m := map[string]int{}
		for _, t := range tags {
			m[tagKey(t)]++
		}
		return m
	}
	inA, inB := keyed(a.Tags), keyed(b.Tags)
	for _, t := range a.Tags {
		if k := tagKey(t); inB[k] > 0 {
			inB[k]--
		} else {
			v.TagsRemoved = append(v.TagsRemoved, t)
		}
	}
	for _, t := range b.Tags {
		if k := tagKey(t); inA[k] > 0 {
			inA[k]--
		} else {
			v.TagsAdded = append(v.TagsAdded, t)
		}
	}
	if len(v.TagsRemoved) > 0 || len(v.TagsAdded) > 0 {
		add("tags", len(a.Tags), len(b.Tags))
	}

	if a.Content != b.Content {
		add("content", len(a.Content), len(b.Content))
		v.Content = lineDiff(strings.Split(a.Content, "\n"), strings.Split(b.Content, "\n"))
	}
	v.Same = len(v.Fields) == 0
	return v
}

func tagKey(t []string) string {
	b, _ := json.Marshal(t)
	return string(b)
}

// maxDiffCells bounds lineDiff's table; past it, every line is shown as
// removed then added.
const maxDiffCells = 4_000_000

// lineDiff is a plain LCS line diff: "  " kept, "- " only in a, "+ " only
// in b.
func lineDiff(a, b []string) []string {
	n, m := len(a), len(b)
	if n*m > maxDiffCells {
		out := make([]string, 0, n+m)
		for _, l := range a {
			out = append(out, "- "+l)
		}
		for _, l := range b {
			out = append(out, "+ "+l)
		}
		return out
	}
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []string
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, "  "+a[i])
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, "- "+a[i])
			i++
		default:
			out = append(out, "+ "+b[j])
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, "- "+a[i])
	}
	for ; j < m; j++ {
		out = append(out, "+ "+b[j])
	}
	return out
}
