package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/andrerfcsantos/orbit/internal/core/git"
	platformgit "github.com/andrerfcsantos/orbit/internal/platform/git"
)

// isolate keeps the tests' git from the developer's configuration, whose
// signing, hooks or default branch would change what they see.
func isolate(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	home := t.TempDir()
	config := "[user]\n\tname = orbit\n\temail = orbit@example.com\n[init]\n\tdefaultBranch = main\n" +
		"[commit]\n\tgpgsign = false\n[advice]\n\tdetachedHead = false\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return t.TempDir()
}

func run(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, file, content string) {
	t.Helper()
	path := filepath.Join(dir, file)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, dir, file string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// commit commits file with content, at date when it is not zero.
func commit(t *testing.T, dir, file, content string, date time.Time) {
	t.Helper()
	write(t, dir, file, content)
	run(t, dir, nil, "add", file)
	var env []string
	if !date.IsZero() {
		stamp := date.Format(time.RFC3339)
		env = []string{"GIT_AUTHOR_DATE=" + stamp, "GIT_COMMITTER_DATE=" + stamp}
	}
	run(t, dir, env, "commit", "--quiet", "-m", "change "+file)
}

// remote makes a bare remote holding a.txt and b.txt on main, and a clone that
// pushes to it as somebody else would.
func remote(t *testing.T, root string) (bare, upstream string) {
	t.Helper()
	bare = filepath.Join(root, "remote.git")
	run(t, root, nil, "init", "--quiet", "--bare", bare)
	upstream = filepath.Join(root, "upstream")
	run(t, root, nil, "clone", "--quiet", bare, upstream)
	commit(t, upstream, "a.txt", "a\n", time.Time{})
	commit(t, upstream, "b.txt", "b\n", time.Time{})
	run(t, upstream, nil, "push", "--quiet", "origin", "main")
	return bare, upstream
}

func clone(t *testing.T, bare, dir string) string {
	t.Helper()
	run(t, filepath.Dir(dir), nil, "clone", "--quiet", bare, dir)
	return dir
}

func service(root string) *Service {
	return &Service{Git: platformgit.Runner{}, WorkDir: root}
}

func repository(dir string) Repository { return Repository{Path: dir, Name: filepath.Base(dir)} }

func only[T any](t *testing.T, results []Result[T]) Result[T] {
	t.Helper()
	if len(results) != 1 {
		t.Fatalf("%d results", len(results))
	}
	return results[0]
}

func TestUpdateFastForwardsAndPutsChangesBack(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	commit(t, upstream, "b.txt", "b2\n", time.Time{})
	run(t, upstream, nil, "push", "--quiet")
	write(t, local, "a.txt", "mine\n")
	write(t, local, "new.txt", "untracked\n")

	result := only(t, service(root).Update(context.Background(), []Repository{repository(local)}, UpdateOptions{}, nil))
	if result.Outcome != OutcomeChanged || result.Detail.Pulled != 1 || !result.Detail.Restored || result.Detail.Stashed != "" {
		t.Fatalf("result = %+v", result)
	}
	if read(t, local, "b.txt") != "b2\n" || read(t, local, "a.txt") != "mine\n" || read(t, local, "new.txt") != "untracked\n" {
		t.Error("the pull or the changes are missing")
	}
	if stashes := run(t, local, nil, "stash", "list"); stashes != "" {
		t.Errorf("stashes left: %s", stashes)
	}

	again := only(t, service(root).Update(context.Background(), []Repository{repository(local)}, UpdateOptions{}, nil))
	if again.Outcome != OutcomeUnchanged {
		t.Errorf("again = %+v", again)
	}
}

func TestUpdateLeavesChangesToPulledFilesInTheStash(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	commit(t, upstream, "b.txt", "theirs\n", time.Time{})
	run(t, upstream, nil, "push", "--quiet")
	write(t, local, "b.txt", "mine\n")

	result := only(t, service(root).Update(context.Background(), []Repository{repository(local)}, UpdateOptions{}, nil))
	if result.Outcome != OutcomeChanged || result.Detail.Pulled != 1 || result.Detail.Restored || result.Detail.Stashed == "" || len(result.Notes) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if read(t, local, "b.txt") != "theirs\n" {
		t.Error("the pull is missing")
	}
	if stashes := run(t, local, nil, "stash", "list"); !strings.Contains(stashes, result.Detail.Stashed) {
		t.Errorf("stashes: %s", stashes)
	}
}

func TestUpdateSwitchesToTheDefaultBranch(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	run(t, local, nil, "switch", "--quiet", "-c", "feature")
	commit(t, local, "feature.txt", "f\n", time.Time{})
	write(t, local, "a.txt", "work in progress\n")
	commit(t, upstream, "b.txt", "b2\n", time.Time{})
	run(t, upstream, nil, "push", "--quiet")
	// A clone made before origin/HEAD was recorded: the remote is asked.
	run(t, local, nil, "remote", "set-head", "origin", "--delete")

	result := only(t, service(root).Update(context.Background(), []Repository{repository(local)}, UpdateOptions{Default: true}, nil))
	got := result.Detail
	if result.Outcome != OutcomeChanged || !got.Switched || got.From != "feature" || got.Branch != "main" || got.Pulled != 1 {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(got.Stashed, "on feature") {
		t.Errorf("stashed = %q", got.Stashed)
	}
	if branch := run(t, local, nil, "branch", "--show-current"); branch != "main" {
		t.Errorf("on %s", branch)
	}
	if stashes := run(t, local, nil, "stash", "list"); !strings.Contains(stashes, "On feature: ") {
		t.Errorf("the stash is not on feature: %s", stashes)
	}
}

func TestUpdateLeavesWhatItCannotFastForward(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	diverged := clone(t, bare, filepath.Join(root, "diverged"))
	commit(t, diverged, "mine.txt", "m\n", time.Time{})
	detached := clone(t, bare, filepath.Join(root, "detached"))
	run(t, detached, nil, "switch", "--quiet", "--detach", "HEAD~1")
	commit(t, upstream, "b.txt", "b2\n", time.Time{})
	run(t, upstream, nil, "push", "--quiet")

	results := service(root).Update(context.Background(), []Repository{repository(diverged), repository(detached)}, UpdateOptions{}, nil)
	for _, result := range results {
		if result.Outcome != OutcomeSkipped || result.Reason == "" {
			t.Errorf("%s: %+v", result.Repository.Name, result)
		}
	}
	if !strings.Contains(results[0].Reason, "diverged") || results[0].Detail.Ahead != 1 || results[0].Detail.Behind != 1 {
		t.Errorf("diverged = %+v", results[0])
	}
}

func TestDiscoverNamesWhatItFinds(t *testing.T) {
	root := isolate(t)
	for _, dir := range []string{"team/one", "team/deep/two", "three"} {
		run(t, root, nil, "init", "--quiet", filepath.Join(root, dir))
	}
	commit(t, filepath.Join(root, "three"), "sub/file.txt", "x", time.Time{})
	s := service(root)

	repos, err := s.Discover(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, repo := range repos {
		names = append(names, repo.Name)
	}
	if want := []string{"team/deep/two", "team/one", "three"}; !slices.Equal(names, want) {
		t.Errorf("names = %q, want %q", names, want)
	}

	inside, err := s.Discover(context.Background(), []string{"three/sub", "three"})
	if err != nil || len(inside) != 1 || inside[0].Name != "three" {
		t.Errorf("inside = %+v, %v", inside, err)
	}
	if _, err := s.Discover(context.Background(), []string{"missing"}); err == nil || strings.Contains(err.Error(), "missing") {
		t.Errorf("err = %v", err)
	}
}

// A directory added but not yet committed, in a repository with no commit at
// all, is the repository's as much as one committed.
func TestDiscoverFindsTheRepositoryOfAStagedDirectory(t *testing.T) {
	root := isolate(t)
	repo := filepath.Join(root, "fresh")
	run(t, root, nil, "init", "--quiet", repo)
	write(t, repo, "new-dir/file.txt", "x")
	run(t, repo, nil, "add", "new-dir/file.txt")
	repos, err := service(root).Discover(context.Background(), []string{filepath.Join(repo, "new-dir")})
	if err != nil || len(repos) != 1 || repos[0].Name != "fresh" {
		t.Errorf("repos = %+v, %v", repos, err)
	}
}

// A home directory kept as a dotfiles repository holds ~/code without tracking
// it: an empty ~/code is not the home directory's repository, which cleaning
// would wipe.
func TestDiscoverDoesNotClimbOutOfAnUntrackedDirectory(t *testing.T) {
	root := isolate(t)
	home := filepath.Join(root, "home")
	run(t, root, nil, "init", "--quiet", home)
	commit(t, home, ".gitignore", "*\n!.gitignore\n", time.Time{})
	if err := os.MkdirAll(filepath.Join(home, "code", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	repos, err := service(root).Discover(context.Background(), []string{filepath.Join(home, "code")})
	if err != nil || len(repos) != 0 {
		t.Errorf("repos = %+v, %v", repos, err)
	}
}

func TestListSaysWhereEachRepositoryStands(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	commit(t, local, "c.txt", "c\n", time.Time{})
	write(t, local, "a.txt", "changed\n")
	write(t, local, "d.txt", "stashed\n")
	run(t, local, nil, "stash", "push", "--include-untracked", "--quiet")
	write(t, local, "a.txt", "changed again\n")

	result := only(t, service(root).List(context.Background(), []Repository{repository(local)}, nil))
	want := Status{Branch: "main", Default: "main", Commit: run(t, local, nil, "rev-parse", "HEAD"), Upstream: "origin/main", Ahead: 1, Changes: 1, Stashes: 1}
	if result.Outcome != OutcomeUnchanged || result.Detail != want {
		t.Errorf("result = %+v, want %+v", result.Detail, want)
	}
}

func TestBranchesOlderThanAreDeletedLocallyAndOnTheRemote(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	old := time.Now().AddDate(-1, 0, 0)
	for _, branch := range []string{"old-pushed", "old-moved", "fresh"} {
		run(t, upstream, nil, "switch", "--quiet", "-c", branch, "main")
		date := old
		if branch == "fresh" {
			date = time.Time{}
		}
		commit(t, upstream, branch+".txt", branch, date)
		run(t, upstream, nil, "push", "--quiet", "origin", branch)
	}
	local := clone(t, bare, filepath.Join(root, "local"))
	for _, branch := range []string{"old-pushed", "old-moved", "fresh"} {
		run(t, local, nil, "branch", "--quiet", "--track", branch, "origin/"+branch)
	}
	run(t, local, nil, "switch", "--quiet", "-c", "old-local", "main")
	commit(t, local, "old-local.txt", "x", old)
	// The branch checked out is kept, however old.
	run(t, local, nil, "switch", "--quiet", "-c", "old-current", "main")
	commit(t, local, "old-current.txt", "x", old)

	age, err := core.ParseAge("6mo")
	if err != nil {
		t.Fatal(err)
	}
	s := service(root)
	plan := only(t, s.StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age, Remote: true}, nil))
	names := func(branches []Branch) []string {
		var names []string
		for _, branch := range branches {
			names = append(names, branch.Name)
		}
		slices.Sort(names)
		return names
	}
	if got, want := names(plan.Detail.Local), []string{"old-local", "old-moved", "old-pushed"}; !slices.Equal(got, want) {
		t.Fatalf("local = %q, want %q", got, want)
	}
	if got, want := names(plan.Detail.Remote), []string{"old-moved", "old-pushed"}; !slices.Equal(got, want) {
		t.Fatalf("remote = %q, want %q", got, want)
	}

	// Somebody commits to old-moved after it was found, locally and on the remote.
	run(t, local, nil, "switch", "--quiet", "old-moved")
	commit(t, local, "moved.txt", "new", time.Time{})
	run(t, local, nil, "push", "--quiet", "origin", "old-moved")
	run(t, local, nil, "switch", "--quiet", "old-current")

	deleted := only(t, s.DeleteBranches(context.Background(), []Result[Branches]{plan}, nil))
	if deleted.Outcome != OutcomeFailed || !strings.Contains(deleted.Error, "2 of 5") {
		t.Errorf("deleted = %+v", deleted)
	}
	for _, branch := range slices.Concat(deleted.Detail.Local, deleted.Detail.Remote) {
		if branch.Deleted == (branch.Name == "old-moved") {
			t.Errorf("%s/%s: deleted %v, %s", branch.Remote, branch.Name, branch.Deleted, branch.Error)
		}
	}
	if left := run(t, local, nil, "branch", "--format=%(refname:short)"); left != "fresh\nmain\nold-current\nold-moved" {
		t.Errorf("local branches left: %q", left)
	}
	if left := run(t, bare, nil, "branch", "--format=%(refname:short)"); left != "fresh\nmain\nold-moved" {
		t.Errorf("remote branches left: %q", left)
	}
}

func TestCleanRemovesUntrackedAndIgnoredFiles(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	commit(t, local, ".gitignore", "build/\n", time.Time{})
	write(t, local, "build/out.bin", "x")
	write(t, local, "scratch.txt", "x")
	s := service(root)

	plan := only(t, s.Uncleaned(context.Background(), []Repository{repository(local)}, nil))
	if got := plan.Detail.Paths; !slices.Equal(got, []string{"build/", "scratch.txt"}) {
		t.Fatalf("plan = %q", got)
	}
	if _, err := os.Stat(filepath.Join(local, "scratch.txt")); err != nil {
		t.Fatal("planning removed a file")
	}

	cleaned := only(t, s.Clean(context.Background(), []Repository{repository(local)}, nil))
	if cleaned.Outcome != OutcomeChanged || len(cleaned.Detail.Paths) != 2 || cleaned.Detail.Collected.BytesBefore == 0 {
		t.Fatalf("cleaned = %+v", cleaned)
	}
	for _, path := range []string{"build", "scratch.txt"} {
		if _, err := os.Stat(filepath.Join(local, path)); !os.IsNotExist(err) {
			t.Errorf("%s is still there", path)
		}
	}

	collected := only(t, s.GC(context.Background(), []Repository{repository(local)}, GCOptions{}, nil))
	if collected.Outcome == OutcomeFailed {
		t.Errorf("gc = %+v", collected)
	}
}

// A directory inside a repository is cleaned alone, as git clean run there
// would clean it.
func TestCleaningADirectoryOfARepositoryCleansItAlone(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	commit(t, local, "sub/tracked.txt", "x", time.Time{})
	write(t, local, "sub/scratch.txt", "x")
	write(t, local, ".env", "SECRET=1")
	s := service(root)

	repos, err := s.Discover(context.Background(), []string{filepath.Join(local, "sub")})
	if err != nil || len(repos) != 1 || !slices.Equal(repos[0].Within, []string{"sub"}) {
		t.Fatalf("repos = %+v, %v", repos, err)
	}
	cleaned := only(t, s.Clean(context.Background(), repos, nil))
	if !slices.Equal(cleaned.Detail.Paths, []string{"sub/scratch.txt"}) {
		t.Errorf("removed %q", cleaned.Detail.Paths)
	}
	if read(t, local, ".env") != "SECRET=1" {
		t.Error("the repository's top was cleaned")
	}

	// Named whole as well, it is cleaned whole.
	both, err := s.Discover(context.Background(), []string{filepath.Join(local, "sub"), local})
	if err != nil || len(both) != 1 || both[0].Within != nil {
		t.Errorf("both = %+v, %v", both, err)
	}
}

// Every directory named is cleaned, each literally: a directory called cache*
// is not a pattern naming cache-prod too.
func TestCleaningSeveralDirectoriesCleansEachLiterally(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	commit(t, local, "a/tracked.txt", "x", time.Time{})
	commit(t, local, "cache*/tracked.txt", "x", time.Time{})
	commit(t, local, "cache-prod/tracked.txt", "x", time.Time{})
	for _, path := range []string{"a/u", "cache*/u", "cache-prod/u"} {
		write(t, local, path, "x")
	}
	s := service(root)
	repos, err := s.Discover(context.Background(), []string{filepath.Join(local, "a"), filepath.Join(local, "cache*")})
	if err != nil || len(repos) != 1 {
		t.Fatalf("repos = %+v, %v", repos, err)
	}
	cleaned := only(t, s.Clean(context.Background(), repos, nil))
	if want := []string{"a/u", "cache*/u"}; !slices.Equal(cleaned.Detail.Paths, want) {
		t.Errorf("removed %q, want %q", cleaned.Detail.Paths, want)
	}
	if _, err := os.Stat(filepath.Join(local, "cache-prod", "u")); err != nil {
		t.Error("cache-prod was cleaned")
	}
}

// What is cleaned is what was listed: a file that appears while the person
// reads the list is not removed unseen.
func TestCleaningAPlanRemovesWhatItListed(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	listed := clone(t, bare, filepath.Join(root, "listed"))
	empty := clone(t, bare, filepath.Join(root, "empty"))
	write(t, listed, "scratch.txt", "x")
	s := service(root)
	plans := s.Uncleaned(context.Background(), []Repository{repository(listed), repository(empty)}, nil)

	write(t, listed, "later.txt", "x")
	write(t, empty, ".env", "SECRET=1")
	results := s.CleanPlanned(context.Background(), plans, nil)
	if len(results) != 2 || !slices.Equal(results[0].Detail.Paths, []string{"scratch.txt"}) || len(results[1].Detail.Paths) != 0 {
		t.Fatalf("results = %+v", results)
	}
	if read(t, listed, "later.txt") != "x" || read(t, empty, ".env") != "SECRET=1" {
		t.Error("a file that was not listed was removed")
	}

	// A repository whose listing failed is not cleaned at all, and says so.
	write(t, listed, "kept.txt", "x")
	failed := []Result[Cleaned]{{Repository: repository(listed), Outcome: OutcomeFailed, Error: "listing failed"}}
	results = s.CleanPlanned(context.Background(), failed, nil)
	if len(results) != 1 || results[0].Outcome != OutcomeFailed || results[0].Error != "listing failed" || read(t, listed, "kept.txt") != "x" {
		t.Errorf("cleaned %+v", results)
	}
}

// An ignored file the stash never held is not overwritten by a commit pulled,
// or a branch switched to, that starts tracking it.
func TestUpdateNeverOverwritesAnIgnoredFile(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	commit(t, upstream, ".env", "theirs\n", time.Time{})
	run(t, upstream, nil, "push", "--quiet")
	write(t, local, ".git/info/exclude", ".env\n")
	write(t, local, ".env", "mine\n")

	result := only(t, service(root).Update(context.Background(), []Repository{repository(local)}, UpdateOptions{}, nil))
	if result.Outcome != OutcomeFailed || !strings.Contains(result.Error, ".env") {
		t.Errorf("result = %+v", result)
	}
	if read(t, local, ".env") != "mine\n" {
		t.Error("the ignored file was overwritten")
	}

	run(t, local, nil, "switch", "--quiet", "-c", "feature", "HEAD")
	result = only(t, service(root).Update(context.Background(), []Repository{repository(local)}, UpdateOptions{Default: true}, nil))
	if result.Outcome != OutcomeFailed || read(t, local, ".env") != "mine\n" {
		t.Errorf("switching: %+v, .env is %q", result, read(t, local, ".env"))
	}
}

// A branch committed to locally after it was listed, but not pushed, keeps its
// remote branch too: that is where its pull request is.
func TestARemoteBranchStaysWhileItsLocalBranchMovedOn(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	old := time.Now().AddDate(-1, 0, 0)
	run(t, upstream, nil, "switch", "--quiet", "-c", "feature")
	commit(t, upstream, "feature.txt", "old", old)
	run(t, upstream, nil, "push", "--quiet", "origin", "feature")
	local := clone(t, bare, filepath.Join(root, "local"))
	run(t, local, nil, "branch", "--quiet", "--track", "feature", "origin/feature")
	age, _ := core.ParseAge("6mo")
	s := service(root)
	plan := only(t, s.StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age, Remote: true}, nil))
	if len(plan.Detail.Local) != 1 || len(plan.Detail.Remote) != 1 {
		t.Fatalf("plan = %+v", plan.Detail)
	}

	run(t, local, nil, "switch", "--quiet", "feature")
	commit(t, local, "feature.txt", "new", time.Time{})
	run(t, local, nil, "switch", "--quiet", "main")
	s.DeleteBranches(context.Background(), []Result[Branches]{plan}, nil)
	if left := run(t, bare, nil, "branch", "--format=%(refname:short)"); left != "feature\nmain" {
		t.Errorf("remote branches left: %q", left)
	}
}

// An alias of a branch is no branch of its own: deleting it through git deletes
// the branch it names.
func TestAnAliasIsNeverDeleted(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	run(t, local, nil, "switch", "--quiet", "-c", "work")
	commit(t, local, "work.txt", "x", time.Now().AddDate(-1, 0, 0))
	run(t, local, nil, "switch", "--quiet", "main")
	run(t, local, nil, "symbolic-ref", "refs/heads/old-alias", "refs/heads/work")
	age, _ := core.ParseAge("6mo")
	s := service(root)
	// Neither the alias nor, since the alias names it, work.
	plan := only(t, s.StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age}, nil))
	if len(plan.Detail.Local) != 0 {
		t.Fatalf("plan = %+v", plan.Detail)
	}

	// Planned by hand, as an older plan might have, it is deleted alone.
	plan.Outcome = OutcomeChanged
	plan.Detail.Local = []Branch{{Name: "old-alias", Commit: run(t, local, nil, "rev-parse", "refs/heads/work")}}
	s.DeleteBranches(context.Background(), []Result[Branches]{plan}, nil)
	if left := run(t, local, nil, "branch", "--format=%(refname:short)"); left != "main\nwork" {
		t.Errorf("branches left: %q", left)
	}
}

