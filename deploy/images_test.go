// Package deploy holds no Go code; this test keeps every image the Compose
// stacks, the Helm chart, the Dockerfiles, the examples and the
// integration rigs pull pinned, so a release always names exact images,
// and keeps the relay image at the latest release.
package deploy

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// relayRepo is the relay image, kept at the latest release.
const relayRepo = "ghcr.io/ohstr/ncli"

// chartDir is the Helm chart whose helper renders every chart image.
const chartDir = "helm/ncli-relay"

// floatingTags move to a new image over time, so they pin nothing on their
// own; a digest must come with them. A per-arch or suffixed variant
// (latest-amd64, rc-arm64, edge-abc123) floats the same way.
var floatingTags = map[string]bool{
	"latest": true, "edge": true, "rc": true, "nightly": true,
	"stable": true, "main": true, "master": true,
}

var digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// splitRef splits an image reference into its name, tag and digest.
func splitRef(ref string) (name, tag, digest string) {
	name, digest, _ = strings.Cut(ref, "@")
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name, tag = name[:i], name[i+1:]
	}
	return name, tag, digest
}

// checkImageRef reports why ref isn't pinned, or "" when it is: it carries
// a sha256 digest, or a tag that isn't floating.
func checkImageRef(ref string) string {
	if ref == "" {
		return "empty image reference"
	}
	if strings.ContainsAny(ref, "${}") {
		return "unresolved variable or template"
	}
	name, tag, digest := splitRef(ref)
	switch {
	case name == "":
		return "no image name"
	case strings.Contains(ref, "@"):
		if !digestRe.MatchString(digest) {
			return fmt.Sprintf("malformed digest %q", digest)
		}
		return ""
	case tag == "":
		return "no tag or digest"
	}
	if base, _, _ := strings.Cut(tag, "-"); floatingTags[base] {
		return fmt.Sprintf("floating tag %q without a digest", tag)
	}
	return ""
}

var varRe = regexp.MustCompile(`\$\$|\$\{([A-Za-z_][A-Za-z0-9_]*)(?:(:?)([-?+])([^}]*))?\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// expandVars resolves ${VAR}, $VAR, ${VAR:-default}, ${VAR-default},
// ${VAR:?msg}, ${VAR:+alt} and $$ the way Compose and Docker do, with
// lookup giving the variables that are set: for Compose none (an unset
// .env pulls the defaults), for a Dockerfile its ARG defaults. A variable
// left without a value is an error, since what it pulls can't be checked.
func expandVars(ref string, lookup func(string) (string, bool)) (string, error) {
	var errs []error
	out := varRe.ReplaceAllStringFunc(ref, func(m string) string {
		if m == "$$" {
			return "$"
		}
		sub := varRe.FindStringSubmatch(m)
		name, colon, op, word := sub[1], sub[2], sub[3], sub[4]
		if name == "" {
			name = sub[5]
		}
		val, set := lookup(name)
		if colon != "" && val == "" {
			set = false
		}
		switch op {
		case "-":
			if set {
				return val
			}
			return word
		case "+":
			if set {
				return word
			}
			return ""
		}
		if !set {
			errs = append(errs, fmt.Errorf("%s has no default", m))
		}
		return val
	})
	return out, errors.Join(errs...)
}

func noVars(string) (string, bool) { return "", false }

type imageRef struct{ where, ref string }

// scanResult is what scanning a file finds: the images it pulls and what
// stops one from being checked.
type scanResult struct {
	refs     []imageRef
	problems []string
}

func (s *scanResult) add(where, ref string) { s.refs = append(s.refs, imageRef{where, ref}) }

func (s *scanResult) problem(format string, args ...any) {
	s.problems = append(s.problems, fmt.Sprintf(format, args...))
}

func (s *scanResult) merge(o scanResult) {
	s.refs = append(s.refs, o.refs...)
	s.problems = append(s.problems, o.problems...)
}

// dockerfileImages finds each FROM image, resolving ARGs declared before
// the first FROM to their defaults; a build stage named by AS is no image.
func dockerfileImages(path string, data []byte) scanResult {
	var res scanResult
	args := map[string]string{}
	lookup := func(name string) (string, bool) { v, ok := args[name]; return v, ok }
	stages := map[string]bool{"scratch": true}
	seenFrom := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		where := fmt.Sprintf("%s:%d", path, n)
		switch strings.ToUpper(fields[0]) {
		case "ARG":
			if seenFrom {
				continue // a stage's ARG never reaches a FROM
			}
			for _, f := range fields[1:] {
				if name, val, ok := strings.Cut(f, "="); ok {
					args[name] = strings.Trim(val, `"'`)
				}
			}
		case "FROM":
			seenFrom = true
			rest := fields[1:]
			for len(rest) > 0 && strings.HasPrefix(rest[0], "--") {
				rest = rest[1:]
			}
			if len(rest) == 0 {
				continue
			}
			ref, err := expandVars(rest[0], lookup)
			if err != nil {
				res.problem("%s: %s: %v", where, rest[0], err)
			} else if !stages[strings.ToLower(ref)] {
				res.add(where, ref)
			}
			if len(rest) >= 3 && strings.EqualFold(rest[1], "AS") {
				stages[strings.ToLower(rest[2])] = true
			}
		}
	}
	return res
}

