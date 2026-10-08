package ncli

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Every skills/*/SKILL.md in the repo is embedded, with its description.
func TestLoadSkillsMatchesRepo(t *testing.T) {
	onDisk, _ := filepath.Glob("../../skills/*/SKILL.md")
	all, err := loadSkills()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 || len(all) != len(onDisk) {
		t.Fatalf("embedded %d skills, repo has %d", len(all), len(onDisk))
	}
	for _, s := range all {
		if s.Description == "" {
			t.Errorf("%s: no description", s.Name)
		}
		if !slices.Contains(s.Files, "SKILL.md") {
			t.Errorf("%s: SKILL.md missing from files %v", s.Name, s.Files)
		}
	}
}

func TestInstallSkills(t *testing.T) {
	all, err := loadSkills()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	status := func(rs []skillInstallResult) map[string]string {
		m := map[string]string{}
		for _, r := range rs {
			m[r.Name] = r.Status
		}
		return m
	}

	rs, err := installSkills(dir, all, false)
	if err != nil {
		t.Fatal(err)
	}
	for name, st := range status(rs) {
		if st != "installed" {
			t.Errorf("first install: %s is %s", name, st)
		}
	}

	rs, err = installSkills(dir, all, false)
	if err != nil {
		t.Fatal(err)
	}
	for name, st := range status(rs) {
		if st != "unchanged" {
			t.Errorf("reinstall: %s is %s", name, st)
		}
	}

	// An edited file blocks the install, and nothing else is written.
	edited := filepath.Join(dir, all[0].Name, "SKILL.md")
	if err := os.WriteFile(edited, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	removed := filepath.Join(dir, all[1].Name, "SKILL.md")
	if err := os.Remove(removed); err != nil {
		t.Fatal(err)
	}
	_, err = installSkills(dir, all, false)
	var conflict *skillConflictError
	if !errors.As(err, &conflict) || conflict.paths[0] != edited {
		t.Fatalf("want conflict on %s, got %v", edited, err)
	}
	if b, _ := os.ReadFile(edited); string(b) != "mine" {
		t.Error("conflict overwrote the edited file")
	}
	if _, err := os.Stat(removed); !errors.Is(err, os.ErrNotExist) {
		t.Error("conflict still wrote other files")
	}

	rs, err = installSkills(dir, all, true)
	if err != nil {
		t.Fatal(err)
	}
	st := status(rs)
	if st[all[0].Name] != "updated" || st[all[1].Name] != "updated" || st[all[2].Name] != "unchanged" {
		t.Errorf("force: got %v", st)
	}
}

func TestFirstSentence(t *testing.T) {
	for in, want := range map[string]string{
		"One. Two.":              "One.",
		"Decode circlehub1... x": "Decode circlehub1... x",
		"a... b. c":              "a... b.",
		"No period":              "No period",
	} {
		if got := firstSentence(in); got != want {
			t.Errorf("firstSentence(%q) = %q, want %q", in, got, want)
		}
	}
}