// A branch another is an alias of is kept: with main an alias of an old
// release, deleting release would leave main naming nothing.
func TestABranchAnAliasNamesIsKept(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	run(t, local, nil, "switch", "--quiet", "-c", "release")
	commit(t, local, "release.txt", "x", time.Now().AddDate(-1, 0, 0))
	run(t, local, nil, "switch", "--quiet", "-c", "feature", "origin/main")
	run(t, local, nil, "symbolic-ref", "refs/heads/main", "refs/heads/release")
	age, _ := core.ParseAge("6mo")
	plan := only(t, service(root).StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age}, nil))
	if len(plan.Detail.Local) != 0 {
		t.Errorf("planned %+v", plan.Detail.Local)
	}
}

// halfStashing runs git, but has git stash push save the stash and leave a
// change behind, failing, as git does when an untracked file cannot be removed
// -- which no permission stops root from doing, so it is played here.
type halfStashing struct {
	Git
	t   *testing.T
	dir string
}

func (h *halfStashing) Run(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := h.Git.Run(ctx, dir, args...)
	if err == nil && len(args) >= 2 && args[0] == "stash" && args[1] == "push" {
		write(h.t, h.dir, "left.txt", "could not be removed\n")
		return out, errors.New("git stash: failed to remove left.txt")
	}
	return out, err
}

