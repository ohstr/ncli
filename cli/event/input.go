package event

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip19"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// ref is one event a command was asked about: either the event itself
// (inline JSON, a file, stdin), or a pointer to it (an id or an addressable
// coordinate) that still has to be fetched from relays.
type ref struct {
	Input string // the reference as given, for reports

	ID     string // hex event id, for a by-id pointer
	Addr   bool   // a kind:pubkey:d coordinate rather than an id
	Author string // hex pubkey the pointer names, "" if it names none
	Kind   int    // kind the pointer names, -1 if it names none
	D      string // the coordinate's d tag
	Hints  []string

	Event  *nip01.Event // set when the event was given in full
	Source string       // where Event came from: "stdin", "arg" or a file path
}

func (r *ref) inline() bool { return r.Event != nil }

// filter is the REQ filter that fetches r.
func (r *ref) filter() *nip01.SubscriptionFilter {
	if r.Addr {
		return &nip01.SubscriptionFilter{
			Kinds:   []int{r.Kind},
			Authors: []string{r.Author},
			Tags:    map[string][]string{"d": {r.D}},
		}
	}
	return &nip01.SubscriptionFilter{IDs: []string{r.ID}}
}

// matches reports whether ev is what r points at, judged by what ev claims
// -- verify then checks the claim.
func (r *ref) matches(ev *nip01.Event) bool {
	if r.Addr {
		return ev.Kind == r.Kind && strings.EqualFold(ev.PubKey, r.Author) && dTag(ev) == r.D
	}
	return strings.EqualFold(ev.ID, r.ID)
}

func dTag(ev *nip01.Event) string {
	for _, t := range ev.Tags {
		if len(t) >= 2 && t[0] == "d" {
			return t[1]
		}
	}
	return ""
}

// collectRefs gathers every event a command was given: positional
// references, --file values, and stdin ("-", or implicitly when nothing else
// was given and stdin is not a terminal). single reports whether the input
// was exactly one event reference rather than a list, so output can mirror
// its shape.
func collectRefs(cmd *cobra.Command, args []string) (refs []*ref, single bool, err error) {
	files, _ := cmd.Flags().GetStringArray("file")
	stdinUsed := false
	readStdin := func() error {
		if stdinUsed {
			return nil
		}
		stdinUsed = true
		data, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return common.RuntimeError(cmd, fmt.Errorf("read stdin: %w", err))
		}
		events, list, err := decodeEvents(data)
		if err != nil {
			return common.InvalidInputError(cmd, "stdin", err)
		}
		single = !list && len(events) == 1
		for _, ev := range events {
			refs = append(refs, inlineRef("stdin", ev))
		}
		return nil
	}

	for _, a := range args {
		if a == "-" {
			if err := readStdin(); err != nil {
				return nil, false, err
			}
			continue
		}
		got, list, err := parseArg(a)
		if err != nil {
			return nil, false, common.InvalidInputError(cmd, common.RedactSecretInput(a), err)
		}
		single = !list && len(got) == 1
		refs = append(refs, got...)
	}
	for _, f := range files {
		if f == "-" {
			if err := readStdin(); err != nil {
				return nil, false, err
			}
			continue
		}
		got, list, err := readEventFile(f)
		if err != nil {
			return nil, false, common.InvalidInputError(cmd, f, err)
		}
		single = !list && len(got) == 1
		refs = append(refs, got...)
	}

	if len(args) == 0 && len(files) == 0 {
		if f, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(int(f.Fd())) {
			return nil, false, common.InvocationOrHelp(cmd, args, errors.New("give an event reference, --file, or pipe events on stdin"))
		}
		if err := readStdin(); err != nil {
			return nil, false, err
		}
	}

	if len(refs) == 0 {
		return nil, false, common.InvalidInputError(cmd, "", errors.New("no events given"))
	}
	if len(refs) > 1 {
		single = false
	}
	return refs, single, nil
}

func inlineRef(source string, ev *nip01.Event) *ref {
	return &ref{Input: source, Kind: -1, Event: ev, Source: source}
}

