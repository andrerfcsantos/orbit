package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	core "github.com/andrerfcsantos/orbit/internal/core/git"
	platformgit "github.com/andrerfcsantos/orbit/internal/platform/git"
	"github.com/andrerfcsantos/orbit/internal/platform/logging"
	"go.uber.org/zap"
)

// BranchOptions are which branches StaleBranches finds.
type BranchOptions struct {
	// OlderThan is how long ago a branch's tip must have been committed.
	OlderThan core.Age
	// Remote finds the remote's branches too, on origin (or a repository's only
	// remote), fetched first so they are as the remote has them now.
	Remote bool
}

// Branch is a branch to delete, and whether it was.
type Branch struct {
	Name string `json:"name"`
	// Remote is the remote the branch is on, empty for a local branch.
	Remote string `json:"remote,omitempty"`
	// Tracking is the remote-tracking ref a remote branch was read from:
	// refs/remotes/origin/<name> with the usual fetch refspec.
	Tracking string `json:"tracking,omitempty"`
	// Commit is the branch's tip, which recreates it: git branch <name> <commit>.
	Commit string `json:"commit"`
	// Date is when the tip was committed.
	Date    time.Time `json:"date"`
	Deleted bool      `json:"deleted"`
	Error   string    `json:"error,omitempty"`
}

// Branches are a repository's branches to delete.
type Branches struct {
	Local  []Branch `json:"local"`
	Remote []Branch `json:"remote,omitempty"`

	// pushURL is where the remote branches were read from and are pushed to,
	// held to check the remote still is that one before deleting. Not in the JSON:
	// a URL can hold a credential.
	pushURL string
}

// Total counts the branches.
func (b Branches) Total() int { return len(b.Local) + len(b.Remote) }

// protected are never deleted, whatever their age, beside a repository's own
// default branch.
var protected = []string{"main", "master"}

// StaleBranches finds every repository's branches whose tip is older than
// options say. The branch checked out, the one checked out in any other worktree,
// the default branch, main and master are never among them, nor on the remote the
// default branch, main, master and the upstream of any local branch kept.
func (s *Service) StaleBranches(ctx context.Context, repos []Repository, options BranchOptions, progress Progress[Branches]) []Result[Branches] {
	logging.AddFields(ctx, zap.Bool("git.branches.remote", options.Remote))
	cutoff := options.OlderThan.Cutoff(s.now())
	parallel := 32
	if options.Remote {
		parallel = 16
	}
	// Worktrees of one repository share its branches, local and remote: they are
	// planned once, with the first, or both would delete them.
	first := make(map[string]Repository)
	for _, repo := range repos {
		if _, ok := first[repo.group()]; !ok {
			first[repo.group()] = repo
		}
	}
	results := each(ctx, s, repos, parallel, progress, func(t *task) (Branches, bool, error) {
		if owner := first[t.repo.group()]; owner.Path != t.repo.Path {
			t.note("its branches are %s's, a worktree of the same repository, and are listed there", owner.Name)
			return Branches{}, false, nil
		}
		branches, err := t.staleBranches(cutoff, options.Remote)
		return branches, branches.Total() > 0, err
	})
	local, remote := 0, 0
	for _, result := range results {
		local += len(result.Detail.Local)
		remote += len(result.Detail.Remote)
	}
	logging.AddFields(ctx, zap.Int("git.branches.found_local", local), zap.Int("git.branches.found_remote", remote))
	record(ctx, "branch-plan", results, "found old branches in", "with none")
	return results
}

