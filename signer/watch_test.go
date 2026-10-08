package signer

import (
	"context"
	"os"
	"testing"
	"time"
)

// A change that lands after the policy was loaded but before Watch starts
// must still be applied.
func TestWatchSeesChangeBeforeStart(t *testing.T) {
	priv, _ := testKey(t, "signer")
	path := writePolicy(t, "kind: signer-policy\nrules: [{name: notes, allow: {kinds: [1]}}]")
	r := startServer(t, Config{PrivKeyHex: priv, PolicyPath: path})
	before := r.srv.Policy().SHA256

	if err := os.WriteFile(path, []byte("kind: signer-policy\nrules: [{name: notes, allow: {kinds: [1, 7]}}]"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.srv.Watch(ctx, 5*time.Millisecond)

	deadline := time.Now().Add(5 * time.Second)
	for r.srv.Policy().SHA256 == before {
		if time.Now().After(deadline) {
			t.Fatal("change made before Watch started was never applied")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
