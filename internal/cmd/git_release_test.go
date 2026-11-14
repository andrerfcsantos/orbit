package cmd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrerfcsantos/orbit/internal/cmd/internal/exit"
)

// releaseTree is a clone, in the working directory, of a remote released as
// v1.4.0 with a commit since.
func releaseTree(t *testing.T) (home, bare string) {
	t.Helper()
	home, root := gitTree(t)
	for _, name := range []string{"BUMP", "PRE", "TEAM", "PREFIX", "BRANCH", "REMOTE", "PUSH", "MESSAGE"} {
		t.Setenv("ORBIT_GIT_RELEASE_TAG_"+name, "")
		_ = os.Unsetenv("ORBIT_GIT_RELEASE_TAG_" + name)
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	bare = filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	git(root, "init", "--quiet", "--bare", "-b", "main", bare)
	git(root, "clone", "--quiet", bare, work)
	git(work, "commit", "--quiet", "--allow-empty", "-m", "first")
	git(work, "tag", "v1.4.0")
	git(work, "commit", "--quiet", "--allow-empty", "-m", "second")
	git(work, "push", "--quiet", "--tags", "origin", "main")
	t.Chdir(work)
	return home, bare
}

func remoteTags(t *testing.T, bare string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", bare, "tag", "--list").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestGitReleaseTagNeedsToBeToldWithoutATerminal(t *testing.T) {
	_, bare := releaseTree(t)
	if code, _, errOut := orbit("git", "release-tag"); code != exit.UsageCode || !strings.Contains(errOut, "--yes") {
		t.Errorf("exit %d: %s", code, errOut)
	}
	if code, _, errOut := orbit("git", "release-tag", "--json"); code != exit.UsageCode {
		t.Errorf("--json: exit %d: %s", code, errOut)
	}
	if code, _, errOut := orbit("git", "release-tag", "--version", "2.0.0", "--bump", "patch", "--yes"); code != exit.UsageCode {
		t.Errorf("--version and --bump: exit %d: %s", code, errOut)
	}
	if tags := remoteTags(t, bare); tags != "v1.4.0" {
		t.Errorf("tags = %q", tags)
	}
}

func TestGitReleaseTagDryRunThenRelease(t *testing.T) {
	_, bare := releaseTree(t)
	code, out, errOut := orbit("git", "release-tag", "--dry-run")
	if code != exit.Success || !strings.Contains(out, "v1.4.0 → v1.5.0") || !strings.Contains(out, "Would tag") {
		t.Fatalf("exit %d, printed:\n%s%s", code, out, errOut)
	}
	code, out, errOut = orbit("git", "release-tag", "--pre", "rc", "--dry-run", "--json")
	var planned map[string]any
	if code != exit.Success || json.Unmarshal([]byte(out), &planned) != nil || planned["tag"] != "v1.5.0-rc.1" || planned["created"] != false {
		t.Fatalf("exit %d, printed %q: %s", code, out, errOut)
	}
	if tags := remoteTags(t, bare); tags != "v1.4.0" {
		t.Fatalf("a dry run tagged: %q", tags)
	}

	t.Setenv("ORBIT_GIT_RELEASE_TAG_BUMP", "patch")
	code, out, errOut = orbit("git", "release-tag", "--yes", "--json")
	var released map[string]any
	if code != exit.Success || json.Unmarshal([]byte(out), &released) != nil || released["tag"] != "v1.4.1" || released["pushed"] != true {
		t.Fatalf("exit %d, printed %q: %s", code, out, errOut)
	}
	if tags := remoteTags(t, bare); tags != "v1.4.0\nv1.4.1" {
		t.Errorf("tags = %q", tags)
	}

	// The commit is released now.
	if code, _, errOut := orbit("git", "release-tag", "--yes"); code != exit.Failure || !strings.Contains(errOut, "released already") {
		t.Errorf("exit %d: %s", code, errOut)
	}
}

// A branch is an argument, and arguments are not repeated in the log.
func TestGitReleaseTagDoesNotLogTheBranchGiven(t *testing.T) {
	const sentinel = "SENTINELBRANCH"
	home, _ := releaseTree(t)
	code, _, errOut := orbit("git", "release-tag", "--branch", sentinel, "--yes")
	if code != exit.Failure || !strings.Contains(errOut, "push it first") {
		t.Errorf("exit %d: %s", code, errOut)
	}
	entries := readLogEntries(t, home)
	if len(entries) == 0 {
		t.Fatal("nothing was logged")
	}
	for _, entry := range entries {
		if strings.Contains(entry, sentinel) {
			t.Errorf("the log repeats the branch:\n%s", entry)
		}
	}
}

// Nor is a prefix, which the tag made carries.
func TestGitReleaseTagDoesNotLogThePrefixGiven(t *testing.T) {
	const sentinel = "SENTINELPREFIX-"
	home, _ := releaseTree(t)
	code, out, errOut := orbit("git", "release-tag", "--prefix", sentinel, "--push=false", "--yes")
	if code != exit.Success || !strings.Contains(out, sentinel+"0.1.0") {
		t.Fatalf("exit %d, printed:\n%s%s", code, out, errOut)
	}
	entries := readLogEntries(t, home)
	if len(entries) == 0 {
		t.Fatal("nothing was logged")
	}
	for _, entry := range entries {
		if strings.Contains(entry, "SENTINELPREFIX") {
			t.Errorf("the log repeats the prefix:\n%s", entry)
		}
	}
}
