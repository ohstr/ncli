package signer

import (
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
)

// Clients each number their own requests, so the log needs the server's
// connection number to tell their lines apart.
func TestDecisionLogNumbersConnections(t *testing.T) {
	priv, _ := testKey(t, "signer")
	_, friend := testKey(t, "friend")
	r := startServer(t, Config{PrivKeyHex: priv, PolicyPath: writePolicy(t, basicPolicy, friend)})
	for i := 0; i < 2; i++ {
		c := dial(t, r.uri)
		if err := c.Sign(ctx5(t), &nip01.Event{CreatedAt: uint64(time.Now().Unix()), Kind: 1}); err != nil {
			t.Fatal(err)
		}
	}
	recs := r.decisions.records(t)
	if len(recs) != 2 || recs[0].ReqID != recs[1].ReqID || recs[0].Client.Conn == recs[1].Client.Conn || recs[0].Client.Conn == 0 {
		t.Fatalf("records = %+v", recs)
	}
}