func (t *task) staleBranches(cutoff time.Time, remote bool) (Branches, error) {
	var branches Branches
	remoteName := ""
	if remote {
		var err error
		if remoteName, err = t.primaryRemote(); err != nil {
			return branches, err
		}
		if remoteName == "" {
			t.note("there is no origin to delete remote branches from")
		} else {
			t.step("fetching " + remoteName)
			if _, err := t.run("fetch", "--prune", "--quiet", remoteName); err != nil {
				return branches, err
			}
		}
	}

	t.step("reading its branches")
	keep := make(map[string]bool)
	for _, name := range protected {
		keep[name] = true
	}
	// Asked of the remote itself, when it is to be deleted from too, the default
	// branch is what no fetch mapping or stale origin/HEAD can misname; a remote
	// that cannot say keeps every branch of its own.
	asked := false
	aliases := make(map[string]bool)
	var listed remoteRefs
	if remoteName != "" {
		var err error
		listed, err = t.remoteRefs(remoteName)
		if name, ok := strings.CutPrefix(listed.symrefs["HEAD"], "refs/heads/"); err == nil && ok && name != "" {
			keep[name], asked = true, true
			for ref := range listed.symrefs {
				if alias, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
					aliases[alias] = true
				}
			}
			if !listed.v2 {
				t.note("%s speaks only git's protocol v0, which does not say which of its branches are aliases, so none of its branches are deleted", remoteName)
				remoteName = ""
			}
		} else {
			t.note("%s did not say what its default branch is, so none of its branches are deleted", remoteName)
			remoteName = ""
		}
	}
	if !asked {
		name, err := t.knownDefault()
		if err != nil {
			return branches, err
		}
		keep[name] = true
	}
	// What is read is fetched; what is deleted is pushed. Unless the two are one
	// repository, the branches deleted would be ones nothing here has seen.
	if remoteName != "" {
		if branches.pushURL = t.pushURL(remoteName); branches.pushURL == "" {
			t.note("%s pushes somewhere other than it fetches from, so none of its branches are deleted", remoteName)
			remoteName = ""
		}
	}
	keepLocal, err := t.checkedOut()
	if err != nil {
		return branches, err
	}

	// The remote's branches are read where its refspecs fetch them into, which
	// need not be refs/remotes/<remote>.
	patterns := []string{"refs/heads"}
	var refspecs []string
	if remoteName != "" {
		refspecs = t.refspecs(remoteName)
		patterns = append(patterns, core.Destinations(refspecs)...)
	}
	out, err := t.run(append([]string{"for-each-ref", "--format=" + core.RefFormat}, patterns...)...)
	if err != nil {
		return branches, err
	}
	refs, err := core.ParseRefs(out)
	if err != nil {
		return branches, err
	}
	// A branch another branch is an alias of is kept: deleting it would leave the
	// alias -- main, perhaps -- naming nothing.
	for _, ref := range refs {
		if target, ok := strings.CutPrefix(ref.Symref, "refs/heads/"); ok && strings.HasPrefix(ref.Ref, "refs/heads/") {
			keepLocal[target] = true
		}
	}
	keepsLocal := func(ref core.Ref) bool {
		name, ok := strings.CutPrefix(ref.Ref, "refs/heads/")
		return ok && (keep[name] || keepLocal[name])
	}
	// A remote branch a kept local branch tracks is kept too: it may be the pull
	// request of work that is newer than its last push. It is known by its name
	// on the remote, however many places it is fetched into.
	tracked := make(map[string]bool)
	for _, ref := range refs {
		if strings.HasPrefix(ref.Ref, "refs/heads/") && (keepsLocal(ref) || !ref.Date.Before(cutoff)) {
			if name, ok := trackedOn(ref, remoteName); ok {
				tracked[name] = true
			}
		}
	}

	// A remote-tracking ref is named by the branch it was fetched from, through
	// the remote's refspecs; one fetched from anything else -- a pull request's
	// refs/pull/12/head -- is no branch to delete.
	sources := make(map[string]string)
	for _, ref := range refs {
		if !strings.HasPrefix(ref.Ref, "refs/heads/") {
			if name, ok := core.Source(refspecs, ref.Ref); ok {
				sources[ref.Ref] = name
			}
		}
	}
	stale := core.Stale(refs, cutoff, func(ref core.Ref) bool {
		if strings.HasPrefix(ref.Ref, "refs/heads/") {
			return keepsLocal(ref)
		}
		name, ok := sources[ref.Ref]
		return !ok || name == "HEAD" || keep[name] || tracked[name] || aliases[name]
	})
	// One branch fetched into two places is planned once, and not at all when
	// the two disagree on where it is: one of them is stale, and which is unknown.
	remoteAt := make(map[string]int)
	disagree := make(map[string]bool)
	for _, ref := range stale {
		if name, ok := strings.CutPrefix(ref.Ref, "refs/heads/"); ok {
			branches.Local = append(branches.Local, Branch{Name: name, Commit: ref.Commit, Date: ref.Date})
			continue
		}
		name := sources[ref.Ref]
		if at, seen := remoteAt[name]; seen {
			disagree[name] = disagree[name] || branches.Remote[at].Commit != ref.Commit
			continue
		}
		remoteAt[name] = len(branches.Remote)
		branches.Remote = append(branches.Remote, Branch{Name: name, Remote: remoteName, Tracking: ref.Ref, Commit: ref.Commit, Date: ref.Date})
	}
	branches.Remote = slices.DeleteFunc(branches.Remote, func(branch Branch) bool { return disagree[branch.Name] })
	shadowed := core.Shadowed(listed.tips, names(branches.Remote))
	branches.Remote = slices.DeleteFunc(branches.Remote, func(branch Branch) bool { return shadowed[branch.Name] })
	oldest := func(a, b Branch) int { return a.Date.Compare(b.Date) }
	slices.SortStableFunc(branches.Local, oldest)
	slices.SortStableFunc(branches.Remote, oldest)
	return branches, nil
}

