// Package deploy holds no Go code; this test keeps every image the
// Compose stacks, the Helm chart and the Dockerfiles under deploy/ pull
// pinned, so a release always names exact images.
package deploy

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// floatingTags move to a new image over time, so they pin nothing on their
// own; a digest must come with them.
var floatingTags = map[string]bool{"latest": true, "edge": true, "rc": true}

var digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// checkImageRef reports why ref isn't pinned, or "" when it is: it carries
// a sha256 digest, or a tag that isn't floating.
func checkImageRef(ref string) string {
	if ref == "" {
		return "empty image reference"
	}
	name, digest, hasDigest := strings.Cut(ref, "@")
	if hasDigest {
		if !digestRe.MatchString(digest) {
			return fmt.Sprintf("malformed digest %q", digest)
		}
		return ""
	}
	tag := ""
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		tag = name[i+1:]
	}
	switch {
	case tag == "":
		return "no tag or digest"
	case floatingTags[tag]:
		return fmt.Sprintf("floating tag %q without a digest", tag)
	}
	return ""
}

var composeVarRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:?-([^}]*))?\}`)

// expandDefaults resolves Compose ${VAR:-default} to its default, which is
// what an unset .env pulls; a variable with no default can't be checked.
func expandDefaults(ref string) (string, error) {
	var err error
	out := composeVarRe.ReplaceAllStringFunc(ref, func(m string) string {
		sub := composeVarRe.FindStringSubmatch(m)
		if sub[2] == "" {
			err = fmt.Errorf("${%s} has no default", sub[1])
		}
		return sub[3]
	})
	return out, err
}

type imageRef struct{ where, ref string }

func TestCheckImageRef(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for ref, pinned := range map[string]bool{
		"getmeili/meilisearch:v1.35":                true,
		"localhost:5000/ncli:0.8.0":                 true,
		"cgr.dev/chainguard/minio:latest@" + digest: true,
		"cgr.dev/chainguard/minio@" + digest:        true,
		"cgr.dev/chainguard/minio:latest":           false,
		"cgr.dev/chainguard/minio":                  false,
		"localhost:5000/ncli":                       false,
		"ghcr.io/ohstr/ncli:edge":                   false,
		"minio:latest@sha256:abc":                   false,
	} {
		if got := checkImageRef(ref) == ""; got != pinned {
			t.Errorf("checkImageRef(%q) pinned = %v, want %v", ref, got, pinned)
		}
	}
}

func TestDeployImagesArePinned(t *testing.T) {
	var refs []imageRef
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		base := d.Name()
		switch {
		case strings.HasPrefix(base, "Dockerfile"):
			refs = append(refs, dockerfileImages(t, path)...)
		case base == "compose.yaml" || base == "compose.yml":
			refs = append(refs, composeImages(t, path)...)
		case base == "values.yaml" && filepath.Base(filepath.Dir(path)) != "templates":
			refs = append(refs, valuesImages(t, path)...)
		case filepath.Base(filepath.Dir(path)) == "templates":
			checkTemplateImages(t, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) == 0 {
		t.Fatal("found no images under deploy/")
	}
	for _, r := range refs {
		if why := checkImageRef(r.ref); why != "" {
			t.Errorf("%s: %s: %s", r.where, r.ref, why)
		}
	}
}

func dockerfileImages(t *testing.T, path string) []imageRef {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	stages := map[string]bool{"scratch": true}
	var refs []imageRef
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || !strings.EqualFold(fields[0], "FROM") {
			continue
		}
		args := fields[1:]
		for len(args) > 0 && strings.HasPrefix(args[0], "--") {
			args = args[1:]
		}
		if len(args) == 0 {
			continue
		}
		if !stages[strings.ToLower(args[0])] {
			refs = append(refs, imageRef{fmt.Sprintf("%s:%d", path, n), args[0]})
		}
		if len(args) >= 3 && strings.EqualFold(args[1], "AS") {
			stages[strings.ToLower(args[2])] = true
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return refs
}

func composeImages(t *testing.T, path string) []imageRef {
	var doc struct {
		Services map[string]struct {
			Image string `yaml:"image"`
			Build any    `yaml:"build"`
		} `yaml:"services"`
	}
	readYAML(t, path, &doc)
	var refs []imageRef
	for name, svc := range doc.Services {
		where := path + ": services." + name
		if svc.Image == "" {
			if svc.Build == nil {
				t.Errorf("%s: no image and no build", where)
			}
			continue
		}
		ref, err := expandDefaults(svc.Image)
		if err != nil {
			t.Errorf("%s: %s: %v", where, svc.Image, err)
			continue
		}
		refs = append(refs, imageRef{where, ref})
	}
	return refs
}

// valuesImages finds every {repository, tag, digest} map in a chart's
// values.yaml and builds the reference the chart's "ncli-relay.image"
// helper renders from it: an empty tag falls back to Chart.yaml's
// appVersion. A stack's values.yaml (an override file next to no
// Chart.yaml) is checked only where it names a repository.
func valuesImages(t *testing.T, path string) []imageRef {
	var root map[string]any
	readYAML(t, path, &root)
	appVersion := ""
	if chart := filepath.Join(filepath.Dir(path), "Chart.yaml"); fileExists(chart) {
		var c struct {
			AppVersion string `yaml:"appVersion"`
		}
		readYAML(t, chart, &c)
		appVersion = c.AppVersion
	}
	var refs []imageRef
	var walk func(key string, v any)
	walk = func(key string, v any) {
		m, ok := v.(map[string]any)
		if !ok {
			return
		}
		if repo, ok := m["repository"].(string); ok {
			tag, _ := m["tag"].(string)
			if tag == "" {
				tag = appVersion
			}
			ref := repo
			if tag != "" {
				ref += ":" + tag
			}
			if digest, _ := m["digest"].(string); digest != "" {
				ref += "@" + digest
			}
			refs = append(refs, imageRef{path + ": " + key, ref})
			return
		}
		for k, child := range m {
			walk(strings.TrimPrefix(key+"."+k, "."), child)
		}
	}
	walk("", root)
	return refs
}

// checkTemplateImages makes every image in a chart template come from the
// "ncli-relay.image" helper, so none bypasses the values checked above.
func checkTemplateImages(t *testing.T, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for n, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		if strings.HasPrefix(trimmed, "image:") && !strings.Contains(trimmed, `include "ncli-relay.image"`) {
			t.Errorf("%s:%d: image not built by the ncli-relay.image helper: %s", path, n+1, trimmed)
		}
	}
}

func readYAML(t *testing.T, path string, out any) {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
