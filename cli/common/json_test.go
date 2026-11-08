package common

import (
	"encoding/json"
	"testing"
)

// Printed JSON keeps "<", ">" and "&" as written, compact or indented, and
// still decodes to the same value.
func TestMarshalJSON_NoHTMLEscaping(t *testing.T) {
	v := map[string]any{"warnings": []string{"group:<kind> & more >"}}
	for indent, want := range map[string]string{
		"":   `{"warnings":["group:<kind> & more >"]}`,
		"  ": "{\n  \"warnings\": [\n    \"group:<kind> & more >\"\n  ]\n}",
	} {
		got, err := MarshalJSON(v, indent)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("indent %q: got %s, want %s", indent, got, want)
		}
		var back map[string][]string
		if err := json.Unmarshal(got, &back); err != nil || back["warnings"][0] != "group:<kind> & more >" {
			t.Errorf("indent %q: round trip = %v, %v", indent, back, err)
		}
	}
}