// A fetch run behind orbit's back, as an editor does, between reading the
// status and resolving the upstream is pulled: the counts are of what is pulled.
func TestAFetchAfterTheStatusIsStillPulled(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	commit(t, upstream, "b.txt", "b2\n", time.Time{})
	run(t, upstream, nil, "push", "--quiet")
	s := service(root)
	s.Git = &fetchingLate{Git: s.Git}

	result := only(t, s.Update(context.Background(), []Repository{repository(local)}, UpdateOptions{}, nil))
	if result.Outcome != OutcomeChanged || result.Detail.Pulled != 1 {
		t.Fatalf("result = %+v", result)
	}
	if read(t, local, "b.txt") != "b2\n" {
		t.Error("the commit fetched was not pulled")
	}
}

// fetchingLate runs git, but fetches only once the status has been read.
type fetchingLate struct {
	Git
	fetched bool
}

func (f *fetchingLate) Run(ctx context.Context, dir string, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "fetch" {
		return "", nil
	}
	out, err := f.Git.Run(ctx, dir, args...)
	if err == nil && len(args) > 0 && args[0] == "status" && !f.fetched {
		f.fetched = true
		if _, err := f.Git.Run(ctx, dir, "fetch", "--quiet"); err != nil {
			return out, err
		}
	}
	return out, err
}

// An alias made while the person reads the plan keeps the branch it names too.
func TestAnAliasMadeAfterPlanningKeepsItsBranch(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	run(t, local, nil, "switch", "--quiet", "-c", "release")
	commit(t, local, "release.txt", "x", time.Now().AddDate(-1, 0, 0))
	run(t, local, nil, "switch", "--quiet", "-c", "feature", "origin/main")
	age, _ := core.ParseAge("6mo")
	s := service(root)
	plan := only(t, s.StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age}, nil))
	if len(plan.Detail.Local) != 1 || plan.Detail.Local[0].Name != "release" {
		t.Fatalf("plan = %+v", plan.Detail.Local)
	}

	run(t, local, nil, "symbolic-ref", "refs/heads/main", "refs/heads/release")
	deleted := only(t, s.DeleteBranches(context.Background(), []Result[Branches]{plan}, nil))
	if deleted.Detail.Local[0].Deleted {
		t.Errorf("deleted = %+v", deleted)
	}
	run(t, local, nil, "rev-parse", "--verify", "--quiet", "refs/heads/release")
}

// git can save a stash and fail to put the changes away, as when an untracked
// file cannot be removed: the update stops there, saying where the changes are.
func TestAStashHalfMadeStopsTheUpdate(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	commit(t, upstream, "b.txt", "b2\n", time.Time{})
	run(t, upstream, nil, "push", "--quiet")
	write(t, local, "a.txt", "mine\n")
	s := service(root)
	s.Git = &halfStashing{Git: s.Git, t: t, dir: local}

	result := only(t, s.Update(context.Background(), []Repository{repository(local)}, UpdateOptions{}, nil))
	if result.Outcome != OutcomeFailed || result.Detail.Pulled != 0 || result.Detail.Stashed == "" || len(result.Notes) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if read(t, local, "b.txt") != "b\n" {
		t.Error("the update went on and pulled")
	}
}

// An annotated tag a refspec fetches in among the branches has no commit date:
// it is no branch, and the repository's branches are planned as ever.
func TestAnAnnotatedTagAmongTheBranchesIsLeftOut(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	run(t, upstream, nil, "tag", "-a", "-m", "release", "v1")
	run(t, upstream, nil, "push", "--quiet", "origin", "v1")
	local := clone(t, bare, filepath.Join(root, "local"))
	run(t, local, nil, "config", "--add", "remote.origin.fetch", "+refs/tags/*:refs/remotes/origin/tags/*")
	run(t, local, nil, "fetch", "--quiet")
	run(t, local, nil, "switch", "--quiet", "-c", "old")
	commit(t, local, "old.txt", "x", time.Now().AddDate(-1, 0, 0))
	run(t, local, nil, "switch", "--quiet", "main")
	age, _ := core.ParseAge("6mo")
	plan := only(t, service(root).StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age, Remote: true}, nil))
	if plan.Outcome != OutcomeChanged || len(plan.Detail.Local) != 1 || plan.Detail.Local[0].Name != "old" {
		t.Errorf("plan = %+v", plan)
	}
}

// A refspec whose wildcard is within a path component -- archive-* -- is read
// from its directory, and its branches told apart there.
func TestARefspecWildcardWithinANameIsRead(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	run(t, upstream, nil, "switch", "--quiet", "-c", "old")
	commit(t, upstream, "old.txt", "x", time.Now().AddDate(-1, 0, 0))
	run(t, upstream, nil, "push", "--quiet", "origin", "old")
	local := clone(t, bare, filepath.Join(root, "local"))
	run(t, local, nil, "config", "--replace-all", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/archive-*")
	run(t, local, nil, "fetch", "--quiet", "--prune")
	age, _ := core.ParseAge("6mo")
	plan := only(t, service(root).StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age, Remote: true}, nil))
	if len(plan.Detail.Remote) != 1 || plan.Detail.Remote[0].Name != "old" || plan.Detail.Remote[0].Tracking != "refs/remotes/origin/archive-old" {
		t.Errorf("plan = %+v", plan.Detail.Remote)
	}
}

// stashing runs git, and stashes in another worktree, which shares the stash,
// once: just before the update's git stash <before>, or just after its <after>.
type stashing struct {
	Git
	other         string
	before, after string
	once          sync.Once
	t             *testing.T
}

