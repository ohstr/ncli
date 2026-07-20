package client

import (
	"context"
	"net/url"
	"testing"
	"time"
)

func TestPing_ReachableAndUnreachable(t *testing.T) {
	wsURL, _ := mockWSServer(t)

	badURL, err := url.Parse("ws://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}

	reachable := &FlowSpec{Type: FlOW_REMOTE, Relay: wsURL.String(), relayURI: wsURL}
	unreachable := &FlowSpec{Type: FlOW_REMOTE, Relay: badURL.String(), relayURI: badURL}

	targets := &TargetsSpec{Relays: []*FlowSpec{reachable, unreachable}}

	report := Ping(context.Background(), targets, PingOptions{Quiet: true, Timeout: 2 * time.Second})

	if report.Checked != 2 {
		t.Fatalf("Checked = %d, want 2", report.Checked)
	}
	if report.Reachable != 1 {
		t.Fatalf("Reachable = %d, want 1", report.Reachable)
	}
	if report.Unreachable != 1 {
		t.Fatalf("Unreachable = %d, want 1", report.Unreachable)
	}
	if report.AllReachable() {
		t.Fatal("AllReachable() = true, want false (one relay was unreachable)")
	}

	var sawReachable, sawUnreachable bool
	for _, r := range report.Results {
		switch r.Relay {
		case reachable.Relay:
			sawReachable = r.Reachable
		case unreachable.Relay:
			sawUnreachable = !r.Reachable && r.Error != ""
		}
	}
	if !sawReachable {
		t.Error("reachable relay not reported as reachable")
	}
	if !sawUnreachable {
		t.Error("unreachable relay not reported as unreachable with an error")
	}
}

func TestPing_SkipsLocalStorePaths(t *testing.T) {
	wsURL, _ := mockWSServer(t)

	remote := &FlowSpec{Type: FlOW_REMOTE, Relay: wsURL.String(), relayURI: wsURL}
	local := &FlowSpec{Type: FlOW_LOCAL, Path: "./testdata/notes.db"}

	targets := &TargetsSpec{Relays: []*FlowSpec{remote, local}}

	report := Ping(context.Background(), targets, PingOptions{Quiet: true, Timeout: 2 * time.Second})

	if report.Checked != 1 {
		t.Fatalf("Checked = %d, want 1 (local store path should be skipped)", report.Checked)
	}
	if len(report.Results) != 1 {
		t.Fatalf("len(Results) = %d, want 1", len(report.Results))
	}
}

func TestPing_DuplicateRelayReportedUnreachable(t *testing.T) {
	wsURL, _ := mockWSServer(t)

	a := &FlowSpec{Type: FlOW_REMOTE, Relay: wsURL.String(), relayURI: wsURL}
	b := &FlowSpec{Type: FlOW_REMOTE, Relay: wsURL.String(), relayURI: wsURL}

	targets := &TargetsSpec{Relays: []*FlowSpec{a, b}}

	report := Ping(context.Background(), targets, PingOptions{Quiet: true, Timeout: 2 * time.Second})

	if report.Checked != 2 {
		t.Fatalf("Checked = %d, want 2", report.Checked)
	}
	if report.Reachable != 1 || report.Unreachable != 1 {
		t.Fatalf("Reachable=%d Unreachable=%d, want 1/1 (second entry is a duplicate)", report.Reachable, report.Unreachable)
	}
	if report.Results[1].Error != "duplicate relay" {
		t.Fatalf("Results[1].Error = %q, want %q", report.Results[1].Error, "duplicate relay")
	}
}
