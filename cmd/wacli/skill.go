package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ferdousbhai/wacli/internal/fsutil"

	"github.com/ferdousbhai/wacli/skills"
	"github.com/spf13/cobra"
)

// The skill is installed once, in the shared agent location Codex reads, and
// linked for Claude Code. wacli only touches skill directories carrying its
// marker, so a hand-written skill is never overwritten.
const skillMarker = ".managed-by-wacli"

type skillPaths struct {
	dir    string // ~/.agents/skills/wacli
	claude string // ~/.claude/skills/wacli
}

func defaultSkillPaths() (skillPaths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return skillPaths{}, err
	}
	return skillPaths{
		dir:    filepath.Join(home, ".agents", "skills", "wacli"),
		claude: filepath.Join(home, ".claude", "skills", "wacli"),
	}, nil
}

type skillState string

const (
	skillMissing   skillState = "not installed"
	skillCurrent   skillState = "installed"
	skillOutdated  skillState = "outdated"
	skillNewer     skillState = "installed by a newer wacli"
	skillUnmanaged skillState = "not managed by wacli"
)

func (p skillPaths) state() skillState {
	if _, err := os.Lstat(p.dir); errors.Is(err, os.ErrNotExist) {
		return skillMissing
	}
	marker, err := os.ReadFile(filepath.Join(p.dir, skillMarker))
	if err != nil {
		return skillUnmanaged
	}
	var rev int
	if _, err := fmt.Sscanf(string(marker), "revision %d", &rev); err != nil {
		return skillOutdated // a marker from before revisions
	}
	switch {
	case rev < skills.Revision:
		return skillOutdated
	case rev > skills.Revision:
		return skillNewer
	}
	return skillCurrent
}

// install writes the skill and links it for Claude Code. force adopts a
// skill wacli didn't install, keeping the old one aside.
func (p skillPaths) install(force bool) ([]string, error) {
	var done []string
	if p.state() == skillUnmanaged {
		if !force {
			return nil, fmt.Errorf("%s exists and wasn't installed by wacli; pass --force to replace it (the old one is kept as a backup)", p.dir)
		}
		// Outside the skills folder, or agents would load it as a second skill.
		backup := filepath.Join(filepath.Dir(filepath.Dir(p.dir)), fmt.Sprintf("wacli-skill.bak-%d", time.Now().Unix()))
		if err := os.Rename(p.dir, backup); err != nil {
			return nil, err
		}
		done = append(done, "kept the previous skill as "+backup)
	}
	if err := p.writeFiles(); err != nil {
		return nil, err
	}
	done = append(done, "installed "+p.dir+" (Codex and other agents reading ~/.agents/skills)")
	if msg := p.linkClaude(); msg != "" {
		done = append(done, msg)
	}
	return done, nil
}

// writeFiles writes the skill and its marker (the marker last, so a crash
// in between reads as outdated and is fixed by the next run).
func (p skillPaths) writeFiles() error {
	if err := os.MkdirAll(p.dir, 0o755); err != nil {
		return err
	}
	p.removeTempFiles()
	write := func(name string, data []byte) error {
		return fsutil.WriteAtomic(filepath.Join(p.dir, name), "", 0o644, func(w *bufio.Writer) error {
			_, err := w.Write(data)
			return err
		})
	}
	if err := write("SKILL.md", skills.Wacli); err != nil {
		return err
	}
	return write(skillMarker, []byte(fmt.Sprintf("revision %d\n", skills.Revision)))
}

// removeTempFiles removes what a crash mid-write left in the skill folder
// (WriteAtomic's ".<name>.*" temp files there).
func (p skillPaths) removeTempFiles() {
	for _, pat := range []string{".SKILL.md.*", "." + skillMarker + ".*"} {
		matches, _ := filepath.Glob(filepath.Join(p.dir, pat))
		for _, m := range matches {
			os.Remove(m)
		}
	}
}

