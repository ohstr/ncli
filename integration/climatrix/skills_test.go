package climatrix

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// Every shipped skill's frontmatter is valid YAML with a name and a
// description; a skill installer skips one that isn't (ncli-huddle was).
func TestSkillsFrontmatter(t *testing.T) {
	var files []string
	for _, pat := range []string{"skills/*/SKILL.md", ".agents/skills/*/SKILL.md"} {
		m, _ := filepath.Glob(filepath.Join(repoRoot(), pat))
		files = append(files, m...)
	}
	if len(files) == 0 {
		t.Fatal("no SKILL.md files")
	}
	for _, f := range files {
		rel, _ := filepath.Rel(repoRoot(), f)
		t.Run(rel, func(t *testing.T) {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			parts := strings.SplitN(string(b), "---", 3)
			if len(parts) < 3 || strings.TrimSpace(parts[0]) != "" {
				t.Fatalf("no --- frontmatter block")
			}
			var fm struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			}
			if err := yaml.Unmarshal([]byte(parts[1]), &fm); err != nil {
				t.Fatalf("frontmatter isn't valid YAML: %v", err)
			}
			if fm.Name != filepath.Base(filepath.Dir(f)) {
				t.Errorf("name %q, want the directory name %q", fm.Name, filepath.Base(filepath.Dir(f)))
			}
			if fm.Description == "" {
				t.Errorf("no description")
			}
		})
	}
}
