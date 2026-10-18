package adminplane

import (
	"errors"
	"strings"
	"testing"
)

func TestResolvePubkeys(t *testing.T) {
	const hexKey = "3c1db3dd55e2ff09ba5317dd8eec2339797e9e2ddf74591172735c47f3a2ad6e"
	const npub = "npub18swm8h24utlsnwjnzlwcampr89uha83dma69jytjwdwy0uaz44hq9kj5ng"

	// A dashboard can rely on admin.pubkeys alone.
	s, err := Resolve(&Config{Pubkeys: []string{npub, strings.ToUpper(hexKey)}, Dashboard: &DashboardConfig{Enabled: true}}, 5500, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Pubkeys) != 2 || s.Pubkeys[0] != hexKey || s.Pubkeys[1] != hexKey {
		t.Fatalf("pubkeys = %v, want both as lowercase hex", s.Pubkeys)
	}

	for _, bad := range []string{"npub1nope", "abc", "nsec1" + strings.Repeat("q", 58), hexKey[:63] + "g"} {
		_, err := Resolve(&Config{Pubkeys: []string{bad}}, 5500, nil)
		var ce *ConfigError
		if !errors.As(err, &ce) || ce.Usage || !strings.Contains(err.Error(), "admin.pubkeys") {
			t.Errorf("%q: err = %v, want invalid admin.pubkeys", bad, err)
		}
	}
}