// linkClaude links the skill into Claude Code's skills when Claude Code is
// set up and nothing else is there.
func (p skillPaths) linkClaude() string {
	if _, err := os.Stat(filepath.Dir(filepath.Dir(p.claude))); err != nil {
		return "" // no ~/.claude: Claude Code isn't set up
	}
	if target, err := os.Readlink(p.claude); err == nil {
		if resolve(p.claude, target) == p.dir {
			return "linked for Claude Code: " + p.claude
		}
		return "left " + p.claude + " alone (it points elsewhere)"
	}
	if _, err := os.Lstat(p.claude); err == nil {
		return "left " + p.claude + " alone (not a link wacli made)"
	}
	if err := os.MkdirAll(filepath.Dir(p.claude), 0o755); err != nil {
		return "couldn't link for Claude Code: " + err.Error()
	}
	rel, err := filepath.Rel(filepath.Dir(p.claude), p.dir)
	if err != nil {
		rel = p.dir
	}
	if err := os.Symlink(rel, p.claude); err != nil {
		return "couldn't link for Claude Code: " + err.Error()
	}
	return "linked for Claude Code: " + p.claude
}

func (p skillPaths) remove() ([]string, error) {
	switch p.state() {
	case skillMissing:
		return []string{"no skill installed"}, nil
	case skillUnmanaged:
		return nil, fmt.Errorf("%s wasn't installed by wacli; remove it yourself", p.dir)
	}
	var done []string
	if target, err := os.Readlink(p.claude); err == nil && resolve(p.claude, target) == p.dir {
		if err := os.Remove(p.claude); err != nil {
			return nil, err
		}
		done = append(done, "removed "+p.claude)
	}
	p.removeTempFiles()
	for _, f := range []string{"SKILL.md", skillMarker} {
		if err := os.Remove(filepath.Join(p.dir, f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if err := os.Remove(p.dir); err != nil {
		return nil, fmt.Errorf("left %s: it has files wacli didn't put there", p.dir)
	}
	return append(done, "removed "+p.dir), nil
}

// refreshSkill updates an installed, managed skill to the one in this
// build, the first time a new build runs. Failures are silent: it's a
// courtesy, and `wacli doctor` reports the state.
func refreshSkill() {
	p, err := defaultSkillPaths()
	if err != nil || p.state() != skillOutdated {
		return
	}
	// Files only: a Claude Code link the user removed stays removed.
	_ = p.writeFiles()
}

func resolve(link, target string) string {
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(link), target)
	}
	return filepath.Clean(target)
}

func newSkillCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Install the agent skill that teaches coding agents to use wacli",
		Long: `The skill tells coding agents (Claude Code, Codex, …) where the chat folders
are, how to read them, and how to send safely. It is installed in
~/.agents/skills/wacli and linked into ~/.claude/skills when Claude Code is set
up; an installed skill updates itself when wacli is updated.`,
	}
	var force bool
	install := &cobra.Command{
		Use:   "install",
		Short: "Install or update the skill",
		RunE: func(*cobra.Command, []string) error {
			p, err := defaultSkillPaths()
			if err != nil {
				return err
			}
			done, err := p.install(force)
			for _, d := range done {
				fmt.Println(d)
			}
			return err
		},
	}
	install.Flags().BoolVar(&force, "force", false, "replace a skill wacli didn't install (kept as a backup)")
	remove := &cobra.Command{
		Use:   "remove",
		Short: "Remove the skill wacli installed",
		RunE: func(*cobra.Command, []string) error {
			p, err := defaultSkillPaths()
			if err != nil {
				return err
			}
			done, err := p.remove()
			for _, d := range done {
				fmt.Println(d)
			}
			return err
		},
	}
	show := &cobra.Command{
		Use:   "show",
		Short: "Print the skill",
		Run: func(*cobra.Command, []string) {
			os.Stdout.Write(skills.Wacli)
		},
	}
	cmd.AddCommand(install, remove, show)
	return cmd
}