// DeleteBranches deletes the branches StaleBranches found. A branch is deleted
// only if it still points where it did when it was found, locally and on the
// remote, so one somebody has committed to since is left alone. A local branch
// is deleted whether or not it was merged: its commit, in its Branch, recreates
// it. Pushing the deletions runs no pre-push hook. There is a result for every
// plan: one with nothing to delete, or that failed, is returned as it was.
func (s *Service) DeleteBranches(ctx context.Context, plans []Result[Branches], progress Progress[Branches]) []Result[Branches] {
	var repos []Repository
	planned := make(map[string]Branches)
	for _, plan := range plans {
		if plan.Outcome == OutcomeChanged && plan.Detail.Total() > 0 {
			repos = append(repos, plan.Repository)
			planned[plan.Repository.Path] = Branches{
				Local:   slices.Clone(plan.Detail.Local),
				Remote:  slices.Clone(plan.Detail.Remote),
				pushURL: plan.Detail.pushURL,
			}
		}
	}
	results := each(ctx, s, repos, 16, progress, func(t *task) (Branches, bool, error) {
		branches := planned[t.repo.Path]
		if len(branches.Local) > 0 {
			t.step(fmt.Sprintf("deleting %d local %s", len(branches.Local), plural(len(branches.Local), "branch", "branches")))
			if err := t.deleteLocal(branches.Local); err != nil {
				return branches, false, err
			}
		}
		if len(branches.Remote) > 0 {
			t.step(fmt.Sprintf("deleting %d %s on %s", len(branches.Remote), plural(len(branches.Remote), "branch", "branches"), branches.Remote[0].Remote))
			if err := t.deleteRemote(branches.Remote, branches.pushURL); err != nil {
				return branches, false, err
			}
		}
		deleted, failed := 0, 0
		for _, branch := range slices.Concat(branches.Local, branches.Remote) {
			if branch.Deleted {
				deleted++
				continue
			}
			failed++
			name := branch.Name
			if branch.Remote != "" {
				name = branch.Remote + "/" + name
			}
			t.note("%s was not deleted: %s", name, branch.Error)
		}
		if failed > 0 {
			return branches, deleted > 0, fmt.Errorf("%d of %d %s not deleted", failed, branches.Total(), plural(branches.Total(), "branch was", "branches were"))
		}
		return branches, true, nil
	})
	results = alongside(plans, results)
	deleted := 0
	for _, result := range results {
		for _, branch := range slices.Concat(result.Detail.Local, result.Detail.Remote) {
			if branch.Deleted {
				deleted++
			}
		}
	}
	logging.AddFields(ctx, zap.Int("git.branches.deleted", deleted))
	record(ctx, "branch-delete", results, "deleted old branches in", "with none")
	return results
}

// remoteRefs are the remote's refs as it lists them now. A fetch copies an
// alias as a branch of its own, and deleting one on the remote deletes the ref
// it names.
type remoteRefs struct {
	// symrefs are the aliases, HEAD's and any branch's, by name.
	symrefs map[string]string
	// tips are every ref's object, by name.
	tips map[string]string
	// v2 is a server that answered in protocol v2, which names every alias and
	// its target, a target it hides included. In v0 only HEAD is said to be one,
	// and an alias of a ref the server hides shows no sign of it, so no branch of
	// a v0 server is deleted.
	v2 bool
}

func (t *task) remoteRefs(remote string) (remoteRefs, error) {
	// Every ref, not just the branches: an alias can name a tag as well. v2 is
	// asked for whatever the repository's protocol.version says; a server option
	// is what proves it was spoken, since a v0 server refuses one, where asking
	// for v2 alone falls back to v0 unsaid.
	out, err := t.run("-c", "protocol.version=2", "ls-remote", "--server-option=orbit", "--symref", remote)
	v2 := err == nil
	if err != nil && strings.Contains(stderr(err), "protocol version 2") {
		out, err = t.run("ls-remote", "--symref", remote)
	}
	if err != nil {
		return remoteRefs{}, err
	}
	return remoteRefs{symrefs: core.RemoteSymrefs(out), tips: core.RemoteTips(out), v2: v2}, nil
}

