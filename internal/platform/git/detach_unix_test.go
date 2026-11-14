//go:build unix

package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A helper left holding git's output once git is done, with nothing stopped,
// does not outlive the run either.
func TestAGitDoneLeavesNothingHoldingItsOutput(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	alias := "alias.linger=!sleep 30 & echo $! > " + pidFile + ".tmp; mv " + pidFile + ".tmp " + pidFile
	if _, err := (Runner{Grace: 200 * time.Millisecond}).Run(context.Background(), dir, "-c", alias, "linger"); !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("err = %v", err)
	}
	waitGone(t, pidFile)
}

// Nor when git fails, whose exit error hides that something held its output.
func TestAGitFailedLeavesNothingHoldingItsOutput(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	alias := "alias.linger=!sleep 30 & echo $! > " + pidFile + ".tmp; mv " + pidFile + ".tmp " + pidFile + "; exit 1"
	if _, err := (Runner{Grace: 200 * time.Millisecond}).Run(context.Background(), dir, "-c", alias, "linger"); err == nil {
		t.Fatal("a git that failed succeeded")
	}
	waitGone(t, pidFile)
}

// The ceiling is a list, which a parent named with a colon would be read as:
// git is refused rather than run unfenced there.
func TestAParentWithTheListSeparatorIsRefused(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(root, "team:archive", "repo")
	gitIn(t, root, "init", "--quiet", repo)
	if _, err := (Runner{}).Run(context.Background(), repo, "status"); err == nil || !strings.Contains(err.Error(), "parent directory") {
		t.Errorf("err = %v", err)
	}
}

// waitGone fails unless the process whose pid the file holds is gone soon.
func waitGone(t *testing.T, pidFile string) {
	t.Helper()
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatal("the helper git started is still running")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A helper that ignores the interrupt does not outlive the git that started
// it: once the grace is over, git's whole process group is killed.
func TestAStoppedGitLeavesNothingRunning(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	alias := "alias.hang=!trap '' INT; sleep 30 & echo $! > " + pidFile + ".tmp; mv " + pidFile + ".tmp " + pidFile + "; wait"
	// Stopped once the helper runs, however long a loaded machine takes to start it.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			if _, err := os.Stat(pidFile); err == nil {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	if _, err := (Runner{Grace: 200 * time.Millisecond}).Run(ctx, dir, "-c", alias, "hang"); err == nil {
		t.Fatal("a git stopped succeeded")
	}
	waitGone(t, pidFile)
}
