package signer

import (
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	l := NewLimiter()
	r := Rate{N: 3, Per: time.Minute}

	for i := 0; i < 3; i++ {
		if !l.Allow("chat", r, t0) {
			t.Fatalf("burst request %d refused", i+1)
		}
	}
	if l.Allow("chat", r, t0) {
		t.Fatal("4th request in the same instant allowed")
	}
	if !l.Allow("other", r, t0) {
		t.Fatal("rules must not share a bucket")
	}
	if l.Allow("chat", r, t0.Add(19*time.Second)) {
		t.Fatal("refilled too early")
	}
	if !l.Allow("chat", r, t0.Add(21*time.Second)) {
		t.Fatal("one token should refill after 20s")
	}
	if !l.Allow("chat", Rate{N: 10, Per: time.Minute}, t0.Add(21*time.Second)) {
		t.Fatal("a changed rate should start a fresh bucket")
	}
}