func (s *stashing) Run(ctx context.Context, dir string, args ...string) (string, error) {
	other := func() { run(s.t, s.other, nil, "stash", "push", "--quiet", "--message", "the other worktree's") }
	isStash := func(sub string) bool { return sub != "" && len(args) >= 2 && args[0] == "stash" && args[1] == sub }
	if isStash(s.before) {
		s.once.Do(other)
	}
	out, err := s.Git.Run(ctx, dir, args...)
	if isStash(s.after) {
		s.once.Do(other)
	}
	return out, err
}

// The stash is shared by worktrees, so another git can stash just after the
// update stashes, or between its finding its stash and dropping it: the update
// puts back its own changes, and no stash is lost.
func TestUpdateNeverTakesAnotherStash(t *testing.T) {
	for _, tc := range []struct{ name, before, after string }{
		{"after stashing", "", "push"},
		{"before dropping", "drop", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := isolate(t)
			bare, upstream := remote(t, root)
			local := clone(t, bare, filepath.Join(root, "local"))
			other := filepath.Join(root, "other")
			run(t, local, nil, "worktree", "add", "--quiet", "-b", "other", other)
			write(t, other, "a.txt", "the other worktree's\n")
			commit(t, upstream, "b.txt", "b2\n", time.Time{})
			run(t, upstream, nil, "push", "--quiet")
			write(t, local, "a.txt", "mine\n")

			s := service(root)
			s.Git = &stashing{Git: s.Git, other: other, before: tc.before, after: tc.after, t: t}
			result := only(t, s.Update(context.Background(), []Repository{repository(local)}, UpdateOptions{}, nil))
			if !result.Detail.Restored || read(t, local, "a.txt") != "mine\n" {
				t.Fatalf("result = %+v, a.txt is %q", result, read(t, local, "a.txt"))
			}
			if stashes := run(t, local, nil, "stash", "list"); stashes != "stash@{0}: On other: the other worktree's" {
				t.Errorf("stashes: %q", stashes)
			}
		})
	}
}

// stopping runs git, and stops the batch the moment git stash push is done.
type stopping struct {
	Git
	stop context.CancelFunc
}

func (s *stopping) Run(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := s.Git.Run(ctx, dir, args...)
	if len(args) >= 2 && args[0] == "stash" && args[1] == "push" {
		s.stop()
	}
	return out, err
}

// Stopped the moment its changes are stashed, an update puts them back, whether
// they were to be put back after the pull or to stay in the stash.
func TestAnUpdateStoppedAfterStashingPutsTheChangesBack(t *testing.T) {
	for _, file := range []string{"a.txt", "b.txt"} {
		t.Run(file, func(t *testing.T) {
			root := isolate(t)
			bare, upstream := remote(t, root)
			local := clone(t, bare, filepath.Join(root, "local"))
			commit(t, upstream, "b.txt", "b2\n", time.Time{})
			run(t, upstream, nil, "push", "--quiet")
			write(t, local, file, "mine\n")

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := service(root)
			s.Git = &stopping{Git: s.Git, stop: cancel}
			result := only(t, s.Update(ctx, []Repository{repository(local)}, UpdateOptions{}, nil))
			if result.Outcome != OutcomeCancelled || read(t, local, file) != "mine\n" {
				t.Errorf("result = %+v, %s is %q", result, file, read(t, local, file))
			}
			if stashes := run(t, local, nil, "stash", "list"); stashes != "" {
				t.Errorf("stashes left: %q", stashes)
			}
		})
	}
}

// A tag called origin/main is not the branch origin/main: the default branch
// with no upstream is brought to the remote's branch, not to the tag.
func TestUpdatePullsTheBranchNotATagOfItsName(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	run(t, local, nil, "branch", "--quiet", "--unset-upstream")
	commit(t, upstream, "b.txt", "b2\n", time.Time{})
	run(t, upstream, nil, "push", "--quiet")
	run(t, local, nil, "fetch", "--quiet")
	run(t, local, nil, "switch", "--quiet", "-c", "feature")
	commit(t, local, "feature.txt", "f\n", time.Time{})
	run(t, local, nil, "tag", "origin/main")
	run(t, local, nil, "switch", "--quiet", "main")

	result := only(t, service(root).Update(context.Background(), []Repository{repository(local)}, UpdateOptions{Default: true}, nil))
	if result.Outcome != OutcomeChanged || result.Detail.Pulled != 1 {
		t.Fatalf("result = %+v", result)
	}
	if head, want := run(t, local, nil, "rev-parse", "HEAD"), run(t, local, nil, "rev-parse", "refs/remotes/origin/main"); head != want {
		t.Errorf("HEAD is %s, origin's main %s", head, want)
	}
}

