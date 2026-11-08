package deploy

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type workflowStep struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
}

// TestReleaseVerifiesRelayImage keeps the release workflow failing when
// GoReleaser finishes without the versioned relay image: the deploy pins
// name it, so a release without it would ship pins nobody can pull.
func TestReleaseVerifiesRelayImage(t *testing.T) {
	var wf struct {
		Jobs map[string]struct {
			Steps []workflowStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(mustRead(t, "../.github/workflows/release.yml"), &wf); err != nil {
		t.Fatal(err)
	}
	steps := wf.Jobs["release"].Steps
	goreleaser := -1
	for i, s := range steps {
		if strings.HasPrefix(s.Uses, "goreleaser/goreleaser-action") {
			goreleaser = i
		}
	}
	if goreleaser < 0 {
		t.Fatal("release job has no GoReleaser step")
	}
	for _, s := range steps[goreleaser+1:] {
		if !strings.Contains(s.Run, "docker buildx imagetools inspect") ||
			!strings.Contains(s.Run, relayRepo+":$VERSION") {
			continue
		}
		if got := s.Env["VERSION"]; got != "${{ steps.changelog.outputs.version }}" {
			t.Errorf("step %q: VERSION=%q, want the resolved release version", s.Name, got)
		}
		if !strings.Contains(s.Run, "exit 1") {
			t.Errorf("step %q does not fail the run when the image is missing", s.Name)
		}
		return
	}
	t.Errorf("no step after GoReleaser inspects %s:$VERSION", relayRepo)
}
