package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ferdousbhai/wacli/skills"
)

func testSkillPaths(t *testing.T) (skillPaths, string) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755) // Claude Code is set up
	return skillPaths{
		dir:    filepath.Join(home, ".agents", "skills", "wacli"),
		claude: filepath.Join(home, ".claude", "skills", "wacli"),
	}, home
}

func TestSkillInstallLinksAndRemoves(t *testing.T) {
	p, _ := testSkillPaths(t)
	if _, err := p.install(false); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(p.claude, "SKILL.md")); string(got) != string(skills.Wacli) {
		t.Fatal("Claude Code link doesn't reach the skill")
	}
	if p.state() != skillCurrent {
		t.Errorf("state %s", p.state())
	}
	if _, err := p.remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(p.claude); !os.IsNotExist(err) {
		t.Error("link left behind")
	}
	if p.state() != skillMissing {
		t.Errorf("state %s", p.state())
	}
}

func TestSkillNeverOverwritesAHandWrittenOne(t *testing.T) {
	p, _ := testSkillPaths(t)
	os.MkdirAll(p.dir, 0o755)
	os.WriteFile(filepath.Join(p.dir, "SKILL.md"), []byte("mine"), 0o644)
	if _, err := p.install(false); err == nil {
		t.Fatal("overwrote an unmanaged skill")
	}
	if _, err := p.remove(); err == nil {
		t.Fatal("removed an unmanaged skill")
	}
	done, err := p.install(true)
	if err != nil {
		t.Fatal(err)
	}
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(filepath.Dir(p.dir)), "wacli-skill.bak-*"))
	if siblings, _ := filepath.Glob(p.dir + "*"); len(siblings) != 1 {
		t.Errorf("something next to the skill: %v", siblings)
	}
	if len(backups) != 1 || !strings.Contains(strings.Join(done, "\n"), "kept the previous skill") {
		t.Errorf("backup %v, done %v", backups, done)
	}
	if mine, _ := os.ReadFile(filepath.Join(backups[0], "SKILL.md")); string(mine) != "mine" {
		t.Error("backup lost the user's skill")
	}
}

func TestOutdatedSkillIsRefreshed(t *testing.T) {
	p, _ := testSkillPaths(t)
	p.install(false)
	os.WriteFile(filepath.Join(p.dir, skillMarker), []byte("revision 0\n"), 0o644)
	if p.state() != skillOutdated {
		t.Fatalf("state %s", p.state())
	}
	p.install(false)
	if p.state() != skillCurrent {
		t.Errorf("state %s", p.state())
	}
}

func TestClaudeLinkLeftAloneWhenItPointsElsewhere(t *testing.T) {
	p, home := testSkillPaths(t)
	other := filepath.Join(home, "other")
	os.MkdirAll(other, 0o755)
	os.MkdirAll(filepath.Dir(p.claude), 0o755)
	os.Symlink(other, p.claude)
	p.install(false)
	if target, _ := os.Readlink(p.claude); target != other {
		t.Errorf("link now %s", target)
	}
}

func TestOlderWacliDoesNotDowngradeTheSkill(t *testing.T) {
	p, _ := testSkillPaths(t)
	p.install(false)
	os.WriteFile(filepath.Join(p.dir, skillMarker), []byte(fmt.Sprintf("revision %d\n", skills.Revision+1)), 0o644)
	if p.state() != skillNewer {
		t.Fatalf("state %s", p.state())
	}
}
