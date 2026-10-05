package groups

import "testing"

func TestRandomHex_Length(t *testing.T) {
	s, err := randomHex(8)
	if err != nil {
		t.Fatalf("randomHex() error = %v", err)
	}
	if len(s) != 16 {
		t.Fatalf("randomHex(8) length = %d, want 16 (hex-encoded)", len(s))
	}
}

func TestRandomHex_Unique(t *testing.T) {
	a, err := randomHex(16)
	if err != nil {
		t.Fatalf("randomHex() error = %v", err)
	}
	b, err := randomHex(16)
	if err != nil {
		t.Fatalf("randomHex() error = %v", err)
	}
	if a == b {
		t.Fatalf("randomHex() returned the same value twice: %q", a)
	}
}
