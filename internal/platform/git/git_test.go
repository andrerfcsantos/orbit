package git

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestDiscoverFindsTheRepositoriesAtEveryDepth(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{
		"a/.git",
		"b/c/d/.git",
		"b/e/.git",
		// Beneath a repository: that repository's own.
		"a/nested/.git",
		"skipped/f/.git",
		"empty/g",
	} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A worktree's .git is a file.
	if err := os.MkdirAll(filepath.Join(root, "worktree"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "worktree", ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Links are not followed, so a loop cannot trap the walk.
	if err := os.Symlink(root, filepath.Join(root, "b", "loop")); err != nil {
		t.Fatal(err)
	}

	found, err := Discover(context.Background(), root, func(_, name string) bool { return name == "skipped" })
	if err != nil {
		t.Fatal(err)
	}
	var relative []string
	for _, path := range found {
		rel, _ := filepath.Rel(root, path)
		relative = append(relative, rel)
	}
	if want := []string{"a", "b/c/d", "b/e", "worktree"}; !slices.Equal(relative, want) {
		t.Errorf("found %q, want %q", relative, want)
	}

	self, err := Discover(context.Background(), filepath.Join(root, "a"), nil)
	if err != nil || len(self) != 1 {
		t.Errorf("a repository given is itself: %q, %v", self, err)
	}
}

func TestDiscoverStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Discover(ctx, t.TempDir(), nil); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// A shell exporting GIT_DIR, or a hook's environment, would otherwise point
// every git at one repository, whichever directory it runs in.
func TestRunIgnoresARepositoryTheEnvironmentNames(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	one, other := filepath.Join(root, "one"), filepath.Join(root, "other")
	gitIn(t, root, "init", "--quiet", one)
	gitIn(t, root, "init", "--quiet", other)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)

	out, err := Runner{}.Run(context.Background(), one, "rev-parse", "--show-toplevel")
	if err != nil || strings.TrimSpace(out) != one {
		t.Errorf("git ran in %q, %v", out, err)
	}
}

// A pathspec orbit makes literal stays literal: GIT_ICASE_PATHSPECS would make
// :(literal)readme.md name README.md too.
func TestRunKeepsPathspecsAsOrbitWritesThem(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	gitIn(t, root, "init", "--quiet", root)
	if err := os.WriteFile(filepath.Join(root, "README.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_ICASE_PATHSPECS", "1")
	out, err := Runner{}.Run(context.Background(), root, "ls-files", "--others", "--", ":(literal)readme.md")
	if err != nil || strings.TrimSpace(out) != "" {
		t.Errorf("listed %q, %v", out, err)
	}
}

func TestEnvironmentNamesAreMatchedAsThePlatformDoes(t *testing.T) {
	kept := passedOn([]string{"git_dir=/elsewhere", "GIT_DIR=/elsewhere", "PATH=/bin"})
	want := []string{"git_dir=/elsewhere", "PATH=/bin"}
	if runtime.GOOS == "windows" {
		want = []string{"PATH=/bin"}
	}
	if !slices.Equal(kept, want) {
		t.Errorf("kept %q, want %q", kept, want)
	}
}

// A .git git cannot read fails, rather than leading git up to the repository
// the directory is inside.
func TestRunNeverClimbsOutOfARepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	gitIn(t, root, "init", "--quiet", root)
	broken := filepath.Join(root, "broken")
	if err := os.MkdirAll(filepath.Join(broken, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := (Runner{}).Run(context.Background(), broken, "rev-parse", "--show-toplevel"); err == nil {
		t.Errorf("git ran in %q", out)
	}
}

func TestCommonDirIsSharedByWorktrees(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	main, linked := filepath.Join(root, "main"), filepath.Join(root, "linked")
	gitIn(t, root, "init", "--quiet", main)
	gitIn(t, main, "-c", "user.name=orbit", "-c", "user.email=orbit@example.com", "commit", "--quiet", "--allow-empty", "-m", "first")
	gitIn(t, main, "worktree", "add", "--quiet", "-b", "linked", linked)

	if got, want := CommonDir(main), filepath.Join(main, ".git"); got != want {
		t.Errorf("main: %s, want %s", got, want)
	}
	if got, want := CommonDir(linked), filepath.Join(main, ".git"); got != want {
		t.Errorf("linked: %s, want %s", got, want)
	}
}

// A remote wanting a password fails at once, rather than opening an askpass
// window the batch would wait on.
func TestRunNeverAsksForAPassword(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil || runtime.GOOS == "windows" {
		t.Skip("needs git and a shell")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="orbit"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	dir := t.TempDir()
	marker := filepath.Join(dir, "asked")
	askpass := filepath.Join(dir, "askpass")
	if err := os.WriteFile(askpass, []byte("#!/bin/sh\ntouch "+marker+"\necho secret\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_ASKPASS", askpass)
	t.Setenv("SSH_ASKPASS", askpass)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	if _, err := (Runner{}).Run(context.Background(), dir, "ls-remote", server.URL+"/repo.git"); err == nil {
		t.Error("ls-remote of a remote wanting a password succeeded")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("an askpass program was asked for the password")
	}
}

func TestMessageSaysWhyGitFailed(t *testing.T) {
	for stderr, want := range map[string]string{
		"error: The following untracked working tree files would be overwritten by merge:\n\t.env\n\tconfig.yaml\nPlease move or remove them before you merge.\nAborting": "The following untracked working tree files would be overwritten by merge: .env, config.yaml",
		"ERROR: Repository not found.\nfatal: Could not read from remote repository.\n\nPlease make sure you have the correct access rights":                              "Could not read from remote repository.",
		"hint: something\nwarning: odd\n": "warning: odd",
	} {
		if got := (&Error{Stderr: stderr}).Message(); got != want {
			t.Errorf("Message() = %q, want %q", got, want)
		}
	}
}

// A path's trailing space is part of it: a worktree whose git directory ends in
// one is grouped with the right repository.
func TestCommonDirKeepsTrailingSpaces(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	common := filepath.Join(root, "shared.git ")
	gitDir := filepath.Join(common, "worktrees", "wt ")
	worktree := filepath.Join(root, "wt")
	for _, dir := range []string{gitDir, worktree, filepath.Join(root, "shared.git")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := CommonDir(worktree); got != common {
		t.Errorf("CommonDir = %q, want %q", got, common)
	}
}

func TestRunReportsWhatGitSaid(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	_, err := Runner{}.Run(context.Background(), t.TempDir(), "rev-parse", "--verify", "refs/heads/nothing")
	var failure *Error
	if !errors.As(err, &failure) {
		t.Fatalf("err = %v", err)
	}
	if failure.Command != "rev-parse" || failure.ExitCode == 0 || !strings.HasPrefix(failure.Error(), "git rev-parse: ") {
		t.Errorf("failure = %+v: %s", failure, failure.Error())
	}
}
