package git

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	core "github.com/andrerfcsantos/orbit/internal/core/git"
	platformgit "github.com/andrerfcsantos/orbit/internal/platform/git"
)

// The git a task asks the repository, shared by the operations.

func (t *task) status() (core.Status, error) {
	out, err := t.run(core.StatusArgs...)
	if err != nil {
		return core.Status{}, err
	}
	return core.ParseStatus(out)
}

// exists reports whether ref names a commit.
func (t *task) exists(ref string) bool {
	_, err := t.run("rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil
}

// stash puts every change away, untracked files included, and returns the
// stash's commit and its message, or nothing when there was nothing to put away.
// A stash saved but not all put away returns both the stash and an error.
//
// The message carries a token of its own, by which the stash is found: worktrees
// share one stash, so the newest entry may be another worktree's, pushed a moment
// after this one.
func (t *task) stash(message string) (string, string, error) {
	token := make([]byte, 4)
	if _, err := rand.Read(token); err != nil {
		return "", "", err
	}
	message = fmt.Sprintf("%s [orbit %x]", message, token)
	_, pushErr := t.run("stash", "push", "--include-untracked", "--message", message)
	// Looked for even when the work was stopped as git stashed, which may have
	// put the changes away all the same: found, they are put back.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.ctx), restoreTimeout)
	defer cancel()
	out, err := t.git.Run(ctx, t.repo.Path, "stash", "list", "--format=%H%x00%gs")
	if err == nil {
		for _, line := range core.Lines(out) {
			commit, subject, ok := strings.Cut(line, "\x00")
			if !ok || !strings.HasSuffix(subject, message) {
				continue
			}
			// git can save the stash and then fail to put the changes away, as
			// when an untracked file cannot be removed: the working tree says
			// which. Half put away, the changes are both there and in the stash.
			if pushErr != nil {
				if remaining, err := t.git.Run(ctx, t.repo.Path, "status", "--porcelain", "--untracked-files=normal"); err != nil || remaining != "" {
					return commit, message, fmt.Errorf("stashing your changes: %w", pushErr)
				}
			}
			return commit, message, nil
		}
	}
	if pushErr != nil {
		return "", "", fmt.Errorf("stashing your changes: %w", pushErr)
	}
	return "", "", err
}

// restoreTimeout bounds putting changes back once the work was stopped.
const restoreTimeout = time.Minute

// pop puts back the stash whose commit is given, staged changes staged again,
// and drops it. It runs even when the work has been stopped, or has run out of
// time: the changes were stashed by orbit, and are put back before it goes.
//
// A stash is applied by its commit, which nothing can change, and dropped by its
// place, which another git stashing meanwhile can: what was dropped is checked,
// and put back when it is not orbit's.
func (t *task) pop(commit string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.ctx), restoreTimeout)
	defer cancel()
	run := func(args ...string) (string, error) { return t.git.Run(ctx, t.repo.Path, args...) }
	if _, err := run("stash", "apply", "--index", "--quiet", commit); err != nil {
		if _, err := run("stash", "apply", "--quiet", commit); err != nil {
			return err
		}
	}

	// The changes are back; a stash entry left over is only clutter.
	for range 3 {
		out, err := run("stash", "list", "--format=%gd %H")
		if err != nil {
			return nil
		}
		place := ""
		for _, line := range core.Lines(out) {
			if name, hash, ok := strings.Cut(line, " "); ok && hash == commit {
				place = name
			}
		}
		if place == "" {
			return nil
		}
		dropped, err := run("stash", "drop", place)
		if err != nil {
			return nil
		}
		// "Dropped refs/stash@{0} (<commit>)"
		open, closing := strings.LastIndex(dropped, "("), strings.LastIndex(dropped, ")")
		if open < 0 || closing < open {
			return nil
		}
		other := dropped[open+1 : closing]
		if other == commit {
			return nil
		}
		// A stash commit's subject is its message: "On main: work in progress".
		message, err := run("log", "-1", "--format=%s", other)
		if err != nil || strings.TrimSpace(message) == "" {
			message = "put back by orb git update"
		}
		if _, err := run("stash", "store", "--message", strings.TrimSpace(message), other); err != nil {
			return fmt.Errorf("another stash, %s, was dropped in place of orbit's and could not be put back: %w", other, err)
		}
	}
	return nil
}

// inProgress names what git is in the middle of -- a merge, a rebase -- or is
// empty when it is in the middle of nothing.
func (t *task) inProgress() (string, error) {
	states := []struct{ path, what string }{
		{"MERGE_HEAD", "a merge"},
		{"rebase-merge", "a rebase"},
		{"rebase-apply", "a rebase"},
		{"CHERRY_PICK_HEAD", "a cherry-pick"},
		{"REVERT_HEAD", "a revert"},
	}
	args := []string{"rev-parse"}
	for _, state := range states {
		args = append(args, "--git-path", state.path)
	}
	out, err := t.run(args...)
	if err != nil {
		return "", err
	}
	for i, path := range core.Lines(out) {
		if i >= len(states) {
			break
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(t.repo.Path, path)
		}
		if _, err := os.Lstat(path); err == nil {
			return states[i].what, nil
		}
	}
	return "", nil
}

