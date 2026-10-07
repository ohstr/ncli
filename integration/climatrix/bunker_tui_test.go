package climatrix

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip46"
)

// nip46Call sends one NIP-46 request from clientPriv to signerPub over c
// and waits for the matching response.
func nip46Call(t *testing.T, c *Raw, responses *Live, clientPriv, signerPub, method string, params []string) *nip46.Response {
	t.Helper()
	req, id, err := nip46.NewRequestEvent(clientPriv, signerPub, method, params, nip46.EncryptionNIP44V2)
	if err != nil {
		t.Fatal(err)
	}
	if err := req.Sign(clientPriv); err != nil {
		t.Fatal(err)
	}
	if ok, msg := c.Publish(req); !ok {
		t.Fatalf("publish %s request: %s", method, msg)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		ev := responses.Next(time.Until(deadline))
		if ev == nil {
			break
		}
		resp, err := nip46.ParseResponseEvent(ev, clientPriv)
		if err != nil || resp.RequestID != id {
			continue
		}
		return &resp.Response
	}
	t.Fatalf("no response to %s", method)
	return nil
}

// The bunker's TUI: an app pairs and asks to sign; the person approves the
// pairing with 'a' and rejects the signature with 'r'.
func TestTTY_BunkerApproveReject(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", nil)
	e := NewEnv(t)
	signer, app := A(t, "alice"), A(t, "carol")

	_, keys, screen := e.startTTY(t, "bunker", "--identity", signer.Nsec, "--relay", r.URL)
	var st struct{ Running bool }
	for i := 0; i < 100 && !st.Running; i++ {
		if res := e.Run(t, "bunker", "status", "--json"); res.Code == 0 {
			res.JSON(t, &st)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !st.Running {
		t.Fatalf("bunker didn't start\n%s", plain(screen.String()))
	}
	// Auto-prompt off, so 'a'/'r' act on the selected pending row.
	_, _ = keys.Write([]byte("p"))

	var pair struct {
		Result string `json:"result"`
	}
	e.MustOK(t, "bunker", "connect", "--json").JSON(t, &pair)
	u, err := url.Parse(pair.Result)
	if err != nil || u.Query().Get("secret") == "" {
		t.Fatalf("no bunker:// URI with a secret: %q", pair.Result)
	}

	c := Dial(t, r.URL)
	responses := c.Live(F{"kinds": []int{nip46.KindRequest}, "#p": []string{app.PubHex}})

	type result struct{ resp *nip46.Response }
	answer := func(method string, params []string, key string) *nip46.Response {
		done := make(chan result, 1)
		go func() {
			done <- result{nip46Call(t, c, responses, app.PrivHex, signer.PubHex, method, params)}
		}()
		// Wait for the request to reach the board, then press the key.
		for i := 0; i < 50 && !strings.Contains(plain(screen.String()), method); i++ {
			time.Sleep(200 * time.Millisecond)
		}
		time.Sleep(500 * time.Millisecond)
		_, _ = keys.Write([]byte(key))
		select {
		case res := <-done:
			return res.resp
		case <-time.After(25 * time.Second):
			t.Fatalf("%s: no answer after pressing %q\n%s", method, key, plain(screen.String()))
			return nil
		}
	}

	resp := answer(nip46.MethodConnect, []string{signer.PubHex, u.Query().Get("secret")}, "a")
	if resp.Error != "" || resp.Result != "ack" {
		t.Fatalf("approved connect: result %q error %q", resp.Result, resp.Error)
	}

	unsigned, _ := json.Marshal(nip01.NewUnsignedEvent(1, signer.PubHex, "please sign"))
	resp = answer(nip46.MethodSignEvent, []string{string(unsigned)}, "r")
	if resp.Error == "" || resp.Result != "" {
		t.Errorf("rejected sign_event still answered: result %q error %q", resp.Result, resp.Error)
	}

	var hist any
	e.MustOK(t, "bunker", "history", "--json").JSON(t, &hist)
	if b, _ := json.Marshal(hist); !strings.Contains(string(b), nip46.MethodSignEvent) {
		t.Errorf("history lacks the rejected sign_event: %s", b)
	}
}