// A remote branch fetched into two places is one branch: a kept local branch
// tracking it through one keeps it through the other, and a remote that cannot
// say what its default branch is has none of its branches deleted.
func TestRemoteBranchesAreKnownByTheirNameOnTheRemote(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	old := time.Now().AddDate(-1, 0, 0)
	for _, branch := range []string{"work", "stray"} {
		run(t, upstream, nil, "switch", "--quiet", "-c", branch, "main")
		commit(t, upstream, branch+".txt", "x", old)
		run(t, upstream, nil, "push", "--quiet", "origin", branch)
	}
	local := clone(t, bare, filepath.Join(root, "local"))
	run(t, local, nil, "config", "--replace-all", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/live/*")
	run(t, local, nil, "config", "--add", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/archive/*")
	run(t, local, nil, "fetch", "--quiet", "--prune")
	run(t, local, nil, "branch", "--quiet", "--track", "work", "refs/remotes/origin/live/work")
	run(t, local, nil, "switch", "--quiet", "work")
	commit(t, local, "work.txt", "new", time.Time{})
	run(t, local, nil, "switch", "--quiet", "main")

	age, _ := core.ParseAge("6mo")
	s := service(root)
	plan := only(t, s.StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age, Remote: true}, nil))
	if len(plan.Detail.Remote) != 1 || plan.Detail.Remote[0].Name != "stray" {
		t.Fatalf("planned %+v", plan.Detail.Remote)
	}
	// stray is fetched into both places, and deleted once.
	if deleted := only(t, s.DeleteBranches(context.Background(), []Result[Branches]{plan}, nil)); deleted.Outcome != OutcomeChanged {
		t.Errorf("deleted = %+v", deleted)
	}
	if left := run(t, bare, nil, "branch", "--format=%(refname:short)"); left != "main\nwork" {
		t.Errorf("left on the remote: %q", left)
	}

	// A detached remote HEAD names no default branch.
	run(t, bare, nil, "update-ref", "--no-deref", "HEAD", run(t, bare, nil, "rev-parse", "main"))
	plan = only(t, s.StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age, Remote: true}, nil))
	if len(plan.Detail.Remote) != 0 || len(plan.Notes) != 1 {
		t.Errorf("plan = %+v, notes %q", plan.Detail.Remote, plan.Notes)
	}
}

// A branch being rebased, here or in another worktree, is in use, though its
// worktree's HEAD reads as detached.
func TestABranchBeingRebasedIsKept(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	old := time.Now().AddDate(-1, 0, 0)
	for _, branch := range []string{"here", "there"} {
		run(t, local, nil, "switch", "--quiet", "-c", branch, "main")
		commit(t, local, "a.txt", branch+"\n", old)
	}
	run(t, local, nil, "switch", "--quiet", "main")
	commit(t, local, "a.txt", "main\n", time.Time{})
	other := filepath.Join(root, "other")
	run(t, local, nil, "worktree", "add", "--quiet", "--detach", other)
	rebase := func(dir, branch string) {
		cmd := exec.Command("git", "rebase", "main", branch)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err == nil {
			t.Fatalf("the rebase of %s did not stop at its conflict:\n%s", branch, out)
		}
	}
	rebase(local, "here")
	rebase(other, "there")

	age, _ := core.ParseAge("6mo")
	s := service(root)
	plan := only(t, s.StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age}, nil))
	if len(plan.Detail.Local) != 0 {
		t.Errorf("planned %+v", plan.Detail.Local)
	}
	plan.Outcome = OutcomeChanged
	for _, branch := range []string{"here", "there"} {
		plan.Detail.Local = append(plan.Detail.Local, Branch{Name: branch, Commit: run(t, local, nil, "rev-parse", "refs/heads/"+branch)})
	}
	for _, branch := range only(t, s.DeleteBranches(context.Background(), []Result[Branches]{plan}, nil)).Detail.Local {
		if branch.Deleted {
			t.Errorf("%s, being rebased, was deleted", branch.Name)
		}
	}
}

// A rebase with --update-refs rewrites the branches stacked beneath the one it
// rebases, which are in use while it is paused.
func TestBranchesARebaseUpdatesAreKept(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	old := time.Now().AddDate(-1, 0, 0)
	run(t, local, nil, "switch", "--quiet", "-c", "bottom")
	commit(t, local, "bottom.txt", "x", old)
	run(t, local, nil, "switch", "--quiet", "-c", "top")
	commit(t, local, "a.txt", "top\n", old)
	run(t, local, nil, "switch", "--quiet", "main")
	commit(t, local, "a.txt", "main\n", time.Time{})
	cmd := exec.Command("git", "rebase", "--update-refs", "main", "top")
	cmd.Dir = local
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("the rebase did not stop at its conflict:\n%s", out)
	}

	age, _ := core.ParseAge("6mo")
	plan := only(t, service(root).StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age}, nil))
	if len(plan.Detail.Local) != 0 {
		t.Errorf("planned %+v", plan.Detail.Local)
	}
}

// A default branch that is neither main nor master, with origin/HEAD never
// recorded, is asked of the remote; one that cannot be asked leaves the
// repository alone rather than guess.
func TestAnUnknownDefaultBranchIsAskedForOrNothingIsDeleted(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	old := time.Now().AddDate(-1, 0, 0)
	run(t, upstream, nil, "switch", "--quiet", "-c", "develop")
	commit(t, upstream, "develop.txt", "x", old)
	run(t, upstream, nil, "push", "--quiet", "origin", "develop")
	run(t, bare, nil, "symbolic-ref", "HEAD", "refs/heads/develop")
	local := clone(t, bare, filepath.Join(root, "local"))
	run(t, local, nil, "switch", "--quiet", "-c", "feature", "origin/main")
	run(t, local, nil, "remote", "set-head", "origin", "--delete")
	age, _ := core.ParseAge("6mo")
	s := service(root)

	plan := only(t, s.StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age}, nil))
	if plan.Outcome != OutcomeUnchanged || len(plan.Detail.Local) != 0 {
		t.Errorf("asked: %+v", plan)
	}

	run(t, local, nil, "remote", "set-head", "origin", "--delete")
	run(t, local, nil, "remote", "set-url", "origin", filepath.Join(root, "gone.git"))
	plan = only(t, s.StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age}, nil))
	if plan.Outcome != OutcomeSkipped || len(plan.Detail.Local) != 0 {
		t.Errorf("unreachable: %+v", plan)
	}
}

// An alias on the remote is fetched as a branch of its own, but deleting it there
// deletes the branch it names: an alias of an old main is never deleted, over
// protocol v0 too, which tells of no alias but HEAD.
func TestARemoteAliasIsNeverDeleted(t *testing.T) {
	for _, protocol := range []string{"2", "0"} {
		t.Run("protocol v"+protocol, func(t *testing.T) {
			root := isolate(t)
			bare := filepath.Join(root, "remote.git")
			run(t, root, nil, "init", "--quiet", "--bare", bare)
			upstream := clone(t, bare, filepath.Join(root, "upstream"))
			commit(t, upstream, "a.txt", "a\n", time.Now().AddDate(-1, 0, 0))
			run(t, upstream, nil, "push", "--quiet", "origin", "main")
			run(t, bare, nil, "symbolic-ref", "refs/heads/old-alias", "refs/heads/main")
			local := clone(t, bare, filepath.Join(root, "local"))
			run(t, local, nil, "config", "protocol.version", protocol)
			run(t, local, nil, "switch", "--quiet", "-c", "feature")
			age, _ := core.ParseAge("6mo")
			s := service(root)

			plan := only(t, s.StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age, Remote: true}, nil))
			if len(plan.Detail.Remote) != 0 {
				t.Errorf("planned %+v", plan.Detail.Remote)
			}
			// Planned by hand, as a plan made before the alias was would have it.
			plan.Outcome = OutcomeChanged
			plan.Detail.Remote = []Branch{{Name: "old-alias", Remote: "origin", Commit: run(t, local, nil, "rev-parse", "origin/main")}}
			s.DeleteBranches(context.Background(), []Result[Branches]{plan}, nil)
			if left := run(t, bare, nil, "for-each-ref", "--format=%(refname)"); left != "refs/heads/main\nrefs/heads/old-alias" {
				t.Errorf("left on the remote: %q", left)
			}
		})
	}
}

// An alias can name a tag too, and deleting it would delete the tag: over
// protocol v0, which names no alias, the tag it shares its object with keeps it.
func TestARemoteAliasOfATagIsNeverDeleted(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	run(t, upstream, nil, "switch", "--quiet", "-c", "saved")
	commit(t, upstream, "saved.txt", "x", time.Now().AddDate(-1, 0, 0))
	run(t, upstream, nil, "tag", "saved-tag")
	run(t, upstream, nil, "push", "--quiet", "origin", "refs/tags/saved-tag")
	run(t, bare, nil, "symbolic-ref", "refs/heads/old-alias", "refs/tags/saved-tag")
	local := clone(t, bare, filepath.Join(root, "local"))
	run(t, local, nil, "config", "protocol.version", "0")
	age, _ := core.ParseAge("6mo")
	s := service(root)
	plan := only(t, s.StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age, Remote: true}, nil))
	if len(plan.Detail.Remote) != 0 {
		t.Errorf("planned %+v", plan.Detail.Remote)
	}

	// Its target hidden, the tag shares no object anything shows; the alias is
	// still named as one, over the protocol v2 orbit asks in.
	run(t, bare, nil, "config", "uploadpack.hideRefs", "refs/tags/saved-tag")
	plan = only(t, s.StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age, Remote: true}, nil))
	if len(plan.Detail.Remote) != 0 {
		t.Errorf("hidden target: planned %+v", plan.Detail.Remote)
	}
	plan.Outcome = OutcomeChanged
	plan.Detail.Remote = []Branch{{Name: "old-alias", Remote: "origin", Commit: run(t, upstream, nil, "rev-parse", "saved-tag")}}
	s.DeleteBranches(context.Background(), []Result[Branches]{plan}, nil)
	if tags := run(t, bare, nil, "tag"); tags != "saved-tag" {
		t.Errorf("tags left: %q", tags)
	}
}

// A path's trailing space is part of it: a pushurl differing from the fetch URL
// by one is another repository.
func TestURLsAreComparedAsGitHasThem(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	run(t, upstream, nil, "switch", "--quiet", "-c", "old")
	commit(t, upstream, "old.txt", "x", time.Now().AddDate(-1, 0, 0))
	run(t, upstream, nil, "push", "--quiet", "origin", "old")
	local := clone(t, bare, filepath.Join(root, "local"))
	run(t, root, nil, "clone", "--quiet", "--bare", bare, bare+" ")
	run(t, local, nil, "config", "remote.origin.pushurl", bare+" ")
	age, _ := core.ParseAge("6mo")
	plan := only(t, service(root).StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age, Remote: true}, nil))
	if len(plan.Detail.Remote) != 0 || len(plan.Notes) != 1 {
		t.Errorf("plan = %+v, notes %q", plan.Detail.Remote, plan.Notes)
	}
}

// A clone and its worktree share their branches: they are listed once, and
// deleted once, which succeeds.
func TestWorktreesShareOnePlan(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	run(t, upstream, nil, "switch", "--quiet", "-c", "old")
	commit(t, upstream, "old.txt", "x", time.Now().AddDate(-1, 0, 0))
	run(t, upstream, nil, "push", "--quiet", "origin", "old")
	if err := os.MkdirAll(filepath.Join(root, "code"), 0o755); err != nil {
		t.Fatal(err)
	}
	local := clone(t, bare, filepath.Join(root, "code", "local"))
	run(t, local, nil, "worktree", "add", "--quiet", "-b", "side", filepath.Join(root, "code", "side"))
	s := service(root)
	repos, err := s.Discover(context.Background(), []string{filepath.Join(root, "code")})
	if err != nil || len(repos) != 2 {
		t.Fatalf("repos = %+v, %v", repos, err)
	}
	age, _ := core.ParseAge("6mo")
	plans := s.StaleBranches(context.Background(), repos, BranchOptions{OlderThan: age, Remote: true}, nil)
	if plans[0].Detail.Total() != 1 || plans[1].Detail.Total() != 0 || len(plans[1].Notes) != 1 {
		t.Fatalf("plans = %+v", plans)
	}
	deleted := s.DeleteBranches(context.Background(), plans, nil)
	if len(deleted) != 2 || deleted[0].Outcome != OutcomeChanged || deleted[1].Outcome != OutcomeUnchanged || len(deleted[1].Notes) != 1 {
		t.Errorf("deleted = %+v", deleted)
	}
}

// What deleting reports is every repository planned, a plan that failed or had
// nothing to delete as it was, so --json accounts for each.
func TestDeletingReportsEveryPlan(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	run(t, upstream, nil, "switch", "--quiet", "-c", "old")
	commit(t, upstream, "old.txt", "x", time.Now().AddDate(-1, 0, 0))
	run(t, upstream, nil, "push", "--quiet", "origin", "old")
	local := clone(t, bare, filepath.Join(root, "local"))
	age, _ := core.ParseAge("6mo")
	s := service(root)
	plans := s.StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age, Remote: true}, nil)
	plans[0].Notes = append(plans[0].Notes, "noted while planning")
	plans = append([]Result[Branches]{{Repository: Repository{Path: "/a", Name: "a"}, Outcome: OutcomeFailed, Error: "fetch failed"}}, plans...)
	plans = append(plans, Result[Branches]{Repository: Repository{Path: "/z", Name: "z"}, Outcome: OutcomeSkipped, Reason: "no default"})

	deleted := s.DeleteBranches(context.Background(), plans, nil)
	if len(deleted) != 3 || deleted[0].Error != "fetch failed" || deleted[1].Outcome != OutcomeChanged || deleted[2].Reason != "no default" {
		t.Errorf("deleted = %+v", deleted)
	}
	if !slices.Contains(deleted[1].Notes, "noted while planning") {
		t.Errorf("the plan's notes are gone: %q", deleted[1].Notes)
	}
}

// A switch can fail having switched, as when a post-checkout hook fails: the
// changes made on the branch left stay in the stash rather than be put back on
// the default branch.
func TestASwitchThatFailedHavingSwitchedKeepsTheStash(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	run(t, local, nil, "switch", "--quiet", "-c", "feature")
	write(t, local, "feature-work.txt", "mine\n")
	write(t, local, ".git/hooks/post-checkout", "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(filepath.Join(local, ".git", "hooks", "post-checkout"), 0o755); err != nil {
		t.Fatal(err)
	}

	result := only(t, service(root).Update(context.Background(), []Repository{repository(local)}, UpdateOptions{Default: true}, nil))
	if result.Outcome != OutcomeFailed || !result.Detail.Switched || result.Detail.Restored || result.Detail.Stashed == "" {
		t.Fatalf("result = %+v", result)
	}
	if branch := run(t, local, nil, "branch", "--show-current"); branch != "main" {
		t.Errorf("on %s", branch)
	}
	if _, err := os.Stat(filepath.Join(local, "feature-work.txt")); !os.IsNotExist(err) {
		t.Error("the changes made on feature were put back on main")
	}
	if stashes := run(t, local, nil, "stash", "list"); !strings.Contains(stashes, "On feature: ") {
		t.Errorf("stashes: %q", stashes)
	}
}

// A repository whose .git is gone since it was found is not taken for the
// repository around it: cleaning it fails rather than cleaning the parent's.
func TestAGitDirectoryGoneSinceDiscoveryFails(t *testing.T) {
	root := isolate(t)
	parent := filepath.Join(root, "parent")
	run(t, root, nil, "init", "--quiet", parent)
	commit(t, parent, "tracked.txt", "x", time.Time{})
	write(t, parent, "parent-scratch.txt", "the parent's")
	child := filepath.Join(parent, "child")
	run(t, root, nil, "init", "--quiet", child)
	write(t, child, "child-scratch.txt", "the child's")
	s := service(root)
	repos, err := s.Discover(context.Background(), []string{child})
	if real, _ := filepath.EvalSymlinks(child); err != nil || len(repos) != 1 || repos[0].Path != real {
		t.Fatalf("repos = %+v, %v", repos, err)
	}

	if err := os.RemoveAll(filepath.Join(child, ".git")); err != nil {
		t.Fatal(err)
	}
	if cleaned := only(t, s.Clean(context.Background(), repos, nil)); cleaned.Outcome != OutcomeFailed {
		t.Errorf("cleaned = %+v", cleaned)
	}
	if read(t, parent, "parent-scratch.txt") != "the parent's" || read(t, child, "child-scratch.txt") != "the child's" {
		t.Error("the parent repository was cleaned")
	}
}

// stoppingLater runs git, and stops the batch a moment after git runs with
// the subcommand.
type stoppingLater struct {
	Git
	subcommand string
	after      time.Duration
	stop       context.CancelFunc
}

func (s *stoppingLater) Run(ctx context.Context, dir string, args ...string) (string, error) {
	if len(args) > 0 && args[0] == s.subcommand {
		time.AfterFunc(s.after, s.stop)
	}
	return s.Git.Run(ctx, dir, args...)
}

// A pull stopped in its post-merge hook has fast-forwarded: changes that touch
// what it pulled stay in the stash rather than be put back, in conflict, onto
// commits they were not made on.
func TestAPullStoppedAfterFastForwardingKeepsOverlappingChanges(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	commit(t, upstream, "b.txt", "theirs\n", time.Time{})
	run(t, upstream, nil, "push", "--quiet")
	write(t, local, "b.txt", "mine\n")
	write(t, local, ".git/hooks/post-merge", "#!/bin/sh\nsleep 5\n")
	if err := os.Chmod(filepath.Join(local, ".git", "hooks", "post-merge"), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := service(root)
	s.Git = &stoppingLater{Git: s.Git, subcommand: "merge", after: time.Second, stop: cancel}
	result := only(t, s.Update(ctx, []Repository{repository(local)}, UpdateOptions{}, nil))
	if result.Detail.Pulled != 1 || result.Detail.Stashed == "" || result.Detail.Restored {
		t.Fatalf("result = %+v", result)
	}
	if read(t, local, "b.txt") != "theirs\n" {
		t.Errorf("b.txt is %q", read(t, local, "b.txt"))
	}
}

// status.aheadBehind=false would have git status print "+? -?": orbit asks for
// the counts whatever the configuration says.
func TestStatusCountsWhateverTheConfiguration(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	commit(t, local, "c.txt", "c\n", time.Time{})
	run(t, local, nil, "config", "status.aheadBehind", "false")
	result := only(t, service(root).List(context.Background(), []Repository{repository(local)}, nil))
	if result.Outcome != OutcomeUnchanged || result.Detail.Ahead != 1 {
		t.Errorf("result = %+v", result)
	}
}

// A gc that fails freed nothing, whatever the repository measured before it.
func TestAFailedCollectionFreesNothing(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	// gc refuses while another holds its lock.
	write(t, local, ".git/gc.pid", fmt.Sprintf("%d %s\n", os.Getpid(), mustHostname(t)))
	collected := only(t, service(root).GC(context.Background(), []Repository{repository(local)}, GCOptions{}, nil))
	if collected.Outcome != OutcomeFailed || collected.Detail.Freed() != 0 || collected.Detail.BytesBefore == 0 {
		t.Errorf("collected = %+v", collected)
	}
}

func mustHostname(t *testing.T) string {
	t.Helper()
	name, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	return name
}

// A stop as list reads the default branch, whose error list does not fail on,
// is a stop: the batch did not finish.
func TestAStopIsNeverTakenForNothingToReport(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := service(root)
	s.Git = &stoppingAt{Git: s.Git, arg: "refs/remotes/origin/HEAD", stop: cancel}
	if result := only(t, s.List(ctx, []Repository{repository(local)}, nil)); result.Outcome != OutcomeCancelled {
		t.Errorf("result = %+v", result)
	}
}

// Over protocol v0 an alias can name any ref, the notes too: a branch at the
// commit of any ref kept on the remote is kept.
func TestARemoteAliasOfTheNotesIsKeptOverProtocolV0(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	run(t, upstream, nil, "switch", "--quiet", "-c", "noted")
	commit(t, upstream, "noted.txt", "x", time.Now().AddDate(-1, 0, 0))
	run(t, upstream, nil, "push", "--quiet", "origin", "noted:refs/notes/commits")
	run(t, bare, nil, "symbolic-ref", "refs/heads/notes-alias", "refs/notes/commits")
	local := clone(t, bare, filepath.Join(root, "local"))
	v0 := filepath.Join(root, "v0-upload-pack")
	write(t, root, "v0-upload-pack", "#!/bin/sh\nunset GIT_PROTOCOL\nexec git-upload-pack \"$@\"\n")
	if err := os.Chmod(v0, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, local, nil, "config", "remote.origin.uploadpack", v0)

	age, _ := core.ParseAge("6mo")
	s := service(root)
	plan := only(t, s.StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age, Remote: true}, nil))
	if len(plan.Detail.Remote) != 0 {
		t.Errorf("planned %+v", plan.Detail.Remote)
	}
	plan.Outcome = OutcomeChanged
	plan.Detail.Remote = []Branch{{Name: "notes-alias", Remote: "origin", Commit: run(t, upstream, nil, "rev-parse", "noted")}}
	s.DeleteBranches(context.Background(), []Result[Branches]{plan}, nil)
	if notes := run(t, bare, nil, "for-each-ref", "--format=%(refname)", "refs/notes"); notes != "refs/notes/commits" {
		t.Errorf("notes left: %q", notes)
	}
}

// A server speaking only protocol v0 names no alias but HEAD's: an alias of a
// tag there is kept by the tag it shares its commit with.
func TestARemoteAliasOfATagIsKeptOverProtocolV0(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	run(t, upstream, nil, "switch", "--quiet", "-c", "saved")
	commit(t, upstream, "saved.txt", "x", time.Now().AddDate(-1, 0, 0))
	run(t, upstream, nil, "tag", "saved-tag")
	run(t, upstream, nil, "push", "--quiet", "origin", "refs/tags/saved-tag")
	run(t, bare, nil, "symbolic-ref", "refs/heads/old-alias", "refs/tags/saved-tag")
	local := clone(t, bare, filepath.Join(root, "local"))
	// An upload-pack that never speaks v2, whatever the client asks for.
	v0 := filepath.Join(root, "v0-upload-pack")
	write(t, root, "v0-upload-pack", "#!/bin/sh\nunset GIT_PROTOCOL\nexec git-upload-pack \"$@\"\n")
	if err := os.Chmod(v0, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, local, nil, "config", "remote.origin.uploadpack", v0)
	// Its target hidden too, the alias shows no sign of being one.
	run(t, bare, nil, "config", "uploadpack.hideRefs", "refs/tags/saved-tag")

	age, _ := core.ParseAge("6mo")
	s := service(root)
	plan := only(t, s.StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age, Remote: true}, nil))
	if len(plan.Detail.Remote) != 0 || !slices.ContainsFunc(plan.Notes, func(note string) bool { return strings.Contains(note, "protocol v0") }) {
		t.Errorf("planned %+v, notes %q", plan.Detail.Remote, plan.Notes)
	}
	plan.Outcome = OutcomeChanged
	plan.Detail.Remote = []Branch{{Name: "old-alias", Remote: "origin", Commit: run(t, upstream, nil, "rev-parse", "saved-tag")}}
	s.DeleteBranches(context.Background(), []Result[Branches]{plan}, nil)
	if tags := run(t, bare, nil, "tag"); tags != "saved-tag" {
		t.Errorf("tags left: %q", tags)
	}
}

// A file listed, then made a directory while the person reads the list, is not
// removed with everything put in it since.
func TestAPlannedFileThatBecameADirectoryIsLeft(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	write(t, local, "scratch", "x")
	write(t, local, "other.txt", "x")
	s := service(root)
	plans := s.Uncleaned(context.Background(), []Repository{repository(local)}, nil)

	if err := os.Remove(filepath.Join(local, "scratch")); err != nil {
		t.Fatal(err)
	}
	write(t, local, "scratch/important.txt", "keep me")
	cleaned := only(t, s.CleanPlanned(context.Background(), plans, nil))
	if read(t, local, "scratch/important.txt") != "keep me" || len(cleaned.Notes) != 1 {
		t.Errorf("cleaned = %+v", cleaned)
	}
	if _, err := os.Stat(filepath.Join(local, "other.txt")); !os.IsNotExist(err) {
		t.Error("the file still a file was not removed")
	}
}

// stoppingAt runs git, and stops the batch as git runs with an argument.
type stoppingAt struct {
	Git
	arg  string
	stop context.CancelFunc
}

func (s *stoppingAt) Run(ctx context.Context, dir string, args ...string) (string, error) {
	if slices.ContainsFunc(args, func(arg string) bool { return strings.Contains(arg, s.arg) }) {
		s.stop()
	}
	return s.Git.Run(ctx, dir, args...)
}

// Stopped as it reads the upstream, an update is stopped, not skipped: the
// upstream is not gone, and the exit code must say the batch did not finish.
func TestAStopIsNeverTakenForASkip(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := service(root)
	s.Git = &stoppingAt{Git: s.Git, arg: "@{upstream}", stop: cancel}
	if result := only(t, s.Update(ctx, []Repository{repository(local)}, UpdateOptions{}, nil)); result.Outcome != OutcomeCancelled {
		t.Errorf("result = %+v", result)
	}
}

// With origin's branches fetched into another namespace, origin/HEAD names
// origin/archive/develop, which is develop: the default branch, kept.
func TestTheDefaultBranchIsReadThroughTheRefspecs(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	old := time.Now().AddDate(-1, 0, 0)
	run(t, upstream, nil, "switch", "--quiet", "-c", "develop")
	commit(t, upstream, "develop.txt", "x", old)
	run(t, upstream, nil, "push", "--quiet", "origin", "develop")
	run(t, bare, nil, "symbolic-ref", "HEAD", "refs/heads/develop")
	local := clone(t, bare, filepath.Join(root, "local"))
	run(t, local, nil, "config", "--replace-all", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/archive/*")
	run(t, local, nil, "fetch", "--quiet", "--prune")
	run(t, local, nil, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/archive/develop")
	run(t, local, nil, "branch", "--quiet", "--set-upstream-to", "origin/archive/develop", "develop")
	run(t, local, nil, "switch", "--quiet", "-c", "feature")

	age, _ := core.ParseAge("6mo")
	plan := only(t, service(root).StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age}, nil))
	if plan.Outcome == OutcomeSkipped || len(plan.Detail.Local) != 0 {
		t.Errorf("plan = %+v", plan)
	}
	if listed := only(t, service(root).List(context.Background(), []Repository{repository(local)}, nil)); listed.Detail.Default != "develop" {
		t.Errorf("list says the default branch is %q", listed.Detail.Default)
	}
}

// origin/HEAD is what the remote's default was when the repository was cloned:
// once the remote's moves on, from main to develop, the remote is what says so,
// and origin/HEAD is used only when it cannot be asked.
func TestTheDefaultBranchIsAskedOfTheRemote(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	old := time.Now().AddDate(-1, 0, 0)
	run(t, upstream, nil, "switch", "--quiet", "-c", "develop")
	commit(t, upstream, "develop.txt", "x", old)
	run(t, upstream, nil, "push", "--quiet", "origin", "develop")
	run(t, bare, nil, "symbolic-ref", "HEAD", "refs/heads/develop")
	run(t, local, nil, "fetch", "--quiet")
	run(t, local, nil, "branch", "--quiet", "--track", "develop", "origin/develop")
	run(t, local, nil, "switch", "--quiet", "-c", "feature")
	if head := run(t, local, nil, "symbolic-ref", "refs/remotes/origin/HEAD"); head != "refs/remotes/origin/main" {
		t.Fatalf("origin/HEAD followed the remote: %s", head)
	}
	s := service(root)
	age, _ := core.ParseAge("6mo")

	plan := only(t, s.StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age}, nil))
	if plan.Outcome == OutcomeSkipped || len(plan.Detail.Local) != 0 {
		t.Errorf("plan = %+v", plan)
	}
	updated := only(t, s.Update(context.Background(), []Repository{repository(local)}, UpdateOptions{Default: true}, nil))
	if updated.Detail.Branch != "develop" || run(t, local, nil, "branch", "--show-current") != "develop" {
		t.Errorf("updated = %+v", updated)
	}

	// Offline, origin/HEAD as recorded names it.
	run(t, local, nil, "switch", "--quiet", "feature")
	run(t, local, nil, "remote", "set-head", "origin", "develop")
	run(t, local, nil, "remote", "set-url", "origin", filepath.Join(root, "gone.git"))
	plan = only(t, s.StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age}, nil))
	if plan.Outcome == OutcomeSkipped || len(plan.Detail.Local) != 0 {
		t.Errorf("offline: plan = %+v", plan)
	}
}

// A branch at a tagged commit, as an old release branch at its release is, is
// deleted: the tag keeps everything it had.
func TestATaggedRemoteBranchIsDeleted(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	run(t, upstream, nil, "switch", "--quiet", "-c", "release/1.0")
	commit(t, upstream, "release.txt", "x", time.Now().AddDate(-1, 0, 0))
	run(t, upstream, nil, "tag", "v1.0")
	run(t, upstream, nil, "push", "--quiet", "origin", "release/1.0", "refs/tags/v1.0")
	local := clone(t, bare, filepath.Join(root, "local"))
	age, _ := core.ParseAge("6mo")
	s := service(root)
	plan := only(t, s.StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age, Remote: true}, nil))
	if len(plan.Detail.Remote) != 1 || plan.Detail.Remote[0].Name != "release/1.0" {
		t.Fatalf("plan = %+v", plan.Detail.Remote)
	}
	if deleted := only(t, s.DeleteBranches(context.Background(), []Result[Branches]{plan}, nil)); deleted.Outcome != OutcomeChanged {
		t.Errorf("deleted = %+v", deleted)
	}
	if left := run(t, bare, nil, "for-each-ref", "--format=%(refname)"); left != "refs/heads/main\nrefs/tags/v1.0" {
		t.Errorf("left on the remote: %q", left)
	}
}

// The remote deleted from is the remote read: a pushurl set while the person
// reads the plan stops the deletion.
func TestARemoteChangedAfterPlanningHasNoBranchesDeleted(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	run(t, upstream, nil, "switch", "--quiet", "-c", "old")
	commit(t, upstream, "old.txt", "x", time.Now().AddDate(-1, 0, 0))
	run(t, upstream, nil, "push", "--quiet", "origin", "old")
	local := clone(t, bare, filepath.Join(root, "local"))
	age, _ := core.ParseAge("6mo")
	s := service(root)
	plan := only(t, s.StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age, Remote: true}, nil))
	if len(plan.Detail.Remote) != 1 {
		t.Fatalf("plan = %+v", plan.Detail)
	}

	fork := filepath.Join(root, "fork.git")
	run(t, root, nil, "clone", "--quiet", "--bare", bare, fork)
	run(t, local, nil, "config", "remote.origin.pushurl", fork)
	deleted := only(t, s.DeleteBranches(context.Background(), []Result[Branches]{plan}, nil))
	if deleted.Outcome != OutcomeFailed || deleted.Detail.Remote[0].Deleted {
		t.Errorf("deleted = %+v", deleted)
	}
	if left := run(t, fork, nil, "branch", "--format=%(refname:short)"); !strings.Contains(left, "old") {
		t.Errorf("the fork's branch was deleted: %q", left)
	}
}

// What would be deleted on a remote is what was read from it: a remote that
// pushes elsewhere, as to a fork, has none of its branches deleted.
func TestARemotePushingElsewhereHasNoBranchesDeleted(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	run(t, upstream, nil, "switch", "--quiet", "-c", "old")
	commit(t, upstream, "old.txt", "x", time.Now().AddDate(-1, 0, 0))
	run(t, upstream, nil, "push", "--quiet", "origin", "old")
	local := clone(t, bare, filepath.Join(root, "local"))
	fork := filepath.Join(root, "fork.git")
	run(t, root, nil, "clone", "--quiet", "--bare", bare, fork)
	run(t, local, nil, "config", "remote.origin.pushurl", fork)

	age, _ := core.ParseAge("6mo")
	plan := only(t, service(root).StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age, Remote: true}, nil))
	if len(plan.Detail.Remote) != 0 || len(plan.Notes) != 1 || !strings.Contains(plan.Notes[0], "pushes somewhere other") {
		t.Errorf("plan = %+v, notes %q", plan.Detail.Remote, plan.Notes)
	}
}

// A remote fetched outside refs/remotes/<remote> -- into refs/remotes/mirror --
// has its branches found there, to delete and to switch to.
func TestARemoteFetchedElsewhereIsReadThere(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	run(t, upstream, nil, "switch", "--quiet", "-c", "old")
	commit(t, upstream, "old.txt", "x", time.Now().AddDate(-1, 0, 0))
	run(t, upstream, nil, "push", "--quiet", "origin", "old")
	local := clone(t, bare, filepath.Join(root, "local"))
	run(t, local, nil, "config", "--replace-all", "remote.origin.fetch", "+refs/heads/*:refs/remotes/mirror/*")
	run(t, local, nil, "fetch", "--quiet", "--prune")
	run(t, local, nil, "switch", "--quiet", "-c", "feature")
	run(t, local, nil, "branch", "--quiet", "-D", "main")
	s := service(root)

	updated := only(t, s.Update(context.Background(), []Repository{repository(local)}, UpdateOptions{Default: true}, nil))
	if updated.Detail.Branch != "main" || run(t, local, nil, "branch", "--show-current") != "main" {
		t.Errorf("updated = %+v", updated)
	}
	age, _ := core.ParseAge("6mo")
	plan := only(t, s.StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age, Remote: true}, nil))
	if len(plan.Detail.Remote) != 1 || plan.Detail.Remote[0].Tracking != "refs/remotes/mirror/old" {
		t.Fatalf("plan = %+v", plan.Detail.Remote)
	}
	s.DeleteBranches(context.Background(), []Result[Branches]{plan}, nil)
	if left := run(t, bare, nil, "branch", "--format=%(refname:short)"); left != "main" {
		t.Errorf("left on the remote: %q", left)
	}
}

// A ref fetched from a pull request is no branch, and a remote fetched into
// another namespace has its branches named as they are on it.
func TestRemoteBranchesAreNamedAsTheRemoteHasThem(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	old := time.Now().AddDate(-1, 0, 0)
	run(t, upstream, nil, "switch", "--quiet", "-c", "feature")
	commit(t, upstream, "feature.txt", "x", old)
	run(t, upstream, nil, "push", "--quiet", "origin", "feature")
	run(t, upstream, nil, "push", "--quiet", "origin", "feature:refs/pull/12/head")
	local := clone(t, bare, filepath.Join(root, "local"))
	run(t, local, nil, "config", "--add", "remote.origin.fetch", "+refs/pull/*/head:refs/remotes/origin/pull/*")
	run(t, local, nil, "fetch", "--quiet")
	age, _ := core.ParseAge("6mo")
	s := service(root)
	plan := only(t, s.StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age, Remote: true}, nil))
	if len(plan.Detail.Remote) != 1 || plan.Detail.Remote[0].Name != "feature" {
		t.Errorf("plan = %+v", plan.Detail.Remote)
	}

	archived := clone(t, bare, filepath.Join(root, "archived"))
	run(t, archived, nil, "config", "--replace-all", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/archive/*")
	run(t, archived, nil, "fetch", "--quiet", "--prune")
	plan = only(t, s.StaleBranches(context.Background(), []Repository{repository(archived)}, BranchOptions{OlderThan: age, Remote: true}, nil))
	if len(plan.Detail.Remote) != 1 || plan.Detail.Remote[0].Name != "feature" || plan.Detail.Remote[0].Tracking != "refs/remotes/origin/archive/feature" {
		t.Fatalf("archived plan = %+v", plan.Detail.Remote)
	}
	s.DeleteBranches(context.Background(), []Result[Branches]{plan}, nil)
	if left := run(t, bare, nil, "for-each-ref", "--format=%(refname)"); left != "refs/heads/main\nrefs/pull/12/head" {
		t.Errorf("left on the remote: %q", left)
	}
}

// A branch is deleted with its configuration, but never while a worktree has it
// checked out.
func TestDeletingALocalBranchRemovesItsConfiguration(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	old := time.Now().AddDate(-1, 0, 0)
	for _, branch := range []string{"gone", "elsewhere"} {
		run(t, local, nil, "switch", "--quiet", "-c", branch, "main")
		commit(t, local, branch+".txt", "x", old)
		run(t, local, nil, "config", "branch."+branch+".description", "work")
	}
	run(t, local, nil, "switch", "--quiet", "main")
	age, _ := core.ParseAge("6mo")
	s := service(root)
	plan := only(t, s.StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age}, nil))
	if len(plan.Detail.Local) != 2 {
		t.Fatalf("plan = %+v", plan.Detail)
	}
	run(t, local, nil, "worktree", "add", "--quiet", filepath.Join(root, "wt"), "elsewhere")

	deleted := only(t, s.DeleteBranches(context.Background(), []Result[Branches]{plan}, nil))
	for _, branch := range deleted.Detail.Local {
		if branch.Deleted != (branch.Name == "gone") {
			t.Errorf("%s: deleted %v, %s", branch.Name, branch.Deleted, branch.Error)
		}
	}
	if config := run(t, local, nil, "config", "--get-regexp", "^branch\\."); strings.Contains(config, "branch.gone") {
		t.Errorf("configuration left: %s", config)
	}
}

func TestUpdateKeepsWhatWasStagedStaged(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	commit(t, upstream, "b.txt", "b2\n", time.Time{})
	run(t, upstream, nil, "push", "--quiet")
	write(t, local, "a.txt", "staged\n")
	run(t, local, nil, "add", "a.txt")

	result := only(t, service(root).Update(context.Background(), []Repository{repository(local)}, UpdateOptions{}, nil))
	if !result.Detail.Restored {
		t.Fatalf("result = %+v", result)
	}
	if status := run(t, local, nil, "status", "--porcelain"); status != "M  a.txt" {
		t.Errorf("status = %q", status)
	}
}

// Worktrees share one stash: updated in one batch, each gets its own changes back.
func TestWorktreesOfOneRepositoryAreUpdatedInTurn(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	run(t, upstream, nil, "switch", "--quiet", "-c", "other")
	run(t, upstream, nil, "push", "--quiet", "origin", "other")
	run(t, upstream, nil, "switch", "--quiet", "main")
	if err := os.MkdirAll(filepath.Join(root, "code"), 0o755); err != nil {
		t.Fatal(err)
	}
	main := clone(t, bare, filepath.Join(root, "code", "main"))
	linked := filepath.Join(root, "code", "linked")
	run(t, main, nil, "worktree", "add", "--quiet", linked, "other")
	run(t, linked, nil, "branch", "--quiet", "--set-upstream-to", "origin/other")
	commit(t, upstream, "b.txt", "main2\n", time.Time{})
	run(t, upstream, nil, "push", "--quiet", "origin", "main")
	run(t, upstream, nil, "switch", "--quiet", "other")
	commit(t, upstream, "b.txt", "other2\n", time.Time{})
	run(t, upstream, nil, "push", "--quiet", "origin", "other")
	write(t, main, "a.txt", "main's\n")
	write(t, linked, "a.txt", "linked's\n")

	s := service(root)
	repos, err := s.Discover(context.Background(), []string{filepath.Join(root, "code")})
	if err != nil || len(repos) != 2 {
		t.Fatalf("repos = %+v, %v", repos, err)
	}
	for _, result := range s.Update(context.Background(), repos, UpdateOptions{}, nil) {
		if result.Outcome != OutcomeChanged || !result.Detail.Restored {
			t.Errorf("%s: %+v", result.Repository.Name, result)
		}
	}
	if read(t, main, "a.txt") != "main's\n" || read(t, linked, "a.txt") != "linked's\n" {
		t.Errorf("changes swapped: main has %q, linked %q", read(t, main, "a.txt"), read(t, linked, "a.txt"))
	}
}

// A remote branch whose last push is old, but whose local branch has newer
// commits, is somebody's work in progress.
func TestRemoteBranchesTrackedByNewerLocalOnesAreKept(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	old := time.Now().AddDate(-1, 0, 0)
	run(t, upstream, nil, "switch", "--quiet", "-c", "work")
	commit(t, upstream, "work.txt", "old", old)
	run(t, upstream, nil, "push", "--quiet", "origin", "work")
	local := clone(t, bare, filepath.Join(root, "local"))
	run(t, local, nil, "switch", "--quiet", "work")
	commit(t, local, "work.txt", "new", time.Time{})
	run(t, local, nil, "switch", "--quiet", "main")

	age, _ := core.ParseAge("6mo")
	plan := only(t, service(root).StaleBranches(context.Background(), []Repository{repository(local)}, BranchOptions{OlderThan: age, Remote: true}, nil))
	if plan.Detail.Total() != 0 {
		t.Errorf("planned %+v", plan.Detail)
	}
}

// Every repository starts and finishes once, however many run at once, and a
// batch cancelled before it starts reaches none of them.
func TestEachReportsEveryRepositoryOnce(t *testing.T) {
	root := isolate(t)
	var repos []Repository
	for i := range 12 {
		dir := filepath.Join(root, "repo", string(rune('a'+i)))
		run(t, root, nil, "init", "--quiet", dir)
		repos = append(repos, repository(dir))
	}
	var mu sync.Mutex
	started, finished := map[string]int{}, map[string]int{}
	progress := func(event Event[Status]) {
		mu.Lock()
		defer mu.Unlock()
		switch event.Kind {
		case EventStarted:
			started[event.Repository.Path]++
		case EventFinished:
			finished[event.Repository.Path]++
		}
	}
	s := service(root)
	s.Concurrency = 3
	results := s.List(context.Background(), repos, progress)
	for i, result := range results {
		if result.Repository.Path != repos[i].Path || result.Outcome != OutcomeUnchanged {
			t.Errorf("result %d = %+v", i, result)
		}
		if started[repos[i].Path] != 1 || finished[repos[i].Path] != 1 {
			t.Errorf("%s started %d times, finished %d", repos[i].Name, started[repos[i].Path], finished[repos[i].Path])
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if tally := Count(s.List(ctx, repos, nil)); tally.Cancelled != len(repos) {
		t.Errorf("cancelled: %+v", tally)
	}
}
