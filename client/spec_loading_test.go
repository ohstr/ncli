package client

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

func loadCases(t *testing.T, casesPath string) []string {
	f, err := os.Open(casesPath)
	if err != nil {
		t.Fatalf("cases file: %v", err)
	}

	var casesYamlBytes []byte
	if casesYamlBytes, err = io.ReadAll(f); err != nil {
		t.Fatalf("read cases file: %v", err)
	}

	yamlFilesPath := make([]string, 0)

	notesDBPath := filepath.Join(t.TempDir(), "notes.db")
	if err := os.WriteFile(notesDBPath, nil, 0644); err != nil {
		t.Fatalf("failed to create notes.db fixture: %v", err)
	}

	casesContent := strings.ReplaceAll(string(casesYamlBytes), "../testdata/notes.db", notesDBPath)
	for _, caseContent := range strings.Split(casesContent, "\n---") {
		fcase, err := os.CreateTemp(t.TempDir(), "testcase_*.yaml")
		if err != nil {
			t.Fatalf("failed to create case temp file: %v", err)
		}

		if _, err := fcase.WriteString(caseContent); err != nil {
			t.Fatalf("failed to write case yaml file: %v", err)
		}

		yamlFilesPath = append(yamlFilesPath, fcase.Name())
		fcase.Close()
	}

	return yamlFilesPath
}

func TestSpecStream(t *testing.T) {

	yamlFilesPath := loadCases(t, "../testdata/cases_stream.yaml")

	tests := []struct {
		name            string
		expectedFilters int
		wantError       bool
	}{
		{"valid", 2, false},
		{"undefined spec", 1, true},
		{"invalid from", 1, true},
		{"invalid to", 1, true},
		{"without filters", 1, false},
		{"invalid filter", 1, false},
		{"valid duration", 2, false},
		{"invalid duration", 1, true},
		{"ensure policy", 1, false},
		{"invalid ensure policy", 1, true},
		{"since", 1, false},
	}

	for testID, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if testID >= len(yamlFilesPath) {
				t.Fatalf("Index out of bounds: %d, YAMLs: %d", testID, len(yamlFilesPath))
			}

			rs, err := loadSpecFromYaml(yamlFilesPath[testID])

			if err != nil && (err != nil) != test.wantError {
				t.Fatalf("unexpected error: %v", err)
			} else if err == nil && test.wantError {
				t.Fatalf("expected error")
			}

			if err == nil {
				switch spec := rs.Spec.(type) {
				case *StreamSpec:
					if spec.filters.Size() != test.expectedFilters {
						t.Fatalf("unexpected filters size, expected=%d got=%d", test.expectedFilters, spec.filters.Size())
					}

				default:
					t.Fatalf("unexpected spec type, expected=%T", &StreamSpec{})
				}
			}

		})
	}
}

func TestSpecInspect(t *testing.T) {

	yamlFilesPath := loadCases(t, "../testdata/cases_inspect.yaml")

	tests := []struct {
		name            string
		wantError       bool
		expectedFilters int
	}{
		{"valid", false, 1},
		{"remote target", false, 1},
		{"multiple targets", false, 1},
		{"explicit filters", false, 2},
		{"without filters", false, 1},
		{"nil target", true, 0},
		{"nil filter", true, 0},
		{"invalid target", true, 0},
		{"invalid filter", true, 0},
	}

	for testID, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if testID >= len(yamlFilesPath) {
				t.Fatalf("Index out of bounds: %d, YAMLs: %d", testID, len(yamlFilesPath))
			}

			rs, err := loadSpecFromYaml(yamlFilesPath[testID])

			if err != nil && (err != nil) != test.wantError {
				t.Fatalf("unexpected error: %v", err)
			} else if err == nil && test.wantError {
				t.Fatalf("expected error")
			}

			if err == nil {
				switch spec := rs.Spec.(type) {
				case *InspectSpec:
					if spec.filters.Size() != test.expectedFilters {
						t.Fatalf("unexpected filters size, expected=%d got=%d", test.expectedFilters, spec.filters.Size())
					}

				default:
					t.Fatalf("unexpected spec type, expected=%T", &InspectSpec{})
				}
			}

		})
	}
}

func TestSpecTargets(t *testing.T) {

	yamlFilesPath := loadCases(t, "../testdata/cases_targets.yaml")

	tests := []struct {
		name            string
		wantError       bool
		expectedRelays  int
		expectedFilters int
	}{
		{"valid, mixed", false, 3, 0},
		{"local, ensure create, missing path", false, 1, 0},
		{"local, default ensure, missing path", true, 0, 0},
		{"nil entry", true, 0, 0},
		{"malformed entry", true, 0, 0},
		{"empty relays", false, 0, 0},
		{"with filters", false, 1, 2},
		{"nil filter entry", true, 0, 0},
	}

	for testID, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if testID >= len(yamlFilesPath) {
				t.Fatalf("Index out of bounds: %d, YAMLs: %d", testID, len(yamlFilesPath))
			}

			rs, err := loadSpecFromYaml(yamlFilesPath[testID])

			if err != nil && (err != nil) != test.wantError {
				t.Fatalf("unexpected error: %v", err)
			} else if err == nil && test.wantError {
				t.Fatalf("expected error")
			}

			if err == nil {
				switch spec := rs.Spec.(type) {
				case *TargetsSpec:
					if len(spec.Relays) != test.expectedRelays {
						t.Fatalf("unexpected relays count, expected=%d got=%d", test.expectedRelays, len(spec.Relays))
					}
					if len(spec.Filters) != test.expectedFilters {
						t.Fatalf("unexpected filters count, expected=%d got=%d", test.expectedFilters, len(spec.Filters))
					}

				default:
					t.Fatalf("unexpected spec type, expected=%T", &TargetsSpec{})
				}
			}

		})
	}
}

