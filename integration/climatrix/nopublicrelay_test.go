package climatrix

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Tests never dial a public relay. A test file may still name one as a
// literal parse/format input; those files are listed here with the reason.
// Anything else naming a public host fails.
var publicHostParseOnly = map[string]string{
	"cli/bunker/board_test.go":                    "status-line rendering",
	"cli/bunker/nostrconnect_conformance_test.go": "parses real-world nostrconnect:// URIs",
	"cli/bunker/uri_test.go":                      "bunker:// URI building",
	"cli/ncli/find_test.go":                       "flag mutual-exclusion check",
	"cli/ncli/ping_test.go":                       "args validation",
	"cli/relay/huddle_test.go":                    "AUTH event naming another relay",
	"client/prefs/relayurl_test.go":               "relay URL normalization",
	"client/prefs_test.go":                        "prefs file round-trip",
	"huddle/client/client_test.go":                "AUTH event naming another relay",
	"huddle/sfu/sfu_test.go":                      "AUTH event naming another relay",
}

var (
	wsURL     = regexp.MustCompile(`wss?://([a-z0-9-]+(?:\.[a-z0-9-]+)+)`)
	localHost = regexp.MustCompile(`^(localhost|127\.0\.0\.1|0\.0\.0\.0|.*example(\.[a-z]+)?|.*\.(test|invalid|local|localhost))$`)
)

func TestNoPublicRelayInTests(t *testing.T) {
	root := repoRoot()
	var offenders []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".claude", "node_modules", "build":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if _, ok := publicHostParseOnly[rel]; ok {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		hosts := map[string]bool{}
		for _, m := range wsURL.FindAllStringSubmatch(string(b), -1) {
			if !localHost.MatchString(m[1]) {
				hosts[m[1]] = true
			}
		}
		if len(hosts) > 0 {
			var hs []string
			for h := range hosts {
				hs = append(hs, h)
			}
			sort.Strings(hs)
			offenders = append(offenders, rel+": "+strings.Join(hs, " "))
		}
		return nil
	})
	if len(offenders) > 0 {
		t.Fatalf("test files naming public relays (use a local relay, or list as parse-only with a reason):\n%s",
			strings.Join(offenders, "\n"))
	}
}
