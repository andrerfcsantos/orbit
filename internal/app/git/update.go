package git

import (
	"context"
	"fmt"
	"strings"

	core "github.com/andrerfcsantos/orbit/internal/core/git"
	"github.com/andrerfcsantos/orbit/internal/platform/logging"
	"go.uber.org/zap"
)

// UpdateOptions are how Update updates.
type UpdateOptions struct {
	// Default switches each repository to its default branch before pulling.
	Default bool
}

// Update is what updating did to a repository.
type Update struct {
	// Branch is the branch checked out once the update is done.
	Branch string `json:"branch,omitempty"`
	// From is the branch switched away from, or HEAD's commit when it was detached.
	From     string `json:"from,omitempty"`
	Switched bool   `json:"switched"`
	// Upstream is the branch pulled from.
	Upstream string `json:"upstream,omitempty"`
	// Pulled counts the commits fast-forwarded to.
	Pulled int `json:"pulled"`
	// Ahead counts the local commits the upstream does not have, and Behind those
	// of the upstream left unpulled, when the two have diverged.
	Ahead  int `json:"ahead"`
	Behind int `json:"behind"`
	// Stashed is the message of the stash holding changes orbit put away and left
	// there, for you to bring back.
	Stashed string `json:"stashed,omitempty"`
	// Restored is changes stashed for the pull, and put back after it.
	Restored bool `json:"restored"`
}

// Update fetches every remote of every repository, pruning branches deleted on
// them, and fast-forwards the branch checked out to its upstream. Changes are
// stashed for the pull, untracked files included, and put back after it, unless
// they touch a file the pull changed: then they are left in the stash rather
// than risk a conflict.
//
// With Default, each repository is first switched to its default branch, its
// changes left in a stash naming the branch they were made on.
//
// A pull is only ever a fast-forward: a branch that has diverged from its
// upstream is left as it is, as is one in the middle of a merge or a rebase.
func (s *Service) Update(ctx context.Context, repos []Repository, options UpdateOptions, progress Progress[Update]) []Result[Update] {
	logging.AddFields(ctx, zap.Bool("git.update.default", options.Default))
	results := each(ctx, s, repos, 16, progress, func(t *task) (Update, bool, error) {
		return t.update(options)
	})
	stashed := 0
	for _, result := range results {
		if result.Detail.Stashed != "" {
			stashed++
		}
	}
	logging.AddFields(ctx, zap.Int("git.update.left_stashed", stashed))
	record(ctx, "update", results, "updated", "up to date")
	return results
}