func TestSpecSync(t *testing.T) {

	yamlFilesPath := loadCases(t, "../testdata/cases_sync.yaml")

	tests := []struct {
		name            string
		wantError       bool
		expectedFilters int
		check           func(t *testing.T, spec *SyncSpec)
	}{
		{"valid basic", false, 1, nil},
		{"flexible flow order", false, 1, nil},
		{"multiple locals", true, 0, nil},
		{"multiple remotes", true, 0, nil},
		{"single remote only", true, 0, nil},
		{"single local only", true, 0, nil},
		{"zero flows", true, 0, nil},
		{"invalid direction", true, 0, nil},
		{"direction up", false, 1, func(t *testing.T, spec *SyncSpec) {
			if spec.Direction != SyncDirectionUp {
				t.Fatalf("unexpected direction, expected=%s got=%s", SyncDirectionUp, spec.Direction)
			}
		}},
		{"custom reconcile/batch settings", false, 1, func(t *testing.T, spec *SyncSpec) {
			if spec.MaxReconcileRounds != 5 {
				t.Fatalf("unexpected maxReconcileRounds, expected=5 got=%d", spec.MaxReconcileRounds)
			}
			if spec.PullBatchSize != 250 {
				t.Fatalf("unexpected pullBatchSize, expected=250 got=%d", spec.PullBatchSize)
			}
		}},
		{"filters present", false, 1, func(t *testing.T, spec *SyncSpec) {
			if len(spec.Filters) != 1 || len(spec.Filters[0].Kinds) != 1 || spec.Filters[0].Kinds[0] != 35500 {
				t.Fatalf("unexpected filters content: %+v", spec.Filters)
			}
		}},
		{"timeouts present", false, 1, func(t *testing.T, spec *SyncSpec) {
			if spec.Timeouts == nil || spec.Timeouts.Handshake == nil || *spec.Timeouts.Handshake != "5s" {
				t.Fatalf("unexpected timeouts: %+v", spec.Timeouts)
			}
		}},
	}

	for testID, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if testID >= len(yamlFilesPath) {
				t.Fatalf("Index out of bounds: %d, YAMLs: %d", testID, len(yamlFilesPath))
			}

			rs, err := loadSpecFromYaml(yamlFilesPath[testID])

			if err != nil && (err != nil) != test.wantError {
				t.Fatalf("unexpected error: %v", err)
			} else if err == nil && test.wantError {
				t.Fatalf("expected error")
			}

			if err == nil {
				switch spec := rs.Spec.(type) {
				case *SyncSpec:
					if len(spec.Filters) != test.expectedFilters {
						t.Fatalf("unexpected filters count, expected=%d got=%d", test.expectedFilters, len(spec.Filters))
					}
					if test.check != nil {
						test.check(t, spec)
					}

				default:
					t.Fatalf("unexpected spec type, expected=%T", &SyncSpec{})
				}
			}

		})
	}
}

