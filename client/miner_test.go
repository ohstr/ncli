package client

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/relay"
	"sigs.k8s.io/yaml"
)

const testPubKey = "3c1db3dd55e2ff09ba5317dd8eec2339797e9e2ddf74591172735c47f3a2ad6e"

func writeUnsignedEventYAML(t *testing.T, path, pubkey, content string) {
	t.Helper()
	data := "pubkey: \"" + pubkey + "\"\ncreated_at: 1719759720\nkind: 1\ntags: []\ncontent: \"" + content + "\"\n"
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("failed to write fixture: %v", err)
	}
}

// TestMineWritesToOutNotInput is the no-silent-overwrite regression test:
// Mine must leave -e's file byte-identical and write only to -o.
func TestMineWritesToOutNotInput(t *testing.T) {
	dir := t.TempDir()
	eventPath := filepath.Join(dir, "event.yaml")
	outPath := filepath.Join(dir, "mined.json")
	writeUnsignedEventYAML(t, eventPath, testPubKey, "hello")

	before, err := os.ReadFile(eventPath)
	if err != nil {
		t.Fatal(err)
	}

	draft, err := LoadDraftEvent(eventPath)
	if err != nil {
		t.Fatalf("LoadDraftEvent failed: %v", err)
	}
	if _, err := Mine(context.Background(), draft, outPath, 8, &MineOptions{Workers: 2}); err != nil {
		t.Fatalf("Mine failed: %v", err)
	}

	after, err := os.ReadFile(eventPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("input file was modified:\nbefore=%q\nafter=%q", before, after)
	}

	outData, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("expected an output file at %s: %v", outPath, err)
	}
	var event nip01.Event
	if err := json.Unmarshal(outData, &event); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if event.ID == "" {
		t.Error("expected a mined event ID, got empty")
	}
}

// TestMineInPlace proves outPath == eventPath (the CLI's --in-place) still
// works, as the one case Mine does allow overwriting the input.
func TestMineInPlace(t *testing.T) {
	dir := t.TempDir()
	eventPath := filepath.Join(dir, "event.yaml")
	writeUnsignedEventYAML(t, eventPath, testPubKey, "hello")

	draft, err := LoadDraftEvent(eventPath)
	if err != nil {
		t.Fatalf("LoadDraftEvent failed: %v", err)
	}
	if _, err := Mine(context.Background(), draft, eventPath, 4, nil); err != nil {
		t.Fatalf("Mine failed: %v", err)
	}

	data, err := os.ReadFile(eventPath)
	if err != nil {
		t.Fatal(err)
	}
	var event nip01.Event
	if err := yaml.Unmarshal(data, &event); err != nil {
		t.Fatalf("in-place output is not valid YAML: %v", err)
	}
	if event.ID == "" {
		t.Error("expected a mined event ID, got empty")
	}
}

// TestMineJSONPOutput is the .jsonp-truncation-bug regression test: before
// the fix, an unhandled extension fell through the output switch with a nil
// data slice and silently wrote an empty file.
func TestMineJSONPOutput(t *testing.T) {
	dir := t.TempDir()
	eventPath := filepath.Join(dir, "event.yaml")
	outPath := filepath.Join(dir, "mined.jsonp")
	writeUnsignedEventYAML(t, eventPath, testPubKey, "hello")

	draft, err := LoadDraftEvent(eventPath)
	if err != nil {
		t.Fatalf("LoadDraftEvent failed: %v", err)
	}
	if _, err := Mine(context.Background(), draft, outPath, 4, nil); err != nil {
		t.Fatalf("Mine failed: %v", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal(".jsonp output is empty -- the extension-fallthrough truncation bug regressed")
	}
	var event nip01.Event
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf(".jsonp output is not valid JSON: %v", err)
	}
}

func TestMineUnsupportedExtension(t *testing.T) {
	dir := t.TempDir()
	eventPath := filepath.Join(dir, "event.yaml")
	outPath := filepath.Join(dir, "mined.txt")
	writeUnsignedEventYAML(t, eventPath, testPubKey, "hello")

	draft, err := LoadDraftEvent(eventPath)
	if err != nil {
		t.Fatalf("LoadDraftEvent failed: %v", err)
	}
	if _, err := Mine(context.Background(), draft, outPath, 4, nil); err == nil {
		t.Fatal("expected an error for an unsupported output extension, got nil")
	}
	if _, err := os.Stat(outPath); err == nil {
		t.Error("expected no output file to be written for an unsupported extension")
	}
}

func TestMineIdentityFillsEmptyPubkey(t *testing.T) {
	dir := t.TempDir()
	eventPath := filepath.Join(dir, "event.yaml")
	outPath := filepath.Join(dir, "mined.json")
	writeUnsignedEventYAML(t, eventPath, "", "hello")

	draft, err := LoadDraftEvent(eventPath)
	if err != nil {
		t.Fatalf("LoadDraftEvent failed: %v", err)
	}
	event, err := Mine(context.Background(), draft, outPath, 4, &MineOptions{IdentityPubKeyHex: testPubKey})
	if err != nil {
		t.Fatalf("Mine failed: %v", err)
	}
	if event.PubKey != testPubKey {
		t.Errorf("expected pubkey %q, got %q", testPubKey, event.PubKey)
	}
}