// parseArg reads one positional reference: an nevent/note/naddr (with or
// without a nostr: prefix), a hex id, a kind:pubkey:d coordinate, inline
// event JSON, or a path to an event file.
func parseArg(arg string) (refs []*ref, list bool, err error) {
	s := strings.TrimPrefix(strings.TrimSpace(arg), "nostr:")
	switch {
	case strings.HasPrefix(s, "{") || strings.HasPrefix(s, "["):
		events, list, err := decodeEvents([]byte(s))
		if err != nil {
			return nil, false, err
		}
		for _, ev := range events {
			refs = append(refs, inlineRef("arg", ev))
		}
		return refs, list, nil
	case strings.HasPrefix(s, "nevent1"):
		p, err := nip19.DecodeEvent(s)
		if err != nil {
			return nil, false, fmt.Errorf("invalid nevent: %w", err)
		}
		r := &ref{Input: arg, ID: p.ID, Author: p.Author, Kind: -1, Hints: p.Relays}
		if p.Kind != 0 {
			r.Kind = p.Kind
		}
		return []*ref{r}, false, nil
	case strings.HasPrefix(s, "note1"):
		id, err := nip19.DecodeNote(s)
		if err != nil {
			return nil, false, fmt.Errorf("invalid note: %w", err)
		}
		return []*ref{{Input: arg, ID: id, Kind: -1}}, false, nil
	case strings.HasPrefix(s, "naddr1"):
		p, err := nip19.DecodeAddr(s)
		if err != nil {
			return nil, false, fmt.Errorf("invalid naddr: %w", err)
		}
		return []*ref{{Input: arg, Addr: true, Author: p.PublicKey, Kind: p.Kind, D: p.Identifier, Hints: p.Relays}}, false, nil
	case isHex32(s):
		return []*ref{{Input: arg, ID: strings.ToLower(s), Kind: -1}}, false, nil
	}

	if r, ok := parseCoordinate(s); ok {
		r.Input = arg
		return []*ref{r}, false, nil
	}
	if info, statErr := os.Stat(s); statErr == nil && !info.IsDir() {
		return readEventFile(s)
	}
	return nil, false, fmt.Errorf("not an event reference: want nevent1/note1/naddr1, a hex id, kind:pubkey:d, event JSON, or an event file")
}

// parseCoordinate reads a NIP-01 "kind:pubkey:d" address; the pubkey may
// also be an npub.
func parseCoordinate(s string) (*ref, bool) {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) != 3 {
		return nil, false
	}
	kind, err := strconv.Atoi(parts[0])
	if err != nil || kind < 0 {
		return nil, false
	}
	pub, err := nip19.ParsePublicKey(parts[1])
	if err != nil {
		return nil, false
	}
	return &ref{Addr: true, Author: pub, Kind: kind, D: parts[2]}, true
}

func isHex32(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func readEventFile(path string) ([]*ref, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	events, list, err := decodeEvents(data)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", path, err)
	}
	refs := make([]*ref, 0, len(events))
	for _, ev := range events {
		refs = append(refs, inlineRef(path, ev))
	}
	return refs, list, nil
}

// decodeEvents reads events from a single object, a JSON array, one value
// per line (NDJSON), or relay messages (["EVENT", <sub>, {...}]) as a relay
// sends them. list reports whether the input itself was a list.
func decodeEvents(data []byte) (events []*nip01.Event, list bool, err error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	values := 0
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err == io.EOF {
			break
		} else if err != nil {
			return nil, false, fmt.Errorf("not valid event JSON: %w", err)
		}
		values++
		got, isList, err := decodeValue(raw)
		if err != nil {
			return nil, false, err
		}
		list = list || isList
		events = append(events, got...)
	}
	if len(events) == 0 {
		return nil, false, errors.New("no events in input")
	}
	return events, list || values > 1, nil
}

func decodeValue(raw json.RawMessage) ([]*nip01.Event, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		ev, err := decodeEvent(trimmed)
		if err != nil {
			return nil, false, err
		}
		return []*nip01.Event{ev}, false, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(trimmed, &items); err != nil {
		return nil, false, errors.New("want an event object, an array of events, or a relay EVENT message")
	}
	if len(items) > 0 {
		var label string
		if json.Unmarshal(items[0], &label) == nil {
			if label != "EVENT" || len(items) < 2 {
				return nil, false, fmt.Errorf("relay message %q carries no event", label)
			}
			ev, err := decodeEvent(items[len(items)-1])
			if err != nil {
				return nil, false, err
			}
			return []*nip01.Event{ev}, false, nil
		}
	}
	events := make([]*nip01.Event, 0, len(items))
	for i, item := range items {
		got, _, err := decodeValue(item)
		if err != nil {
			return nil, false, fmt.Errorf("item %d: %w", i, err)
		}
		events = append(events, got...)
	}
	return events, true, nil
}

func decodeEvent(raw []byte) (*nip01.Event, error) {
	var ev nip01.Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		return nil, fmt.Errorf("not a valid event: %w", err)
	}
	if ev.Tags == nil {
		ev.Tags = [][]string{}
	}
	return &ev, nil
}
