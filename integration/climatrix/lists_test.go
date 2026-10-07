package climatrix

import "testing"

// TestEmptyListsAreArrays: every list-shaped --json output is [] on empty
// state, never null or missing -- an agent indexes it without a nil check.
func TestEmptyListsAreArrays(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", map[string]any{
		"membership": map[string]any{"enabled": true},
		"huddle":     map[string]any{"enabled": true},
	})
	e := NewEnv(t)
	cfg := r.ConfigPath

	for _, c := range []struct {
		key  string
		args []string
	}{
		{"groups", []string{"groups", "list", "--relay", r.URL}},
		{"roots", []string{"groups", "tree", "--relay", r.URL}},
		{"spaces", []string{"space", "list", "--relay", r.URL}},
		{"rooms", []string{"huddle", "list", "--relay", r.URL}},
		{"", []string{"find", "-s", r.URL, "--kinds", "30999"}},
		{"identities", []string{"id", "list"}},
		{"relays", []string{"prefs", "relays", "list"}},
		{"servers", []string{"blossom", "servers", "list"}},
		{"invites", []string{"relay", "invites", "list", "--config", cfg}},
		{"members", []string{"relay", "members", "list", "--config", cfg}},
		{"roles", []string{"relay", "roles", "list", "--config", cfg}},
	} {
		t.Run(c.args[0]+" "+c.args[1], func(t *testing.T) {
			e.MustOK(t, append(c.args, "--json")...).ExpectJSONArray(t, c.key)
		})
	}

	// contexts maps name to path: an object, still never null.
	var ctx struct {
		Contexts map[string]string `json:"contexts"`
	}
	e.MustOK(t, "relay", "context", "list", "--json").JSON(t, &ctx)
	if ctx.Contexts == nil {
		t.Errorf("relay context list: contexts is null, want {}")
	}
}