func (t *task) update(options UpdateOptions) (Update, bool, error) {
	var update Update
	t.step("fetching")
	if _, err := t.run("fetch", "--all", "--prune", "--quiet"); err != nil {
		return update, false, err
	}
	status, err := t.status()
	if err != nil {
		return update, false, err
	}
	update.Branch = status.Branch
	if status.Conflicted > 0 {
		return update, false, skip("%d %s unmerged", status.Conflicted, plural(status.Conflicted, "path is", "paths are"))
	}
	what, err := t.inProgress()
	if err != nil {
		return update, false, err
	}
	if what != "" {
		return update, false, skip("%s is in progress", what)
	}

	changed := false
	// target is the branch pulled from as it is shown, and fallback the ref that
	// names it when it is no upstream of the branch, but the default branch's.
	target, fallback := status.Upstream, ""
	if options.Default {
		t.step("finding the default branch")
		name, remote, tracking, err := t.defaultBranch()
		if err != nil {
			return update, false, err
		}
		if status.Branch != name {
			from := status.Branch
			if from == "" {
				from = short(status.Commit)
			}
			stash := ""
			if len(status.Paths) > 0 {
				t.step("stashing your changes")
				var message string
				if stash, message, err = t.stash(fmt.Sprintf("orb git update: changes on %s before switching to %s", from, name)); err != nil {
					t.halfStashed(&update, stash, message)
					return update, false, err
				}
				if stash != "" {
					update.Stashed = message
				}
			}
			t.step("switching to " + name)
			if err := t.switchTo(name, tracking); err != nil {
				// A switch can fail having switched, as when a post-checkout hook
				// fails: the changes go back only to where they were made.
				if t.stillAt(status.Branch, status.Commit) {
					if stash != "" {
						t.restore(&update, stash, update.Stashed)
					}
					return update, false, err
				}
				update.Switched, update.From, update.Branch = true, from, name
				if stash != "" {
					t.note("your changes on %s are in the stash, as %q: git stash list", from, update.Stashed)
				}
				return update, true, err
			}
			if stash != "" {
				t.note("your changes on %s are in the stash, as %q: git stash list", from, update.Stashed)
			}
			update.Switched, update.From, update.Branch = true, from, name
			changed = true
			if status, err = t.status(); err != nil {
				return update, changed, err
			}
			target = status.Upstream
		}
		if target == "" && tracking != "" {
			target, fallback = remote+"/"+name, tracking
		}
	}

	if status.Branch == "" {
		return update, changed, skip("HEAD is detached, so there is no branch to pull into")
	}
	if target == "" {
		return update, changed, skip("%s has no upstream branch to pull from", status.Branch)
	}
	// Named in full, or a tag called origin/main would be taken for the branch;
	// resolved once, so the comparison, the overlap and the pull are of one commit.
	ref := status.Branch + "@{upstream}"
	if fallback != "" {
		ref = fallback
	}
	out, err := t.run("rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return update, changed, skip("%s's upstream, %s, is gone", status.Branch, target)
	}
	commit := strings.TrimSpace(out)
	ahead, behind, err := t.aheadBehind(commit)
	if err != nil {
		return update, changed, err
	}
	update.Upstream, update.Ahead = target, ahead
	if behind == 0 {
		return update, changed, nil
	}
	if ahead > 0 {
		update.Behind = behind
		return update, changed, skip("%s has diverged from %s, with %d %s of its own and %d of %s's, so nothing was pulled",
			status.Branch, target, ahead, plural(ahead, "commit", "commits"), behind, target)
	}

	// stash is put back after the pull; kept, touching what the pull changes, is
	// left in the stash once something was pulled.
	stash, kept, message, overlaps := "", "", "", 0
	if len(status.Paths) > 0 {
		incoming, err := t.run("diff", "--name-only", "--no-renames", "-z", "HEAD", commit)
		if err != nil {
			return update, changed, err
		}
		overlaps = len(core.Overlapping(status.Paths, core.SplitNUL(incoming)))
		t.step("stashing your changes")
		var made string
		made, message, err = t.stash(fmt.Sprintf("orb git update: changes on %s before pulling %s", status.Branch, target))
		if err != nil {
			t.halfStashed(&update, made, message)
			return update, changed, err
		}
		if overlaps == 0 {
			stash = made
		} else {
			kept = made
		}
	}

	t.step(fmt.Sprintf("pulling %d %s", behind, plural(behind, "commit", "commits")))
	// git otherwise overwrites an ignored file, such as a .env the stash never
	// held, when a commit pulled starts tracking it.
	if _, err := t.run("merge", "--ff-only", "--no-overwrite-ignore", "--quiet", commit); err != nil {
		// A merge can fail having fast-forwarded, as when it is stopped in a
		// post-merge hook. Changes that touch nothing pulled go back either way;
		// those that do only if nothing was pulled after all.
		pulled := !t.stillAt(status.Branch, status.Commit)
		if pulled {
			update.Pulled = behind
			changed = true
		}
		if stash != "" {
			t.restore(&update, stash, message)
		}
		if kept != "" && !pulled {
			t.restore(&update, kept, message)
		} else if kept != "" {
			update.Stashed = message
			t.note("your changes touch %d %s the pull changed, so they are left in the stash, as %q: git stash pop brings them back",
				overlaps, plural(overlaps, "file", "files"), message)
		}
		return update, changed, err
	}
	update.Pulled = behind
	if kept != "" {
		update.Stashed = message
		t.note("your changes touch %d %s the pull changed, so they are left in the stash, as %q: git stash pop brings them back",
			overlaps, plural(overlaps, "file", "files"), message)
	}
	if stash != "" {
		t.step("restoring your changes")
		t.restore(&update, stash, message)
	}
	return update, true, nil
}

// halfStashed says where changes are that git saved in a stash but could not
// all put away: in the stash, and some still in the working tree, which nothing
// more is done to.
func (t *task) halfStashed(update *Update, stash, message string) {
	if stash == "" {
		return
	}
	update.Stashed = message
	t.note("your changes were saved in the stash, as %q, but git could not put them all away, so nothing more was done: git stash list", message)
}

// restore pops the stash the update made, or says where the changes are when it
// cannot.
func (t *task) restore(update *Update, stash, message string) {
	if err := t.pop(stash); err != nil {
		update.Stashed = message
		t.note("your changes could not be put back, so they are left in the stash, as %q: %v", message, err)
		return
	}
	update.Stashed = ""
	update.Restored = true
}

// stillAt reports whether HEAD is still the branch -- none when it was detached
// -- and the commit the work began on. It is asked even of work that was
// stopped; one that cannot be told reads as moved, which keeps a stash rather
// than apply it to the wrong branch, or onto commits it was not made on.
func (t *task) stillAt(branch, commit string) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.ctx), restoreTimeout)
	defer cancel()
	out, err := t.git.Run(ctx, t.repo.Path, "symbolic-ref", "--quiet", "--short", "HEAD")
	if branch != "" && (err != nil || strings.TrimSpace(out) != branch) || branch == "" && err == nil {
		return false
	}
	out, err = t.git.Run(ctx, t.repo.Path, "rev-parse", "--verify", "--quiet", "HEAD")
	return err == nil && strings.TrimSpace(out) == commit
}

// switchTo checks out the branch name, creating it to track the remote-tracking
// ref tracking when there is no local one yet. An ignored file the branch tracks
// stops the switch rather than being overwritten.
func (t *task) switchTo(name, tracking string) error {
	if t.exists("refs/heads/" + name) {
		_, err := t.run("switch", "--quiet", "--no-overwrite-ignore", name)
		return err
	}
	if tracking != "" && t.exists(tracking) {
		_, err := t.run("switch", "--quiet", "--no-overwrite-ignore", "--create", name, "--track", tracking)
		return err
	}
	return fmt.Errorf("there is no branch %s to switch to", name)
}