// names are the branches'.
func names(branches []Branch) []string {
	names := make([]string, len(branches))
	for i, branch := range branches {
		names[i] = branch.Name
	}
	return names
}

// pushURL is the one URL remote both fetches from and pushes to, or empty when
// it pushes anywhere else, or to more than one.
func (t *task) pushURL(remote string) string {
	fetched, fetchErr := t.run("remote", "get-url", "--all", remote)
	pushed, pushErr := t.run("remote", "get-url", "--push", "--all", remote)
	urls := core.Records(pushed)
	if fetchErr != nil || pushErr != nil || len(urls) != 1 || urls[0] == "" || !slices.Equal(core.Records(fetched), urls) {
		return ""
	}
	return urls[0]
}

// knownDefault is the repository's default branch as deleting its branches must
// know it: the branch the remote says is its default; when it cannot be asked,
// the one origin/HEAD names, as last recorded; with no remote, main or master. A
// guess -- main beside a develop that is the default -- could delete the default
// branch, so a default that cannot be known leaves the repository alone.
func (t *task) knownDefault() (string, error) {
	remote, err := t.primaryRemote()
	if err != nil {
		return "", err
	}
	if remote == "" {
		out, err := t.run("remote")
		if err != nil {
			return "", err
		}
		if len(core.Lines(out)) == 0 {
			for _, name := range protected {
				if t.exists("refs/heads/" + name) {
					return name, nil
				}
			}
		}
		return "", skip("which branch is the default is not known, so none of its branches are deleted")
	}
	if name, ok := t.askedDefault(remote); ok {
		return name, nil
	}
	if name, _ := t.remoteHead(remote); name != "" {
		return name, nil
	}
	return "", skip("%s could not say what its default branch is and %s/HEAD is not set, so none of its branches are deleted", remote, remote)
}

// trackedOn is the branch on remote that a local branch's upstream is.
func trackedOn(local core.Ref, remote string) (string, bool) {
	if remote == "" || local.UpstreamRemote != remote {
		return "", false
	}
	name, ok := strings.CutPrefix(local.UpstreamRef, "refs/heads/")
	return name, ok && name != ""
}

// localBranches reads the local branches, by name.
func (t *task) localBranches() (map[string]core.Ref, error) {
	out, err := t.run("for-each-ref", "--format="+core.RefFormat, "refs/heads")
	if err != nil {
		return nil, err
	}
	refs, err := core.ParseRefs(out)
	if err != nil {
		return nil, err
	}
	branches := make(map[string]core.Ref, len(refs))
	for _, ref := range refs {
		branches[strings.TrimPrefix(ref.Ref, "refs/heads/")] = ref
	}
	return branches, nil
}