func TestSpecFilter(t *testing.T) {

	toleranceMilliseconds := uint64(10) // instead of patching time package
	now := time.Now()

	// time.Sleep(time.Second * 5)

	tests := []struct {
		name            string
		duration        string
		wantError       bool
		approxTimestamp uint64
	}{
		{"months", "10mo", false, uint64(now.Add(HoursPerMonth * time.Hour * 10).Unix())},
		{"weeks", "5w", false, uint64(now.Add(HoursPerWeek * time.Hour * 5).Unix())},
		{"days", "3d", false, uint64(now.Add(HoursPerDay * time.Hour * 3).Unix())},
		{"hours", "15h", false, uint64(now.Add(time.Hour * 15).Unix())},
		{"minutes", "40m", false, uint64(now.Add(time.Minute * 40).Unix())},
		{"seconds", "500s", false, uint64(now.Add(time.Second * 500).Unix())},
		{"timestamp", "1719759720668", false, 1719759720668},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			specYaml := fmt.Sprintf(`until: %s`, test.duration)

			var obj *FilterSpec
			err := yaml.Unmarshal([]byte(specYaml), &obj)
			if err != nil && (err != nil) != test.wantError {
				t.Fatal(err)

			} else if err == nil && (err == nil) == test.wantError {
				t.Fatal("error wanted")
			}

			if obj.SubscriptionFilter.Until < test.approxTimestamp-toleranceMilliseconds || obj.SubscriptionFilter.Until > test.approxTimestamp+toleranceMilliseconds {
				t.Errorf("timestamp out of expected range, %d = %d ", obj.SubscriptionFilter.Until, test.approxTimestamp)
			}

			t.Logf("obj.str.until=%v", obj.Until)
			t.Logf("obj.timestamp.until=%v", obj.SubscriptionFilter.Until)

		})
	}

}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{"hours", "24h", 24 * time.Hour, false},
		{"days", "7d", 7 * HoursPerDay * time.Hour, false},
		{"weeks", "2w", 2 * HoursPerWeek * time.Hour, false},
		{"months", "1mo", HoursPerMonth * time.Hour, false},
		{"minutes", "40m", 40 * time.Minute, false},
		{"seconds", "500s", 500 * time.Second, false},
		{"combined", "1d12h", HoursPerDay*time.Hour + 12*time.Hour, false},
		{"unsupported unit", "24y", 0, true},
		{"garbage", "not-a-duration", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDuration(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for input %q, got duration %v", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for input %q: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("ParseDuration(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestFilterSpecTags(t *testing.T) {

	t.Run("hash-prefixed quoted tag key populates Tags", func(t *testing.T) {
		specYaml := `
kinds: [1]
"#e":
  - abc123
`
		var obj *FilterSpec
		if err := yaml.Unmarshal([]byte(specYaml), &obj); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := obj.SubscriptionFilter.Tags["e"]; len(got) != 1 || got[0] != "abc123" {
			t.Fatalf("expected Tags[e]=[abc123], got %v", obj.SubscriptionFilter.Tags)
		}
	})

	// A `.`-prefixed key (used by mistake in some hand-written filter
	// files) is silently ignored: nip01.SubscriptionFilter.UnmarshalJSON
	// only recognizes keys starting with `#`, so this produces neither an
	// error nor a populated Tags map.
	t.Run("dot-prefixed key silently does not populate Tags", func(t *testing.T) {
		specYaml := `
kinds: [1]
.d: myvalue
`
		var obj *FilterSpec
		if err := yaml.Unmarshal([]byte(specYaml), &obj); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(obj.SubscriptionFilter.Tags) != 0 {
			t.Fatalf("expected dot-prefixed key to be ignored, got Tags=%v", obj.SubscriptionFilter.Tags)
		}
	})

	// `since` looks BACKWARD by default (parseDurationUnit is called with
	// past=true), so an unsigned duration already means "N ago". An
	// explicit "-" prefix negates the duration a second time and pushes
	// the result into the future instead — the opposite of what the sign
	// suggests. This test locks in that (counter-intuitive but current)
	// behavior; see examples/filters.yaml for the user-facing warning.
	t.Run("since without a sign resolves to the past", func(t *testing.T) {
		now := time.Now()
		var obj *FilterSpec
		if err := yaml.Unmarshal([]byte(`since: "1w 2d"`), &obj); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if obj.SubscriptionFilter.Since >= uint64(now.Unix()) {
			t.Fatalf("expected since to be in the past, got=%d now=%d", obj.SubscriptionFilter.Since, now.Unix())
		}
	})

	t.Run("since with an explicit minus sign resolves to the future", func(t *testing.T) {
		now := time.Now()
		var obj *FilterSpec
		if err := yaml.Unmarshal([]byte(`since: "-1w 2d"`), &obj); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if obj.SubscriptionFilter.Since <= uint64(now.Unix()) {
			t.Fatalf("expected since to be in the future, got=%d now=%d", obj.SubscriptionFilter.Since, now.Unix())
		}
	})

	// ids/authors/tag values are all strings. An unquoted, all-digit YAML
	// scalar is read as a number, not a string, so it fails to unmarshal
	// into []string instead of being silently coerced.
	t.Run("unquoted all-digit ids fails to unmarshal", func(t *testing.T) {
		specYaml := `
ids:
  - 0000000000000000
`
		var obj *FilterSpec
		if err := yaml.Unmarshal([]byte(specYaml), &obj); err == nil {
			t.Fatalf("expected error unmarshalling unquoted numeric id, got none (ids=%v)", obj.SubscriptionFilter.IDs)
		}
	})

	t.Run("limit, search, and tags combined", func(t *testing.T) {
		specYaml := `
kinds: [1]
limit: 50
search: "lightning"
"#p":
  - deadbeef
`
		var obj *FilterSpec
		if err := yaml.Unmarshal([]byte(specYaml), &obj); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if obj.SubscriptionFilter.Limit != 50 {
			t.Fatalf("unexpected limit: %d", obj.SubscriptionFilter.Limit)
		}
		if obj.SubscriptionFilter.Search != "lightning" {
			t.Fatalf("unexpected search: %q", obj.SubscriptionFilter.Search)
		}
		if got := obj.SubscriptionFilter.Tags["p"]; len(got) != 1 || got[0] != "deadbeef" {
			t.Fatalf("unexpected tags: %v", obj.SubscriptionFilter.Tags)
		}
	})
}
