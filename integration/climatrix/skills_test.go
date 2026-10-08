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

// ncli skills serves the same skills from the binary: list, show and an
// install that never overwrites an edited file without --force.
func TestSkillsCmd(t *testing.T) {
	e := NewEnv(t)
	onDisk, _ := filepath.Glob(filepath.Join(repoRoot(), "skills", "*", "SKILL.md"))

	var list struct {
		Skills []struct {
			Name, Description string
			Files             []string
		}
	}
	e.MustOK(t, "skills", "list", "--json").JSON(t, &list)
	if len(list.Skills) != len(onDisk) {
		t.Fatalf("list: %d skills, repo has %d", len(list.Skills), len(onDisk))
	}
	if out := e.MustOK(t, "skills", "list").Stdout; !strings.Contains(out, "ncli-query") {
		t.Errorf("text list: %q", out)
	}

	want, _ := os.ReadFile(filepath.Join(repoRoot(), "skills", "ncli-query", "SKILL.md"))
	if got := e.MustOK(t, "skills", "show", "ncli-query").Stdout; got != string(want) {
		t.Error("show ncli-query differs from skills/ncli-query/SKILL.md")
	}
	var show struct{ Content string }
	e.MustOK(t, "skills", "show", "ncli-query", "references/filters-reference.md", "--json").JSON(t, &show)
	if !strings.Contains(show.Content, "filters.yaml") {
		t.Errorf("show reference: %q", show.Content)
	}
	e.Run(t, "skills", "show", "nope", "--json").ExpectErr(t, "not_found")
	e.Run(t, "skills", "show", "ncli-query", "../ncli-miner/SKILL.md", "--json").ExpectErr(t, "not_found")
	e.Run(t, "skills", "show", "--json").ExpectErr(t, "usage")

	// Default dir is ~/.claude/skills; HOME is the Env dir.
	dir := filepath.Join(e.Dir, ".claude", "skills")
	type installOut struct {
		Dir    string
		Skills []struct{ Name, Status string }
	}
	var first installOut
	e.MustOK(t, "skills", "install", "--json").JSON(t, &first)
	if first.Dir != dir || len(first.Skills) != len(onDisk) || first.Skills[0].Status != "installed" {
		t.Fatalf("install: %+v", first)
	}
	if _, err := os.Stat(filepath.Join(dir, "ncli-query", "references", "filters-reference.md")); err != nil {
		t.Errorf("references not installed: %v", err)
	}
	var again installOut
	e.MustOK(t, "skills", "install", "--json").JSON(t, &again)
	for _, s := range again.Skills {
		if s.Status != "unchanged" {
			t.Errorf("reinstall: %s %s", s.Name, s.Status)
		}
	}

	edited := filepath.Join(dir, "ncli-miner", "SKILL.md")
	if err := os.WriteFile(edited, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := e.Run(t, "skills", "install", "--json").ExpectErr(t, "conflict"); r.Input != edited {
		t.Errorf("conflict input %q, want %q", r.Input, edited)
	}
	e.MustOK(t, "skills", "install", "ncli-miner", "--force", "--json")
	if b, _ := os.ReadFile(edited); string(b) == "mine" {
		t.Error("--force didn't overwrite")
	}
	e.Run(t, "skills", "install", "bogus", "--json").ExpectErr(t, "not_found")
}