// checkedOut are the branches any of the repository's worktrees, this one
// included, has checked out, or is rebasing or bisecting: a worktree in the
// middle of a rebase reports its HEAD as detached, yet finishing the rebase
// writes the branch, as git branch -D knows.
func (t *task) checkedOut() (map[string]bool, error) {
	out, err := t.run("worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	branches := make(map[string]bool)
	for _, line := range core.Lines(out) {
		if name, ok := strings.CutPrefix(line, "branch refs/heads/"); ok {
			branches[name] = true
		}
	}
	common := platformgit.CommonDir(t.repo.Path)
	gitDirs := []string{common}
	if linked, err := os.ReadDir(filepath.Join(common, "worktrees")); err == nil {
		for _, entry := range linked {
			gitDirs = append(gitDirs, filepath.Join(common, "worktrees", entry.Name()))
		}
	}
	for _, gitDir := range gitDirs {
		for _, state := range []string{"rebase-merge/head-name", "rebase-apply/head-name", "BISECT_START"} {
			data, err := os.ReadFile(filepath.Join(gitDir, state))
			if err != nil {
				continue
			}
			if name := strings.TrimPrefix(strings.TrimSpace(string(data)), "refs/heads/"); name != "" {
				branches[name] = true
			}
		}
		// A rebase with --update-refs rewrites the branches stacked on the one
		// it rebases, listed with their commits before and after.
		if data, err := os.ReadFile(filepath.Join(gitDir, "rebase-merge", "update-refs")); err == nil {
			for _, line := range core.Lines(string(data)) {
				if name, ok := strings.CutPrefix(line, "refs/heads/"); ok {
					branches[name] = true
				}
			}
		}
	}
	return branches, nil
}

// deleteLocal deletes each branch only if it still points at the commit it was
// found at, in one step with the check, so a commit made meanwhile by any git
// keeps it. update-ref does that, but neither spares a branch checked out nor
// removes its configuration, as git branch -D would, so both are done here.
func (t *task) deleteLocal(branches []Branch) error {
	tips, err := t.localBranches()
	if err != nil {
		return err
	}
	out, err := t.checkedOut()
	if err != nil {
		return err
	}
	// Read again too: an alias made since the plan would be left naming nothing.
	for _, ref := range tips {
		if target, ok := strings.CutPrefix(ref.Symref, "refs/heads/"); ok {
			out[target] = true
		}
	}
	for i := range branches {
		branch := &branches[i]
		tip, ok := tips[branch.Name]
		switch {
		case !ok:
			branch.Deleted = true
			continue
		case out[branch.Name]:
			branch.Error = "it is checked out, or another branch is an alias of it"
			continue
		case tip.Commit != branch.Commit:
			branch.Error = "it has moved since it was found"
			continue
		}
		// --no-deref, or an alias would delete the branch it names.
		if _, err := t.run("update-ref", "--no-deref", "-d", "refs/heads/"+branch.Name, branch.Commit); err != nil {
			branch.Error = "it has moved since it was found"
			if !strings.Contains(stderr(err), "but expected") {
				branch.Error = err.Error()
			}
			continue
		}
		branch.Deleted = true
		// Absent when the branch had no configuration, which is no failure.
		_, _ = t.run("config", "--remove-section", "branch."+branch.Name)
	}
	return nil
}

// deleteRemote deletes the branches on their remote, but for any a local branch
// tracks: one kept as the local deletion ran, because it had moved, is somebody's
// work in progress.
func (t *task) deleteRemote(branches []Branch, url string) error {
	remote := branches[0].Remote
	// The remote must still be the one its branches were read from: one whose
	// pushurl was changed meanwhile would be another repository, with the same
	// commits perhaps, whose branches nothing here has seen.
	if url == "" || t.pushURL(remote) != url {
		for i := range branches {
			branches[i].Error = remote + "'s URL changed since its branches were listed"
		}
		return nil
	}
	locals, err := t.localBranches()
	if err != nil {
		return err
	}
	tracked := make(map[string]bool)
	for _, local := range locals {
		if name, ok := trackedOn(local, remote); ok {
			tracked[name] = true
		}
	}
	// Asked again: an alias made since the plan would take its branch with it.
	listed, err := t.remoteRefs(remote)
	if err != nil || !listed.v2 {
		for i := range branches {
			branches[i].Error = remote + " could not say which of its branches are aliases"
		}
		return nil
	}
	var candidates []*Branch
	for i := range branches {
		branch := &branches[i]
		switch target := listed.symrefs["refs/heads/"+branch.Name]; {
		case tracked[branch.Name]:
			branch.Error = "a local branch kept tracks it"
		case target != "":
			branch.Error = "it is an alias of " + strings.TrimPrefix(target, "refs/heads/") + " on " + remote
		default:
			candidates = append(candidates, branch)
		}
	}
	deleting := make([]string, len(candidates))
	for i, branch := range candidates {
		deleting[i] = branch.Name
	}
	shadowed := core.Shadowed(listed.tips, deleting)
	var pushing []*Branch
	for _, branch := range candidates {
		if shadowed[branch.Name] {
			branch.Error = "a ref kept on " + remote + " is at its commit, so it may be that ref's alias"
			continue
		}
		pushing = append(pushing, branch)
	}
	lease := func(branch *Branch) string {
		return "--force-with-lease=refs/heads/" + branch.Name + ":" + branch.Commit
	}
	deletion := func(branch *Branch) string { return ":refs/heads/" + branch.Name }
	for _, batch := range chunks(pushing, func(branch *Branch) int { return len(lease(branch)) + len(deletion(branch)) + 2 }) {
		if t.pushURL(remote) != url {
			for _, branch := range batch {
				branch.Error = remote + "'s URL changed since its branches were listed"
			}
			continue
		}
		args := []string{"push", "--porcelain", "--no-verify"}
		for _, branch := range batch {
			args = append(args, lease(branch))
		}
		args = append(args, remote)
		for _, branch := range batch {
			args = append(args, deletion(branch))
		}
		out, err := t.run(args...)
		pushed := core.ParsePush(out)
		for _, branch := range batch {
			switch result, ok := pushed["refs/heads/"+branch.Name]; {
			case ok && result.Done:
				branch.Deleted = true
			case ok:
				branch.Error = result.Reason
			case err != nil:
				branch.Error = err.Error()
			default:
				branch.Error = "git did not say it was deleted"
			}
		}
	}
	return nil
}