func TestMineIdentityConflict(t *testing.T) {
	dir := t.TempDir()
	eventPath := filepath.Join(dir, "event.yaml")
	outPath := filepath.Join(dir, "mined.json")
	writeUnsignedEventYAML(t, eventPath, testPubKey, "hello")

	draft, err := LoadDraftEvent(eventPath)
	if err != nil {
		t.Fatalf("LoadDraftEvent failed: %v", err)
	}
	otherPubkey := strings.Repeat("1", 64)
	if _, err := Mine(context.Background(), draft, outPath, 4, &MineOptions{IdentityPubKeyHex: otherPubkey}); err == nil {
		t.Fatal("expected a conflict error when --identity's pubkey differs from the event's own pubkey")
	}
}

func mineTestEvent(t *testing.T, pubkey, content string, difficulty int) *nip01.Event {
	t.Helper()
	ev := nip01.NewUnsignedEvent(1, pubkey, content)
	if err := ev.Mine(context.Background(), difficulty); err != nil {
		t.Fatalf("failed to mine test event: %v", err)
	}
	return ev
}

// TestCheckPOWLiveMergesAndDedupsAcrossTargets proves CheckPOWLive's fetch
// merges across ALL targets and dedups by event ID (DumpFromRelays'
// semantics), not "stop at the first target with a match" (Find's
// semantics) -- checking PoW compliance wants the full matching set.
func TestCheckPOWLiveMergesAndDedupsAcrossTargets(t *testing.T) {
	ctx := context.Background()
	limitation := &nip11.Limitation{MaxLimit: 1000, MaxSubscriptions: 10}
	dir := t.TempDir()

	path1 := filepath.Join(dir, "store1.db")
	path2 := filepath.Join(dir, "store2.db")

	shared := mineTestEvent(t, testPubKey, "shared", 4)
	onlyIn1 := mineTestEvent(t, testPubKey, "only in store1", 4)
	onlyIn2 := mineTestEvent(t, testPubKey, "only in store2", 4)

	store1, err := relay.NewEventStore(path1, limitation)
	if err != nil {
		t.Fatalf("failed to create store1: %v", err)
	}
	if err := store1.InsertEvents(ctx, []*nip01.Event{shared, onlyIn1}); err != nil {
		t.Fatalf("failed to seed store1: %v", err)
	}
	store1.Close()

	store2, err := relay.NewEventStore(path2, limitation)
	if err != nil {
		t.Fatalf("failed to create store2: %v", err)
	}
	if err := store2.InsertEvents(ctx, []*nip01.Event{shared, onlyIn2}); err != nil {
		t.Fatalf("failed to seed store2: %v", err)
	}
	store2.Close()

	targets := &TargetsSpec{Relays: []*FlowSpec{
		{Type: FlOW_LOCAL, Path: path1},
		{Type: FlOW_LOCAL, Path: path2},
	}}

	report, err := CheckPOWLive(ctx, targets, nil)
	if err != nil {
		t.Fatalf("CheckPOWLive failed: %v", err)
	}

	if report.Checked != 3 {
		t.Fatalf("expected 3 deduplicated events (shared + onlyIn1 + onlyIn2), got %d", report.Checked)
	}
	if report.Valid != 3 || report.Invalid != 0 {
		t.Fatalf("expected all 3 to pass PoW verification, got valid=%d invalid=%d", report.Valid, report.Invalid)
	}
}

func TestApplyIdentityFilterFillsEmptyAuthors(t *testing.T) {
	specs := []*FilterSpec{NewFilterSpec(&nip01.SubscriptionFilter{Kinds: []int{1}})}

	out, err := ApplyIdentityFilter(specs, testPubKey)
	if err != nil {
		t.Fatalf("ApplyIdentityFilter failed: %v", err)
	}
	if len(out) != 1 || len(out[0].Authors) != 1 || out[0].Authors[0] != testPubKey {
		t.Fatalf("expected Authors=[%s], got %v", testPubKey, out[0].Authors)
	}
}

func TestApplyIdentityFilterNarrowsExistingAuthors(t *testing.T) {
	other := strings.Repeat("1", 64)
	specs := []*FilterSpec{NewFilterSpec(&nip01.SubscriptionFilter{Authors: []string{other, testPubKey}})}

	out, err := ApplyIdentityFilter(specs, testPubKey)
	if err != nil {
		t.Fatalf("ApplyIdentityFilter failed: %v", err)
	}
	if len(out[0].Authors) != 1 || out[0].Authors[0] != testPubKey {
		t.Fatalf("expected Authors narrowed to [%s], got %v", testPubKey, out[0].Authors)
	}
}

func TestApplyIdentityFilterConflictErrors(t *testing.T) {
	other := strings.Repeat("1", 64)
	specs := []*FilterSpec{NewFilterSpec(&nip01.SubscriptionFilter{Authors: []string{other}})}

	if _, err := ApplyIdentityFilter(specs, testPubKey); err == nil {
		t.Fatal("expected an error when --identity's pubkey isn't among the filter's existing authors")
	}
}