// composeImages finds each service's image with its variables at their
// defaults. A service that builds its image pulls nothing, and only a
// base compose.yaml (not an override layered on it) must name one or the
// other.
func composeImages(path string, data []byte) scanResult {
	var res scanResult
	var doc struct {
		Services map[string]struct {
			Image string `yaml:"image"`
			Build any    `yaml:"build"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		res.problem("%s: %v", path, err)
		return res
	}
	base := filepath.Base(path)
	isBase := base == "compose.yaml" || base == "compose.yml"
	for name, svc := range doc.Services {
		where := path + ": services." + name
		switch {
		case svc.Build != nil:
		case svc.Image == "":
			if isBase {
				res.problem("%s: no image and no build", where)
			}
		default:
			ref, err := expandVars(svc.Image, noVars)
			if err != nil {
				res.problem("%s: %s: %v", where, svc.Image, err)
				continue
			}
			res.add(where, ref)
		}
	}
	return res
}

// walkImages calls str for every string `image` (or `...Image`) value and
// split for every map that names an image in parts: a repository, or a
// registry and a name.
func walkImages(key string, v any, str func(key, ref string), split func(key string, m map[string]any)) {
	switch v := v.(type) {
	case map[string]any:
		_, hasRepo := v["repository"].(string)
		_, hasRegistry := v["registry"].(string)
		_, hasName := v["name"].(string)
		if hasRepo || (hasRegistry && hasName) {
			split(key, v)
			return
		}
		for k, child := range v {
			childKey := strings.TrimPrefix(key+"."+k, ".")
			if s, ok := child.(string); ok && (k == "image" || strings.HasSuffix(k, "Image")) {
				str(childKey, s)
				continue
			}
			walkImages(childKey, child, str, split)
		}
	case []any:
		for i, child := range v {
			walkImages(fmt.Sprintf("%s[%d]", key, i), child, str, split)
		}
	}
}

// joinRef builds registry/repository:tag@digest from an image's parts.
func joinRef(m map[string]any) (ref, tag, digest string) {
	registry, _ := m["registry"].(string)
	repo, _ := m["repository"].(string)
	if repo == "" {
		repo, _ = m["name"].(string)
	}
	tag, _ = m["tag"].(string)
	digest, _ = m["digest"].(string)
	if registry != "" {
		repo = strings.TrimSuffix(registry, "/") + "/" + repo
	}
	return repo, tag, digest
}

// chartInfo is what the chart's image helper falls back to: the relay
// image's tag (appVersion) and the digests pinned by repository:tag.
type chartInfo struct {
	appVersion string
	pins       map[string]string
}

var pinsRe = regexp.MustCompile(`(?s)define "ncli-relay\.imageDigests" -\}\}(.*?)\{\{-? end`)

func parseChart(chartYAML, helpers []byte) (chartInfo, error) {
	var c struct {
		AppVersion string `yaml:"appVersion"`
	}
	if err := yaml.Unmarshal(chartYAML, &c); err != nil {
		return chartInfo{}, err
	}
	info := chartInfo{appVersion: c.AppVersion, pins: map[string]string{}}
	m := pinsRe.FindSubmatch(helpers)
	if m == nil {
		return chartInfo{}, errors.New(`no "ncli-relay.imageDigests" in _helpers.tpl`)
	}
	if err := yaml.Unmarshal(m[1], &info.pins); err != nil {
		return chartInfo{}, fmt.Errorf("ncli-relay.imageDigests: %w", err)
	}
	return info, nil
}

// valuesImages builds each image in a values.yaml as the chart's
// "ncli-relay.image" helper renders it: the root image's empty tag falls
// back to appVersion, and an empty digest to the one pinned for exactly
// that repository and tag.
func valuesImages(path string, data []byte, chart chartInfo) scanResult {
	var res scanResult
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		res.problem("%s: %v", path, err)
		return res
	}
	walkImages("", root, func(key, ref string) {
		res.add(path+": "+key, ref)
	}, func(key string, m map[string]any) {
		repo, tag, digest := joinRef(m)
		if tag == "" && key == "image" {
			tag = chart.appVersion
		}
		ref := repo
		if tag != "" {
			ref += ":" + tag
		}
		if digest == "" {
			digest = chart.pins[ref]
		}
		if digest != "" {
			ref += "@" + digest
		}
		res.add(path+": "+key, ref)
	})
	return res
}

// manifestImages finds the images in a YAML file of one or more documents
// (a Kubernetes manifest, say), as written.
func manifestImages(path string, data []byte) scanResult {
	var res scanResult
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for i := 0; ; i++ {
		var doc any
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			res.problem("%s: %v", path, err)
			break
		}
		where := path
		if i > 0 {
			where = fmt.Sprintf("%s[doc %d]", path, i)
		}
		walkImages("", doc, func(key, ref string) {
			res.add(where+": "+key, ref)
		}, func(key string, m map[string]any) {
			ref, tag, digest := joinRef(m)
			if tag != "" {
				ref += ":" + tag
			}
			if digest != "" {
				ref += "@" + digest
			}
			res.add(where+": "+key, ref)
		})
	}
	return res
}

var templateImageRe = regexp.MustCompile(`(?:^|[\s{,])image:\s*(.*)$`)

// templateProblems makes every image in a chart template, block or flow
// style, come from the "ncli-relay.image" helper, so none bypasses the
// values checked above.
func templateProblems(path string, data []byte) []string {
	var out []string
	for n, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		m := templateImageRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		val := strings.TrimPrefix(strings.TrimPrefix(m[1], "{{-"), "{{")
		if !strings.HasPrefix(strings.TrimSpace(val), `include "ncli-relay.image"`) {
			out = append(out, fmt.Sprintf("%s:%d: image not built by the ncli-relay.image helper: %s", path, n+1, strings.TrimSpace(line)))
		}
	}
	return out
}

// scanFile picks the scanner for a file by where it is and its name, or
// none: Dockerfiles anywhere, compose files under deploy/ and
// integration/, the chart's values and templates, and every YAML file
// under examples/.
func scanFile(path string, data []byte, chart chartInfo) (scanResult, bool) {
	base := filepath.Base(path)
	inExamples := strings.HasPrefix(filepath.ToSlash(path), "../examples/")
	switch {
	case strings.HasPrefix(base, "Dockerfile"):
		return dockerfileImages(path, data), true
	case strings.HasPrefix(base, "compose") && (strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml")):
		return composeImages(path, data), true
	case inExamples && (strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml")):
		return manifestImages(path, data), true
	case strings.HasPrefix(filepath.ToSlash(path), "../"):
		return scanResult{}, false
	case filepath.Base(filepath.Dir(path)) == "templates":
		return scanResult{problems: templateProblems(path, data)}, true
	case base == "values.yaml":
		return valuesImages(path, data, chart), true
	}
	return scanResult{}, false
}

// scanRepo scans deploy/ (the working directory), examples/ and
// integration/.
func scanRepo(t *testing.T) scanResult {
	t.Helper()
	chart, err := parseChart(mustRead(t, filepath.Join(chartDir, "Chart.yaml")),
		mustRead(t, filepath.Join(chartDir, "templates", "_helpers.tpl")))
	if err != nil {
		t.Fatal(err)
	}
	var res scanResult
	for _, root := range []string{".", "../examples", "../integration"} {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if name := d.Name(); path != root && (strings.HasPrefix(name, ".") || name == "node_modules") {
					return filepath.SkipDir
				}
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if r, ok := scanFile(path, data, chart); ok {
				res.merge(r)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return res
}

func TestImagesArePinned(t *testing.T) {
	res := scanRepo(t)
	if len(res.refs) == 0 {
		t.Fatal("found no images")
	}
	for _, p := range res.problems {
		t.Error(p)
	}
	for _, r := range res.refs {
		if why := checkImageRef(r.ref); why != "" {
			t.Errorf("%s: %s: %s", r.where, r.ref, why)
		}
	}
}

var headingRe = regexp.MustCompile(`(?m)^## \[([^\]]+)\]`)

// releasedVersion is the latest release: the CHANGELOG's second heading,
// since the top one is the release in progress (released only once the
// release workflow tags it).
func releasedVersion(changelog []byte) (string, error) {
	m := headingRe.FindAllSubmatch(changelog, 2)
	if len(m) < 2 {
		return "", errors.New("CHANGELOG.md has fewer than two ## [version] headings")
	}
	return string(m[1][1]), nil
}

var envVersionRe = regexp.MustCompile(`(?m)^#?\s*NCLI_VERSION=(\S*)`)

// TestRelayImageIsLatestRelease keeps every relay image (the chart's
// appVersion, the stacks' NCLI_VERSION defaults, the examples and rigs)
// at the latest release, so starting a CHANGELOG section for the next
// release fails until the pin moves to the one just cut.
func TestRelayImageIsLatestRelease(t *testing.T) {
	want, err := releasedVersion(mustRead(t, "../CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, r := range scanRepo(t).refs {
		name, tag, _ := splitRef(r.ref)
		if name != relayRepo {
			continue
		}
		found++
		if tag != want {
			t.Errorf("%s: %s: want tag %s, the latest release in CHANGELOG.md", r.where, r.ref, want)
		}
	}
	if found == 0 {
		t.Fatalf("found no %s image", relayRepo)
	}
	envs, err := filepath.Glob("stacks/*/.env.example")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range envs {
		for _, m := range envVersionRe.FindAllSubmatch(mustRead(t, path), -1) {
			if got := string(m[1]); got != want {
				t.Errorf("%s: NCLI_VERSION=%s, want %s", path, got, want)
			}
		}
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
