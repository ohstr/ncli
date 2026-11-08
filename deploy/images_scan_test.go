package deploy

import (
	"sort"
	"strings"
	"testing"
)

var (
	digestA = "sha256:" + strings.Repeat("a", 64)
	digestB = "sha256:" + strings.Repeat("b", 64)
)

func refsOf(res scanResult) []string {
	var out []string
	for _, r := range res.refs {
		out = append(out, r.ref)
	}
	sort.Strings(out)
	return out
}

func TestCheckImageRef(t *testing.T) {
	for _, tc := range []struct {
		ref    string
		pinned bool
	}{
		{"getmeili/meilisearch:v1.35", true},
		{"localhost:5000/ncli:0.8.0", true},
		{"cgr.dev/chainguard/minio:latest@" + digestA, true},
		{"cgr.dev/chainguard/minio@" + digestA, true},
		{"ghcr.io/ohstr/ncli:0.8.0-rc.50", true},
		{"node:22-alpine", true},
		{"cgr.dev/chainguard/minio:latest", false},
		{"cgr.dev/chainguard/minio", false},
		{"localhost:5000/ncli", false},
		{"ghcr.io/ohstr/ncli:edge", false},
		{"ghcr.io/ohstr/ncli:latest-amd64", false},
		{"ghcr.io/ohstr/ncli:rc-arm64", false},
		{"ghcr.io/ohstr/ncli:edge-1a2b3c", false},
		{"busybox:stable", false},
		{"minio:latest@sha256:abc", false},
		{"ghcr.io/ohstr/ncli:${NCLI_VERSION}", false},
		{"", false},
	} {
		if got := checkImageRef(tc.ref) == ""; got != tc.pinned {
			t.Errorf("checkImageRef(%q) pinned = %v, want %v", tc.ref, got, tc.pinned)
		}
	}
}

