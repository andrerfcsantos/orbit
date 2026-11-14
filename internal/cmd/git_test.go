package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrerfcsantos/orbit/internal/cmd/internal/exit"
	"github.com/andrerfcsantos/orbit/internal/platform/build"
)

// gitTree makes a directory holding a repository two levels down, with one old
// branch, and runs the tests' git and orbit apart from the developer's own
// configuration.
func gitTree(t *testing.T) (home, root string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	home = t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"),
		[]byte("[user]\n\tname = orbit\n\temail = orbit@example.com\n[commit]\n\tgpgsign = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, name := range []string{"ORBIT_GIT_JSON", "ORBIT_GIT_CONCURRENCY", "ORBIT_GIT_TIMEOUT", "ORBIT_GIT_BRANCH_DELETE_OLDER_THAN"} {
		t.Setenv(name, "")
		_ = os.Unsetenv(name)
	}

	root = t.TempDir()
	repo := filepath.Join(root, "team", "service")
	gitRun := func(env []string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitRun(nil, "init", "--quiet", "-b", "main", repo)
	gitRun(nil, "-C", repo, "commit", "--quiet", "--allow-empty", "-m", "first")
	old := []string{"GIT_AUTHOR_DATE=2020-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2020-01-01T00:00:00Z"}
	gitRun(nil, "-C", repo, "switch", "--quiet", "-c", "ancient")
	gitRun(old, "-C", repo, "commit", "--quiet", "--allow-empty", "-m", "old")
	gitRun(nil, "-C", repo, "switch", "--quiet", "main")
	t.Chdir(root)
	return home, root
}

func orbit(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := Execute(Inputs{Name: "orbit", BuildInfo: build.NewInfo("v1", "c1", "d1"), Stdout: &out, Stderr: &errOut}, args...)
	return code, out.String(), errOut.String()
}

func TestGitListFindsTheRepositoriesBeneath(t *testing.T) {
	gitTree(t)
	code, out, errOut := orbit("git", "list")
	if code != exit.Success {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(out, "team/service") || !strings.Contains(out, "main") {
		t.Errorf("printed:\n%s", out)
	}

	code, out, errOut = orbit("git", "list", "--json")
	var results []map[string]any
	if code != exit.Success || json.Unmarshal([]byte(out), &results) != nil || len(results) != 1 {
		t.Fatalf("exit %d, printed %q: %s", code, out, errOut)
	}
	if detail := results[0]["detail"].(map[string]any); detail["branch"] != "main" {
		t.Errorf("detail = %v", detail)
	}
}

// A path is an argument, and arguments are not repeated in the log.
func TestGitDoesNotRepeatAPathItCannotUse(t *testing.T) {
	const sentinel = "SENTINELPATH"
	home, _ := gitTree(t)
	code, out, errOut := orbit("git", "list", sentinel)
	if code != exit.UsageCode || !strings.Contains(errOut, "does not exist") {
		t.Errorf("exit %d: %s", code, errOut)
	}
	if strings.Contains(out+errOut, sentinel) {
		t.Errorf("the output repeats the path:\n%s", errOut)
	}
	for _, entry := range readLogEntries(t, home) {
		if strings.Contains(entry, sentinel) {
			t.Errorf("the log repeats the path:\n%s", entry)
		}
	}
}

func TestGitBranchDeleteAsksFirst(t *testing.T) {
	gitTree(t)
	branches := func() string {
		out, err := exec.Command("git", "-C", "team/service", "branch", "--format=%(refname:short)").Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}

	// Nobody is at a terminal to say yes.
	if code, _, errOut := orbit("git", "branch", "delete"); code != exit.UsageCode || !strings.Contains(errOut, "--yes") {
		t.Errorf("exit %d: %s", code, errOut)
	}
	if code, _, errOut := orbit("git", "branch", "delete", "--json"); code != exit.UsageCode {
		t.Errorf("--json without --yes: exit %d: %s", code, errOut)
	}
	if code, _, errOut := orbit("git", "branch", "delete", "--older-than", "6m", "--yes"); code != exit.UsageCode {
		t.Errorf("--older-than 6m: exit %d: %s", code, errOut)
	}
	code, out, errOut := orbit("git", "branch", "delete", "--dry-run")
	if code != exit.Success || !strings.Contains(out, "ancient") {
		t.Errorf("--dry-run: exit %d, printed:\n%s%s", code, out, errOut)
	}
	if got := branches(); got != "ancient\nmain" {
		t.Fatalf("branches before deleting: %q", got)
	}

	code, out, errOut = orbit("git", "branch", "delete", "--yes")
	if code != exit.Success || !strings.Contains(out, "deleted 1 local branch") {
		t.Errorf("exit %d, printed:\n%s%s", code, out, errOut)
	}
	if got := branches(); got != "main" {
		t.Errorf("branches after deleting: %q", got)
	}
}

func TestGitCleanAsksFirst(t *testing.T) {
	_, root := gitTree(t)
	scratch := filepath.Join(root, "team", "service", "scratch.txt")
	if err := os.WriteFile(scratch, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := orbit("git", "clean"); code != exit.UsageCode {
		t.Errorf("exit %d: %s", code, errOut)
	}
	if code, out, _ := orbit("git", "clean", "--dry-run"); code != exit.Success || !strings.Contains(out, "scratch.txt") {
		t.Errorf("--dry-run: exit %d:\n%s", code, out)
	}
	if _, err := os.Stat(scratch); err != nil {
		t.Fatal("asking removed the file")
	}
	if code, out, errOut := orbit("git", "clean", "--yes", "--json"); code != exit.Success || !strings.Contains(out, `"scratch.txt"`) {
		t.Errorf("exit %d, printed:\n%s%s", code, out, errOut)
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Error("the file is still there")
	}
}
