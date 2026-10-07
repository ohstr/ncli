package climatrix

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// coverageEntry is one command's row in coverage.yaml.
type coverageEntry struct {
	// Modes exercised: json (agent), text, tty (human), errors.
	Modes []string `json:"modes"`
	// Tests are function-name prefixes in this package that cover it.
	Tests []string `json:"tests"`
	// Pending, if set, says why the command isn't covered yet.
	Pending string `json:"pending"`
}

func loadCoverage(t *testing.T) map[string]coverageEntry {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(), "integration", "climatrix", "coverage.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cov := map[string]coverageEntry{}
	if err := yaml.Unmarshal(b, &cov); err != nil {
		t.Fatalf("coverage.yaml: %v", err)
	}
	return cov
}

// commandTree walks the real binary's --help output.
func commandTree(t *testing.T) []string {
	t.Helper()
	e := NewEnv(t)
	var out []string
	var walk func(path []string)
	walk = func(path []string) {
		r := e.Run(append(append([]string{}, path...), "--help")...)
		inList := false
		sc := bufio.NewScanner(strings.NewReader(r.Stdout + r.Stderr))
		var subs []string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "Available Commands:"):
				inList = true
				continue
			case inList && (line == "" || !strings.HasPrefix(line, " ")):
				inList = false
			}
			if !inList {
				continue
			}
			name := strings.Fields(line)[0]
			if name != "help" && name != "completion" {
				subs = append(subs, name)
			}
		}
		for _, s := range subs {
			p := append(append([]string{}, path...), s)
			out = append(out, strings.Join(p, " "))
			walk(p)
		}
	}
	walk(nil)
	sort.Strings(out)
	return out
}

var testFunc = regexp.MustCompile(`(?m)^func (Test\w+)\(t \*testing\.T\)`)

func packageTests(t *testing.T) []string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(repoRoot(), "integration", "climatrix", "*_test.go"))
	var names []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range testFunc.FindAllStringSubmatch(string(b), -1) {
			names = append(names, m[1])
		}
	}
	return names
}

// Every command in the binary has a coverage.yaml row naming tests that
// exist; no row names a command that's gone.
func TestCoverage_EveryCommandListed(t *testing.T) {
	cov := loadCoverage(t)
	tree := commandTree(t)
	if len(tree) < 90 {
		t.Fatalf("walked only %d commands; --help parsing is broken", len(tree))
	}
	inTree := map[string]bool{}
	for _, c := range tree {
		inTree[c] = true
		if _, ok := cov[c]; !ok {
			t.Errorf("command %q has no coverage.yaml entry", c)
		}
	}
	for c := range cov {
		if !inTree[c] {
			t.Errorf("coverage.yaml lists %q, which isn't a command", c)
		}
	}
	tests := packageTests(t)
	for c, e := range cov {
		if e.Pending == "" && len(e.Tests) == 0 {
			t.Errorf("%q: no tests and not pending", c)
		}
		for _, prefix := range e.Tests {
			found := false
			for _, name := range tests {
				if strings.HasPrefix(name, prefix) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%q: no test matches %q", c, prefix)
			}
		}
		for _, m := range e.Modes {
			switch m {
			case "json", "text", "tty", "errors":
			default:
				t.Errorf("%q: unknown mode %q", c, m)
			}
		}
	}
}