func TestExpandVars(t *testing.T) {
	set := func(name string) (string, bool) {
		v, ok := map[string]string{"SET": "1.2", "EMPTY": ""}[name]
		return v, ok
	}
	for _, tc := range []struct {
		in, want string
		wantErr  bool
	}{
		{"x:${V:-1.0}", "x:1.0", false},
		{"x:${V-1.0}", "x:1.0", false},
		{"x:${SET:-1.0}", "x:1.2", false},
		{"x:${EMPTY:-1.0}", "x:1.0", false},
		{"x:${EMPTY-1.0}", "x:", false},
		{"x:$SET", "x:1.2", false},
		{"x:${V:+alt}1.0", "x:1.0", false},
		{"x:$$1", "x:$1", false},
		{"x:${V:?set V}", "x:", true},
		{"x:${V?set V}", "x:", true},
		{"x:${V}", "x:", true},
		{"x:$V", "x:", true},
	} {
		got, err := expandVars(tc.in, set)
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("expandVars(%q) = %q, %v; want %q, err %v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestComposeImages(t *testing.T) {
	for _, tc := range []struct {
		name, path, yaml string
		refs             []string
		problems         int
	}{
		{"default", "compose.yaml", "services:\n  a:\n    image: x:${V:-1.0}\n", []string{"x:1.0"}, 0},
		{"required var", "compose.yaml", "services:\n  a:\n    image: x:${V:?set V}\n", nil, 1},
		{"bare var", "compose.yaml", "services:\n  a:\n    image: x:${V}\n", nil, 1},
		{"build names its image", "compose.yaml", "services:\n  a:\n    build: .\n    image: ncli:dev\n", nil, 0},
		{"no image", "compose.yaml", "services:\n  a:\n    command: [x]\n", nil, 1},
		{"override", "compose.local.yaml", "services:\n  a:\n    entrypoint: [x]\n", nil, 0},
		{"merge key", "compose.yaml", "x-r: &r\n  image: x:latest\nservices:\n  a:\n    <<: *r\n", []string{"x:latest"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := composeImages(tc.path, []byte(tc.yaml))
			if got := refsOf(res); strings.Join(got, ",") != strings.Join(tc.refs, ",") || len(res.problems) != tc.problems {
				t.Errorf("refs %v problems %v; want %v and %d problems", got, res.problems, tc.refs, tc.problems)
			}
		})
	}
}

func TestDockerfileImages(t *testing.T) {
	for _, tc := range []struct {
		name, file string
		refs       []string
		problems   int
	}{
		{"plain", "FROM alpine:3.16\n", []string{"alpine:3.16"}, 0},
		{"stage", "FROM golang:1.26 AS build\nFROM build\nFROM scratch\n", []string{"golang:1.26"}, 0},
		{"platform flag", "FROM --platform=$BUILDPLATFORM node:22-alpine\n", []string{"node:22-alpine"}, 0},
		{"arg default", "ARG GO=1.26\nFROM golang:${GO}\n", []string{"golang:1.26"}, 0},
		{"quoted arg default", "ARG BASE=\"alpine:3.16\"\nFROM $BASE\n", []string{"alpine:3.16"}, 0},
		{"floating arg default", "ARG TAG=latest\nFROM alpine:${TAG}\n", []string{"alpine:latest"}, 0},
		{"arg without default", "ARG BASE\nFROM ${BASE}\n", nil, 1},
		{"arg in a stage", "FROM alpine:3.16\nARG T=1\nFROM x:${T}\n", []string{"alpine:3.16"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := dockerfileImages("Dockerfile", []byte(tc.file))
			if got := refsOf(res); strings.Join(got, ",") != strings.Join(tc.refs, ",") || len(res.problems) != tc.problems {
				t.Errorf("refs %v problems %v; want %v and %d problems", got, res.problems, tc.refs, tc.problems)
			}
		})
	}
}

func TestValuesImages(t *testing.T) {
	chart := chartInfo{appVersion: "0.8.0", pins: map[string]string{"cgr.dev/m:latest": digestA}}
	for _, tc := range []struct {
		name, yaml string
		refs       []string
	}{
		{"relay falls back to appVersion", "image:\n  repository: r/ncli\n  tag: \"\"\n", []string{"r/ncli:0.8.0"}},
		{"other empty tag has none", "meili:\n  image:\n    repository: m\n    tag: \"\"\n", []string{"m"}},
		{"pinned default", "minio:\n  image:\n    repository: cgr.dev/m\n    tag: latest\n", []string{"cgr.dev/m:latest@" + digestA}},
		{"mirror gets no pin", "minio:\n  image:\n    repository: mirror/m\n    tag: latest\n", []string{"mirror/m:latest"}},
		{"own digest", "minio:\n  image:\n    repository: mirror/m\n    tag: latest\n    digest: " + digestB + "\n", []string{"mirror/m:latest@" + digestB}},
		{"string form", "init:\n  image: busybox:latest\n", []string{"busybox:latest"}},
		{"suffixed key", "minio:\n  clientImage: mc:1.0\n", []string{"mc:1.0"}},
		{"registry and name", "x:\n  image:\n    registry: docker.io\n    name: bitnami/redis\n    tag: \"7.2\"\n", []string{"docker.io/bitnami/redis:7.2"}},
		{"registry and repository", "x:\n  image:\n    registry: docker.io/\n    repository: redis\n    tag: latest\n", []string{"docker.io/redis:latest"}},
		{"list", "init:\n  - image: a:1\n  - image: b:latest\n", []string{"a:1", "b:latest"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := refsOf(valuesImages("values.yaml", []byte(tc.yaml), chart))
			if strings.Join(got, ",") != strings.Join(tc.refs, ",") {
				t.Errorf("refs %v, want %v", got, tc.refs)
			}
		})
	}
}

func TestManifestImages(t *testing.T) {
	doc := "kind: Pod\nspec:\n  containers:\n    - name: a\n      image: a:1\n---\nkind: Pod\nspec:\n  containers: [{name: b, image: \"b:latest\"}]\n"
	got := refsOf(manifestImages("pod.yaml", []byte(doc)))
	if want := "a:1,b:latest"; strings.Join(got, ",") != want {
		t.Errorf("refs %v, want %s", got, want)
	}
}

func TestGoImages(t *testing.T) {
	src := "package x\n\nconst blossomImage = \"ghcr.io/hzrd149/blossom-server:latest\"\n" +
		"var (\n\tminioImage string = \"cgr.dev/m@" + digestA + "\"\n\tother = \"a/b:latest\"\n)\n" +
		"func f() { relayImage := \"x:1\"; _ = relayImage }\n"
	res := goImages("../integration/x_test.go", []byte(src))
	if got := refsOf(res); strings.Join(got, ",") != "cgr.dev/m@"+digestA+",ghcr.io/hzrd149/blossom-server:latest" {
		t.Errorf("refs %v", got)
	}
	for _, r := range res.refs {
		if strings.HasSuffix(r.where, "blossomImage") && checkImageRef(r.ref) == "" {
			t.Errorf("%s: floating %s passes as pinned", r.where, r.ref)
		}
	}
	if _, ok := scanFile("../integration/climatrix/x_test.go", []byte(src), chartInfo{}); !ok {
		t.Error("a Go file under integration/ is not scanned")
	}
	if _, ok := scanFile("../cli/x.go", []byte(src), chartInfo{}); ok {
		t.Error("a Go file outside integration/ is scanned")
	}
}

func TestTemplateProblems(t *testing.T) {
	for _, tc := range []struct {
		name, line string
		bad        bool
	}{
		{"helper", `          image: {{ include "ncli-relay.image" (dict "image" .Values.image "default" "") | quote }}`, false},
		{"trimmed helper", `  image: {{- include "ncli-relay.image" (dict "image" .Values.image "default" "") | quote }}`, false},
		{"list item", `        - image: "{{ .Values.x.repository }}:{{ .Values.x.tag }}"`, true},
		{"block", `          image: busybox:latest`, true},
		{"flow", `      containers: [{name: init, image: busybox:latest}]`, true},
		{"flow helper", `      containers: [{name: init, image: {{ include "ncli-relay.image" (dict "image" .Values.i "default" "") | quote }}}]`, false},
		{"pull policy", `          imagePullPolicy: {{ .Values.image.pullPolicy }}`, false},
		{"comment", `  # image: busybox:latest`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(templateProblems("t.yaml", []byte(tc.line))) > 0; got != tc.bad {
				t.Errorf("flagged = %v, want %v", got, tc.bad)
			}
		})
	}
}

func TestAllowedRelayTags(t *testing.T) {
	const two = "# Changelog\n\n## [0.8.0-rc.52]\n\n## [0.8.0-rc.51]\n\n## [0.8.0-rc.50]\n"
	for _, tc := range []struct {
		name, changelog, release string
		want                     []string
	}{
		{"next release on top", two, "", []string{"0.8.0-rc.52", "0.8.0-rc.51"}},
		{"releasing", two, "0.8.0-rc.52", []string{"0.8.0-rc.52"}},
		{"just released", "## [0.8.0-rc.51]\n\n## [0.8.0-rc.50]\n", "", []string{"0.8.0-rc.51", "0.8.0-rc.50"}},
		{"unreleased on top", "## [Unreleased]\n\n## [0.9.0]\n", "", []string{"0.9.0"}},
		{"one heading", "## [0.1.0]\n", "", []string{"0.1.0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := allowedRelayTags([]byte(tc.changelog), tc.release)
			if err != nil || strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("allowedRelayTags = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	if _, err := allowedRelayTags([]byte("## [Unreleased]\n"), ""); err == nil {
		t.Error("no version heading: want an error")
	}
}

func TestParseChart(t *testing.T) {
	helpers := "{{- define \"ncli-relay.imageDigests\" -}}\n\"cgr.dev/m:latest\": " + digestA + "\n{{- end -}}\n"
	info, err := parseChart([]byte("appVersion: \"0.8.0\"\n"), []byte(helpers))
	if err != nil || info.appVersion != "0.8.0" || info.pins["cgr.dev/m:latest"] != digestA {
		t.Errorf("parseChart = %+v, %v", info, err)
	}
}
