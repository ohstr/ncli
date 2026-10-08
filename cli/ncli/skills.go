package ncli

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/skills"
	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"
)

var skillsCmd = &cobra.Command{
	Use:   "skills",
	Short: "List, show or install the agent skills built into ncli",
	Long: `ncli ships agent skills (SKILL.md guides, one per command group) inside the
binary, so an agent with only ncli on PATH can read guidance that matches
the installed version.`,
	Example: `  ncli skills list
  ncli skills show ncli-query
  ncli skills install`,
	// Embedded data only: no config or log setup.
	PersistentPreRun: func(cmd *cobra.Command, args []string) {},
	RunE:             common.RequireSubcommand,
}

var skillsListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List the built-in skills",
	Example: `  ncli skills list --json`,
	Args:    common.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		all, err := loadSkills()
		if err != nil {
			return common.RuntimeError(cmd, err)
		}

		if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
			common.PrintJSON(map[string]any{"skills": all})
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, s := range all {
			fmt.Fprintf(w, "%s\t%s\n", s.Name, firstSentence(s.Description))
		}
		return w.Flush()
	},
}

var skillsShowCmd = &cobra.Command{
	Use:   "show <name> [file]",
	Short: "Print a skill's SKILL.md, or one of its files",
	Example: `  ncli skills show ncli-query
  ncli skills show ncli-query references/filters-reference.md`,
	Args: func(cmd *cobra.Command, args []string) error {
		if err := common.MinimumNArgs(1)(cmd, args); err != nil {
			return err
		}
		return common.MaximumNArgs(2)(cmd, args)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := findSkill(cmd, args[0])
		if err != nil {
			return err
		}

		file := "SKILL.md"
		if len(args) == 2 {
			file = path.Clean(args[1])
		}
		data, err := fs.ReadFile(skills.FS, path.Join(s.Name, file))
		if err != nil || !fs.ValidPath(file) {
			return common.NotFoundError(cmd, args[len(args)-1],
				fmt.Errorf("%s has no file %q (files: %s)", s.Name, file, strings.Join(s.Files, ", ")))
		}

		if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
			common.PrintJSON(map[string]any{
				"name":        s.Name,
				"description": s.Description,
				"file":        file,
				"content":     string(data),
				"files":       s.Files,
			})
			return nil
		}
		_, err = os.Stdout.Write(data)
		return err
	},
}

var (
	skillsInstallDir   string
	skillsInstallForce bool
)

var skillsInstallCmd = &cobra.Command{
	Use:   "install [name...]",
	Short: "Copy built-in skills into an agent's skills directory",
	Long: `Copy built-in skills (all, or the ones named) into an agent's skills
directory, default ~/.claude/skills. A file that already exists with
different content (edited locally, or from another ncli version) aborts the
install with nothing written, unless --force.`,
	Example: `  ncli skills install
  ncli skills install ncli-query ncli-relay-ops
  ncli skills install --dir .agents/skills --force`,
	RunE: func(cmd *cobra.Command, args []string) error {
		all, err := loadSkills()
		if err != nil {
			return common.RuntimeError(cmd, err)
		}
		selected := all
		if len(args) > 0 {
			selected = nil
			for _, name := range args {
				s, err := findSkill(cmd, name)
				if err != nil {
					return err
				}
				selected = append(selected, s)
			}
		}

		dir := skillsInstallDir
		if dir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return common.RuntimeError(cmd, err)
			}
			dir = filepath.Join(home, ".claude", "skills")
		}
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}

		results, err := installSkills(dir, selected, skillsInstallForce)
		var conflict *skillConflictError
		if errors.As(err, &conflict) {
			return common.ConflictError(cmd, conflict.paths[0], err)
		}
		if err != nil {
			return common.RuntimeError(cmd, err)
		}

		if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
			common.PrintJSON(map[string]any{"dir": dir, "skills": results})
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, r := range results {
			fmt.Fprintf(w, "%s\t%s\t%s\n", r.Status, r.Name, r.Path)
		}
		return w.Flush()
	},
}

type skillInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Files       []string `json:"files"`
}

type skillInstallResult struct {
	Name   string `json:"name"`
	Status string `json:"status"` // installed, updated or unchanged
	Path   string `json:"path"`
}

type skillConflictError struct{ paths []string }