// primaryRemote is origin, or the only remote when there is one and it has
// another name. It is empty when there is no remote, or several and no origin.
func (t *task) primaryRemote() (string, error) {
	out, err := t.run("remote")
	if err != nil {
		return "", err
	}
	remotes := core.Lines(out)
	for _, remote := range remotes {
		if remote == "origin" {
			return remote, nil
		}
	}
	if len(remotes) == 1 {
		return remotes[0], nil
	}
	return "", nil
}

// defaultBranch finds the repository's default branch without GitHub: the branch
// the remote says its HEAD names, asked of it as clone does; when it cannot be
// asked, the one refs/remotes/origin/HEAD names, as clone recorded it; failing
// that, main or master.
//
// It returns the branch as the remote names it, and the remote-tracking ref it
// is fetched into, empty when only a local main or master was found.
func (t *task) defaultBranch() (name, remote, tracking string, err error) {
	if remote, err = t.primaryRemote(); err != nil {
		return "", "", "", err
	}
	if remote != "" {
		if name, ok := t.askedDefault(remote); ok {
			return name, remote, t.trackingRef(remote, name), nil
		}
		if name, tracking := t.remoteHead(remote); name != "" {
			return name, remote, tracking, nil
		}
		for _, name := range protected {
			if tracking := t.trackingRef(remote, name); tracking != "" {
				return name, remote, tracking, nil
			}
		}
	}
	for _, name := range protected {
		if t.exists("refs/heads/" + name) {
			return name, remote, "", nil
		}
	}
	if remote == "" {
		return "", "", "", skip("no default branch was found: there is no remote to ask, and no main or master")
	}
	return "", remote, "", skip("no default branch was found: %s/HEAD is not set, and there is no main or master", remote)
}

// askedDefault is the default branch remote says it has: the branch its HEAD
// names now. origin/HEAD only says what it named when the repository was cloned:
// git does not follow the remote's default when it changes, as from master to
// main.
func (t *task) askedDefault(remote string) (string, bool) {
	t.step("asking " + remote + " for its default branch")
	out, err := t.run("-c", "protocol.version=2", "ls-remote", "--symref", remote, "HEAD")
	if err != nil {
		return "", false
	}
	return core.RemoteHead(out)
}

// refspecs are what remote fetches into where.
func (t *task) refspecs(remote string) []string {
	// Absent, and an error, when the remote fetches nothing.
	out, _ := t.run("config", "--get-all", "remote."+remote+".fetch")
	return core.Lines(out)
}

// remoteHead is the branch refs/remotes/<remote>/HEAD names, as the remote
// names it -- read back through the fetch refspecs, so origin/archive/develop is
// develop when that is where they fetch it -- and the ref it names. Both are
// empty when it is not set, names a ref gone, or cannot be read back.
func (t *task) remoteHead(remote string) (string, string) {
	out, err := t.run("symbolic-ref", "--quiet", "refs/remotes/"+remote+"/HEAD")
	if err != nil {
		return "", ""
	}
	ref := strings.TrimSpace(out)
	if !t.exists(ref) {
		return "", ""
	}
	name, ok := core.Source(t.refspecs(remote), ref)
	if !ok {
		return "", ""
	}
	return name, ref
}

// trackingRef is the one remote-tracking ref remote's branch name is fetched
// into, or empty when there is none, or more than one.
func (t *task) trackingRef(remote, name string) string {
	refspecs := t.refspecs(remote)
	destinations := core.Destinations(refspecs)
	if len(destinations) == 0 {
		return ""
	}
	out, err := t.run(append([]string{"for-each-ref", "--format=%(refname)"}, destinations...)...)
	if err != nil {
		return ""
	}
	found, matches := "", 0
	for _, ref := range core.Lines(out) {
		if source, ok := core.Source(refspecs, ref); ok && source == name {
			found, matches = ref, matches+1
		}
	}
	if matches != 1 {
		return ""
	}
	return found
}

// aheadBehind counts the commits HEAD has that target has not, and those
// target has that HEAD has not.
func (t *task) aheadBehind(target string) (int, int, error) {
	out, err := t.run("rev-list", "--left-right", "--count", "HEAD..."+target)
	if err != nil {
		return 0, 0, err
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return 0, 0, errors.New("git rev-list printed a count it could not read")
	}
	ahead, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, errors.New("git rev-list printed a count it could not read")
	}
	behind, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, errors.New("git rev-list printed a count it could not read")
	}
	return ahead, behind, nil
}

// objectBytes is how much the repository's objects take.
func (t *task) objectBytes() (int64, error) {
	out, err := t.run("count-objects", "-v")
	if err != nil {
		return 0, err
	}
	return core.ObjectsBytes(out)
}

// stderr is what a failed git said.
func stderr(err error) string {
	var failure *platformgit.Error
	if errors.As(err, &failure) {
		return failure.Stderr
	}
	return ""
}

func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}
