package signer

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip46"
)

func TestServerMalformedRequests(t *testing.T) {
	priv, _ := testKey(t, "signer")
	_, friend := testKey(t, "friend")
	r := startServer(t, Config{PrivKeyHex: priv, PolicyPath: writePolicy(t, basicPolicy, friend)})
	c := dial(t, r.uri)
	ctx := ctx5(t)

	cases := map[string][]string{
		nip46.MethodSignEvent + " no params":        nil,
		nip46.MethodSignEvent + " bad event":        {"{not json"},
		nip46.MethodSignEvent + " bad attestations": {`{"kind":1,"created_at":1,"tags":[],"content":""}`, `{"not":"an array"}`},
		nip46.MethodNIP44Encrypt + " one param":     {friend},
		nip46.MethodNIP44Encrypt + " bad peer":      {"npub1nope", "x"},
	}
	for name, params := range cases {
		method := strings.Fields(name)[0]
		_, err := c.Call(ctx, method, params...)
		var re *RemoteError
		if !errors.As(err, &re) || !re.Invalid() {
			t.Errorf("%s: err = %v, want an invalid: response", name, err)
		}
	}
	// Each malformed request is still a logged denial.
	if n := len(r.denials.records(t)); n != len(cases) {
		t.Errorf("denial records = %d, want %d", n, len(cases))
	}

	// A non-JSON line gets an error response, and the connection survives.
	path, _ := ParseURI(r.uri)
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	sc := bufio.NewScanner(conn)
	_, _ = conn.Write([]byte("garbage\n\n{\"id\":\"2\",\"method\":\"ping\",\"params\":[]}\n"))
	var resp nip46.Response
	if !sc.Scan() || json.Unmarshal(sc.Bytes(), &resp) != nil || !strings.HasPrefix(resp.Error, ErrPrefixInvalid) {
		t.Fatalf("garbage line: %q", sc.Text())
	}
	if !sc.Scan() || json.Unmarshal(sc.Bytes(), &resp) != nil || resp.RequestID != "2" || resp.Result != "pong" {
		t.Fatalf("after garbage: %q", sc.Text())
	}
}

func TestClientRejectsTamperedResult(t *testing.T) {
	// A fake signer that answers sign_event with a different event.
	dir := sockDir(t)
	path := dir + "/fake.sock"
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	priv, pub := testKey(t, "fake")
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		sc := bufio.NewScanner(conn)
		enc := json.NewEncoder(conn)
		for sc.Scan() {
			var req nip46.Request
			_ = json.Unmarshal(sc.Bytes(), &req)
			switch req.Method {
			case nip46.MethodGetPublicKey:
				_ = enc.Encode(nip46.Response{RequestID: req.RequestID, Result: pub})
			default:
				ev := signed(t, priv, 1, t0, "something else")
				b, _ := json.Marshal(ev)
				_ = enc.Encode(nip46.Response{RequestID: req.RequestID, Result: string(b)})
			}
		}
	}()

	c := dial(t, URIScheme+"://"+path)
	ev := target(t, "", 1, t0, "what I asked for")
	err = c.Sign(ctx5(t), ev)
	if err == nil || !strings.Contains(err.Error(), "different event") {
		t.Fatalf("tampered result accepted: %v", err)
	}
	if ev.Content != "what I asked for" || ev.Sig != "" {
		t.Fatal("caller's event was overwritten")
	}
}