func (e *skillConflictError) Error() string {
	return fmt.Sprintf("%d file(s) differ from the built-in copy, first %s; nothing written, rerun with --force to overwrite",
		len(e.paths), e.paths[0])
}

// loadSkills reads every embedded skill's frontmatter and file list.
func loadSkills() ([]skillInfo, error) {
	entries, err := fs.ReadDir(skills.FS, ".")
	if err != nil {
		return nil, err
	}
	all := []skillInfo{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := fs.ReadFile(skills.FS, path.Join(e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		var fm struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if parts := strings.SplitN(string(raw), "---", 3); len(parts) == 3 {
			if err := yaml.Unmarshal([]byte(parts[1]), &fm); err != nil {
				return nil, fmt.Errorf("%s/SKILL.md frontmatter: %w", e.Name(), err)
			}
		}
		s := skillInfo{Name: e.Name(), Description: fm.Description, Files: []string{}}
		err = fs.WalkDir(skills.FS, e.Name(), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			s.Files = append(s.Files, strings.TrimPrefix(p, e.Name()+"/"))
			return nil
		})
		if err != nil {
			return nil, err
		}
		sort.Strings(s.Files)
		all = append(all, s)
	}
	return all, nil
}

func findSkill(cmd *cobra.Command, name string) (skillInfo, error) {
	all, err := loadSkills()
	if err != nil {
		return skillInfo{}, common.RuntimeError(cmd, err)
	}
	names := make([]string, 0, len(all))
	for _, s := range all {
		if s.Name == name {
			return s, nil
		}
		names = append(names, s.Name)
	}
	return skillInfo{}, common.NotFoundError(cmd, name,
		fmt.Errorf("no built-in skill %q (have: %s)", name, strings.Join(names, ", ")))
}

// installSkills copies each skill under dir. Any differing file aborts the
// whole install before anything is written, unless force.
func installSkills(dir string, selected []skillInfo, force bool) ([]skillInstallResult, error) {
	type pending struct {
		target string
		data   []byte
	}
	var (
		writes    = map[string][]pending{}
		conflicts []string
	)
	for _, s := range selected {
		for _, f := range s.Files {
			data, err := fs.ReadFile(skills.FS, path.Join(s.Name, f))
			if err != nil {
				return nil, err
			}
			target := filepath.Join(dir, s.Name, filepath.FromSlash(f))
			existing, err := os.ReadFile(target)
			switch {
			case err == nil && bytes.Equal(existing, data):
				continue
			case err == nil && !force:
				conflicts = append(conflicts, target)
			case err != nil && !errors.Is(err, fs.ErrNotExist):
				return nil, err
			}
			writes[s.Name] = append(writes[s.Name], pending{target, data})
		}
	}
	if len(conflicts) > 0 {
		return nil, &skillConflictError{paths: conflicts}
	}

	results := []skillInstallResult{}
	for _, s := range selected {
		skillDir := filepath.Join(dir, s.Name)
		status := "unchanged"
		if len(writes[s.Name]) > 0 {
			status = "updated"
			if _, err := os.Stat(skillDir); errors.Is(err, fs.ErrNotExist) {
				status = "installed"
			}
		}
		for _, w := range writes[s.Name] {
			if err := os.MkdirAll(filepath.Dir(w.target), 0o755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(w.target, w.data, 0o644); err != nil {
				return nil, err
			}
		}
		results = append(results, skillInstallResult{Name: s.Name, Status: status, Path: skillDir})
	}
	return results, nil
}

// firstSentence trims a skill description for the text listing. An
// ellipsis ("circlehub1... ") doesn't end a sentence.
func firstSentence(s string) string {
	for i := 1; i+1 < len(s); i++ {
		if s[i] == '.' && s[i+1] == ' ' && s[i-1] != '.' {
			return s[:i+1]
		}
	}
	return s
}

func init() {
	skillsInstallCmd.Flags().StringVar(&skillsInstallDir, "dir", "", "Skills directory to install into (default ~/.claude/skills)")
	skillsInstallCmd.Flags().BoolVar(&skillsInstallForce, "force", false, "Overwrite files that differ from the built-in copy")

	skillsCmd.AddCommand(skillsListCmd, skillsShowCmd, skillsInstallCmd)
	RootCmd.AddCommand(skillsCmd)
}
