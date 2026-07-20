package client

import (
	"testing"

	"sigs.k8s.io/yaml"
)

func TestStreamSpecStrictPowFromYAML(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want bool
	}{
		{
			name: "unset defaults to false",
			yaml: `
kind: stream
spec:
  from: ["wss://relay-a.example.com"]
  to: ["wss://relay-b.example.com"]
`,
			want: false,
		},
		{
			name: "explicit true",
			yaml: `
kind: stream
spec:
  strictPow: true
  from: ["wss://relay-a.example.com"]
  to: ["wss://relay-b.example.com"]
`,
			want: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var rs *RootSpec
			if err := yaml.Unmarshal([]byte(test.yaml), &rs); err != nil {
				t.Fatalf("unexpected error unmarshaling spec: %v", err)
			}

			spec, ok := rs.Spec.(*StreamSpec)
			if !ok {
				t.Fatalf("expected *StreamSpec, got %T", rs.Spec)
			}
			if spec.StrictPow != test.want {
				t.Fatalf("StrictPow = %v, want %v", spec.StrictPow, test.want)
			}
		})
	}
}

func TestSyncSpecStrictPowFromYAML(t *testing.T) {
	yamlSrc := `
kind: sync
spec:
  strictPow: true
  from:
    type: local
    path: ./missing.db
    ensure: create
  to:
    relay: wss://relay.example.com
`
	var rs *RootSpec
	if err := yaml.Unmarshal([]byte(yamlSrc), &rs); err != nil {
		t.Fatalf("unexpected error unmarshaling spec: %v", err)
	}

	spec, ok := rs.Spec.(*SyncSpec)
	if !ok {
		t.Fatalf("expected *SyncSpec, got %T", rs.Spec)
	}
	if !spec.StrictPow {
		t.Fatal("expected StrictPow to be true")
	}
}

// TestClientStrictPowPrecedence covers Client.strictPow's override rule:
// the CLI flag (options.StrictPow), when passed explicitly, always wins
// over the spec file's own value; when the flag was never passed
// (options.StrictPow == nil), the spec's value is used as-is.
func TestClientStrictPowPrecedence(t *testing.T) {
	trueVal, falseVal := true, false

	tests := []struct {
		name        string
		cliFlag     *bool
		specDefault bool
		want        bool
	}{
		{"flag unset, spec false", nil, false, false},
		{"flag unset, spec true", nil, true, true},
		{"flag explicitly true overrides spec false", &trueVal, false, true},
		{"flag explicitly false overrides spec true", &falseVal, true, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := &Client{options: &ClientOptions{StrictPow: test.cliFlag}}
			if got := c.strictPow(test.specDefault); got != test.want {
				t.Fatalf("strictPow(%v) with cliFlag=%v = %v, want %v", test.specDefault, test.cliFlag, got, test.want)
			}
		})
	}
}
