package signer

import "testing"

func TestParseURI(t *testing.T) {
	good := map[string]string{
		"bunker+unix:///run/signer/agent.sock": "/run/signer/agent.sock",
		"unix:///run/signer/agent.sock":        "/run/signer/agent.sock",
		"/run/signer/agent.sock":               "/run/signer/agent.sock",
		"bunker+unix:///run//signer/./a.sock":  "/run/signer/a.sock",
	}
	for in, want := range good {
		if got, err := ParseURI(in); err != nil || got != want {
			t.Errorf("ParseURI(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{
		"",
		"bunker+unix://run/agent.sock",
		"unix://relative.sock",
		"agent.sock",
		"bunker://abc?relay=wss://r",
		"tcp://127.0.0.1:9000",
		"bunker+unix:///run/a.sock?x=1",
	} {
		if _, err := ParseURI(bad); err == nil {
			t.Errorf("ParseURI(%q) succeeded", bad)
		}
	}
	if !IsURI("bunker+unix:///x") || !IsURI("unix:///x") || IsURI("satoshi") || IsURI("/run/a.sock") {
		t.Error("IsURI")
	}
}
