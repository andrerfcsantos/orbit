package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	core "github.com/andrerfcsantos/orbit/internal/core/git"
	"github.com/andrerfcsantos/orbit/internal/platform/logging"
	"go.uber.org/zap"
)

// ReleaseRepository is the repository a release is tagged in, before anything
// is fetched: enough to ask what is missing.
type ReleaseRepository struct {
	Repository Repository `json:"repository"`
	// Branch is the branch checked out, empty when HEAD is detached.
	Branch string `json:"branch,omitempty"`
}

// LocateRelease finds the repository path is, or is inside, and the branch
// checked out. Its errors name the path by its place alone.
func (s *Service) LocateRelease(ctx context.Context, path string) (ReleaseRepository, error) {
	if !filepath.IsAbs(path) && s.WorkDir != "" {
		path = filepath.Join(s.WorkDir, path)
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return ReleaseRepository{}, errors.New("the path given is not a directory")
	}
	// The repository the path is in, as git run there finds it: not one beneath
	// it, such as a submodule, which Discover would prefer.
	out, err := s.Git.Climb(ctx, path, "rev-parse", "--show-toplevel")
	// Only the newline git ends it with: a directory's name can end in a space.
	top := strings.TrimSuffix(strings.TrimSuffix(out, "\n"), "\r")
	if err != nil || top == "" {
		return ReleaseRepository{}, errors.New("the path given is in no git repository")
	}
	repos, err := s.Discover(ctx, []string{top})
	if err != nil {
		return ReleaseRepository{}, err
	}
	switch len(repos) {
	case 0:
		return ReleaseRepository{}, errors.New("there is no git repository there")
	case 1:
	default:
		return ReleaseRepository{}, fmt.Errorf("there are %d repositories there: give the one to tag", len(repos))
	}
	// The full ref, as --short would say heads/main beside a tag named main.
	ref, _ := s.task(ctx, repos[0]).run("symbolic-ref", "--quiet", "HEAD")
	branch, _ := strings.CutPrefix(strings.TrimSpace(ref), "refs/heads/")
	return ReleaseRepository{Repository: repos[0], Branch: branch}, nil
}

// DefaultBranch is the repository's default branch, as update --default finds
// it, or empty when there is none to be found.
func (s *Service) DefaultBranch(ctx context.Context, repo Repository) string {
	ctx, cancel := s.bounded(ctx)
	defer cancel()
	name, _, _, err := s.task(ctx, repo).defaultBranch()
	if err != nil {
		return ""
	}
	return name
}

// ReleaseOptions say what a release is tagged from.
type ReleaseOptions struct {
	// Remote is where the branch is read from and the tag pushed to: origin, or
	// the only remote, when empty.
	Remote string
	// Branch is whose tip, as the remote has it, is tagged: the branch checked
	// out when empty.
	Branch string
	// Prefix is the tags' prefix when PrefixSet; otherwise the latest tag's is
	// followed.
	Prefix    string
	PrefixSet bool
	// Push is whether the tag is to be pushed: then only the tags the remote has
	// count as releases made.
	Push bool
}

// ReleaseState is a repository read for a release: the commit to tag and the
// tags a version is chosen from.
type ReleaseState struct {
	Repository Repository `json:"repository"`
	Remote     string     `json:"remote,omitempty"`
	Branch     string     `json:"branch"`
	// Ref is what is tagged: the remote's branch, or the local one when there is
	// no remote.
	Ref     string `json:"ref"`
	Commit  string `json:"commit"`
	Subject string `json:"subject"`
	// Prefix is the series' prefix, and Followed whether it is the repository's
	// latest tag's rather than one given.
	Prefix   string `json:"prefix"`
	Followed bool   `json:"prefix_followed"`
	// Convention is the latest tag the prefix was followed from.
	Convention string `json:"convention,omitempty"`
	// Unpushed counts the commits on the local branch the remote's has not, which
	// the release leaves out.
	Unpushed int `json:"unpushed,omitempty"`

	releases core.Releases
	// located and options are what it was read from, for Recheck.
	located ReleaseRepository
	options ReleaseOptions
}

// Latest is the release the next one follows, if there is one.
func (st *ReleaseState) Latest() (core.Known, bool) { return st.releases.Latest() }

// InProgress are the pre-releases under way, highest first, as Releases has them.
func (st *ReleaseState) InProgress() []core.Known { return st.releases.InProgress() }

// ReleasePlan is the tag a choice makes, and what to know about it first.
type ReleasePlan struct {
	core.Planned
	Choice core.Choice `json:"choice"`
	// Warnings are reasons the tag may not do what is meant, such as commits
	// left out of it.
	Warnings []string `json:"warnings,omitempty"`
}

// Plan is the tag a choice makes in the state read.
func (st *ReleaseState) Plan(choice core.Choice) (ReleasePlan, error) {
	planned, err := st.releases.Next(choice)
	if err != nil {
		return ReleasePlan{}, err
	}
	plan := ReleasePlan{Planned: planned, Choice: choice}
	if st.Unpushed > 0 {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("%d %s on your local %s %s not on %s, and %s left out",
			st.Unpushed, plural(st.Unpushed, "commit", "commits"), st.Branch, plural(st.Unpushed, "is", "are"), st.Remote,
			plural(st.Unpushed, "is", "are")))
	}
	return plan, nil
}

// InspectRelease fetches the branch and the tags, and reads what a release of
// the branch is made from.
func (s *Service) InspectRelease(ctx context.Context, repo ReleaseRepository, options ReleaseOptions) (*ReleaseState, error) {
	logging.AddFields(ctx, zap.String("git.operation", "release-tag"))
	st, why, err := s.inspectRelease(ctx, repo, options)
	if err != nil {
		return nil, refused(ctx, why, err)
	}
	return st, nil
}

// PlanRelease is the tag a choice makes in the state read, as Plan is, with
// why there is none recorded.
func (s *Service) PlanRelease(ctx context.Context, st *ReleaseState, choice core.Choice) (ReleasePlan, error) {
	plan, err := st.Plan(choice)
	if err != nil {
		return plan, refused(ctx, "the version chosen cannot be released", err)
	}
	return plan, nil
}

// refused records why no tag was made, in words free of the values given: the
// error, which names them, is the person's to read.
func refused(ctx context.Context, why string, err error) error {
	logging.Summarize(ctx, "no release tag was made: "+why)
	return err
}

func (s *Service) inspectRelease(ctx context.Context, repo ReleaseRepository, options ReleaseOptions) (*ReleaseState, string, error) {
	ctx, cancel := s.bounded(ctx)
	defer cancel()
	t := s.task(ctx, repo.Repository)
	st := &ReleaseState{Repository: repo.Repository, Remote: options.Remote, Branch: options.Branch, located: repo, options: options}
	if st.Remote == "" {
		remote, err := t.primaryRemote()
		if err != nil {
			return nil, "the remotes could not be read", fmt.Errorf("reading the remotes: %w", err)
		}
		st.Remote = remote
	}
	if st.Branch == "" {
		st.Branch = repo.Branch
	}
	if st.Branch == "" {
		return nil, "HEAD is detached", errors.New("HEAD is detached: give the branch to release")
	}
	if out, err := t.run("check-ref-format", "--branch", st.Branch); err != nil || strings.TrimSpace(out) == "" {
		return nil, "the branch is not a branch name", errors.New("the branch given is not a branch name")
	}

	published := map[string]bool{}
	if st.Remote == "" {
		if remotes, _ := t.run("remote"); len(core.Lines(remotes)) > 1 {
			return nil, "no remote was chosen", errors.New("the repository has several remotes, none of them origin: give the one with --remote")
		}
		if options.Push {
			return nil, "there is no remote", errors.New("the repository has no remote to push the tag to: give --push=false to tag it only here")
		}
		st.Ref = "refs/heads/" + st.Branch
	} else {
		if _, err := t.run("remote", "get-url", st.Remote); err != nil {
			return nil, "there is no such remote", fmt.Errorf("the repository has no remote %s", st.Remote)
		}
		// The tags are read where the remote fetches from, and must be where the
		// tag is pushed, or the checks on them check another repository.
		if options.Push && t.pushURL(st.Remote) == "" {
			return nil, "the remote pushes elsewhere than it fetches from", fmt.Errorf(
				"%s pushes somewhere other than it fetches from, or to several places, so its tags cannot be checked: "+
					"give --remote one that pushes where it fetches", st.Remote)
		}
		st.Ref = "refs/remotes/" + st.Remote + "/" + st.Branch
		// The tags too, so one made since the last fetch decides the version, and
		// none is made twice.
		// Never pruned, whatever fetch.prune says: a tag never pushed must stay to
		// be found.
		if _, err := t.run("fetch", "--tags", "--no-prune", "--no-prune-tags", "--no-recurse-submodules", st.Remote,
			"+refs/heads/"+st.Branch+":"+st.Ref); err != nil {
			if strings.Contains(stderr(err), "couldn't find remote ref") {
				return nil, "the remote has no such branch", fmt.Errorf("%s has no branch %s: push it first", st.Remote, st.Branch)
			}
			return nil, "fetching failed", fmt.Errorf("fetching %s from %s: %w", st.Branch, st.Remote, err)
		}
		out, err := t.run("ls-remote", "--tags", "--refs", st.Remote)
		if err != nil {
			return nil, "the remote's tags could not be listed", fmt.Errorf("listing %s's tags: %w", st.Remote, err)
		}
		for _, line := range core.Lines(out) {
			if _, ref, ok := strings.Cut(line, "\t"); ok {
				published[strings.TrimPrefix(ref, "refs/tags/")] = true
			}
		}
	}

	// A shallow clone hides the tags beneath its boundary from --merged, so the
	// latest release would seem not to be on the branch.
	if out, err := t.run("rev-parse", "--is-shallow-repository"); err == nil && strings.TrimSpace(out) == "true" {
		return nil, "the clone is shallow", errors.New(
			"the clone is shallow, so the releases on the branch cannot all be seen: git fetch --unshallow fetches the rest")
	}
	commit, err := t.run("rev-parse", "--verify", "--quiet", st.Ref+"^{commit}")
	if err != nil {
		return nil, "there is no such branch", fmt.Errorf("there is no branch %s", st.Branch)
	}
	st.Commit = strings.TrimSpace(commit)
	subject, _ := t.run("log", "-1", "--format=%s", st.Commit)
	st.Subject = strings.TrimSpace(subject)
	if st.Remote != "" && t.exists("refs/heads/"+st.Branch) {
		if out, err := t.run("rev-list", "--count", st.Ref+"..refs/heads/"+st.Branch); err == nil {
			st.Unpushed, _ = strconv.Atoi(strings.TrimSpace(out))
		}
	}

	tags, err := t.versionTags(st.Commit, published)
	if err != nil {
		return nil, "the tags could not be listed", err
	}
	st.Prefix, st.Followed = options.Prefix, !options.PrefixSet
	if st.Followed {
		var latest *core.Known
		st.Prefix, latest = core.Convention(tags, options.Push)
		if latest != nil {
			st.Convention = latest.Name
		}
	}
	st.releases = core.Releases{Prefix: st.Prefix, Tags: core.Series(tags, st.Prefix), Commit: st.Commit, PublishedOnly: options.Push}

	return st, "", nil
}

// versionTags are the repository's tags that are versions, each with whether
// commit contains it and the remote has it.
func (t *task) versionTags(commit string, published map[string]bool) ([]core.Known, error) {
	out, err := t.run("for-each-ref",
		"--format=%(refname:strip=2)%09%(objectname)%09%(*objectname)%09%(*objecttype)%09%(creatordate:unix)", "refs/tags")
	if err != nil {
		return nil, fmt.Errorf("listing the tags: %w", err)
	}
	merged, err := t.run("for-each-ref", "--merged", commit, "--format=%(refname:strip=2)", "refs/tags")
	if err != nil {
		return nil, fmt.Errorf("listing the tags on the branch: %w", err)
	}
	onBranch := map[string]bool{}
	for _, name := range core.Lines(merged) {
		onBranch[name] = true
	}
	var tags []core.Known
	for _, line := range core.Lines(out) {
		fields := strings.Split(line, "\t")
		if len(fields) != 5 {
			continue
		}
		tag, ok := core.ParseTag(fields[0])
		if !ok {
			continue
		}
		// An annotated tag's commit is the object it peels to; * peels once on
		// older gits, so a tag of a tag is peeled the rest of the way.
		target := fields[2]
		if target == "" {
			target = fields[1]
		}
		if fields[3] == "tag" {
			peeled, err := t.run("rev-parse", "--verify", "--quiet", "refs/tags/"+fields[0]+"^{commit}")
			if err != nil {
				continue
			}
			target = strings.TrimSpace(peeled)
		}
		seconds, _ := strconv.ParseInt(fields[4], 10, 64)
		tags = append(tags, core.Known{Tag: tag, Commit: target, Created: time.Unix(seconds, 0),
			Merged: onBranch[fields[0]], Published: published[fields[0]]})
	}
	return tags, nil
}

// Release is a release tag made, or one a dry run would make.
type Release struct {
	*ReleaseState
	ReleasePlan
	// Created is whether the tag was made, and Pushed whether the remote has it.
	Created bool `json:"created"`
	Pushed  bool `json:"pushed"`
}

// CreateRelease tags the commit with the plan's tag, annotated with message,
// and pushes it unless push is false. A tag whose push failed is deleted again,
// so trying again starts afresh.
func (s *Service) CreateRelease(ctx context.Context, st *ReleaseState, plan ReleasePlan, message string, push bool) (Release, error) {
	ctx, cancel := s.bounded(ctx)
	defer cancel()
	t := s.task(ctx, st.Repository)
	release := Release{ReleaseState: st, ReleasePlan: plan}
	// The version's numbers alone: the tag's prefix and stage can be the
	// values given, which are not logged.
	logging.AddFields(ctx, zap.String("git.release.version", plan.Version.Core().String()),
		zap.Bool("git.release.prerelease", !plan.Version.Stable()), zap.Bool("git.release.push", push))
	ref := "refs/tags/" + plan.Tag
	if _, err := t.run("check-ref-format", ref); err != nil {
		return release, refused(ctx, "the tag is no name git takes", fmt.Errorf("%s is no name git takes for a tag", plan.Tag))
	}
	if t.exists(ref) {
		return release, refused(ctx, "the tag exists already", fmt.Errorf("%s exists already: it was made since the tags were read", plan.Tag))
	}
	if push {
		if out, err := t.run("ls-remote", "--tags", "--refs", st.Remote, ref); err != nil {
			return release, refused(ctx, "the remote could not be asked for the tag", fmt.Errorf("asking %s for %s: %w", st.Remote, plan.Tag, err))
		} else if strings.TrimSpace(out) != "" {
			return release, refused(ctx, "the remote has the tag already",
				fmt.Errorf("%s has %s already: it was pushed since the tags were read", st.Remote, plan.Tag))
		}
	}
	if message == "" {
		message = "Release " + plan.Tag
	}
	// Stopped or timed out, ctx is done: what is undone is undone regardless,
	// with a little time of its own. Only the tag object this run made is
	// deleted: a tag of the name another made meanwhile is left as it is.
	undoGit := func() (*task, context.CancelFunc) {
		undoCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		return s.task(undoCtx, st.Repository), cancel
	}
	undo := func(object string) error {
		if object == "" {
			return nil
		}
		u, cancel := undoGit()
		defer cancel()
		_, err := u.run("update-ref", "-d", ref, object)
		return err
	}
	// verbatim, so the message kept is the one given, by which madeTag knows it.
	if _, err := t.run("tag", "--annotate", "--cleanup=verbatim", "--message", message, "--", plan.Tag, st.Commit); err != nil {
		// The tag can be written before git fails, as when a hook is stopped: it
		// is this run's when it tags the commit with this run's message.
		var undoErr error
		if !strings.Contains(stderr(err), "already exists") {
			u, cancel := undoGit()
			undoErr = undo(u.madeTag(ref, st.Commit, message))
			cancel()
		}
		if undoErr != nil {
			return release, refused(ctx, "tagging failed, and the tag is left here",
				fmt.Errorf("tagging %s: %w; the tag it made is still here, and must be deleted before trying again", short(st.Commit), err))
		}
		return release, refused(ctx, "tagging failed", fmt.Errorf("tagging %s: %w", short(st.Commit), err))
	}
	u, cancel := undoGit()
	object := u.madeTag(ref, st.Commit, message)
	cancel()
	release.Created = true
	if !push {
		logging.Summarize(ctx, fmt.Sprintf("tagged %s as %s, without pushing it", short(st.Commit), described(plan.Version)))
		return release, nil
	}
	// --no-follow-tags, or push.followTags would push other tags along with it.
	if _, err := t.run("push", "--no-follow-tags", st.Remote, ref+":"+ref); err != nil {
		// The remote may have taken the tag before the push failed or was
		// stopped: then it is released, and the tag here stays.
		u, cancel := undoGit()
		out, askErr := u.run("ls-remote", "--tags", st.Remote, ref)
		cancel()
		if askErr != nil {
			return release, refused(ctx, "pushing failed, and whether the remote took the tag is unknown", fmt.Errorf(
				"pushing %s to %s: %w; whether %s took it could not be asked, so the tag stays here until you know",
				plan.Tag, st.Remote, err, st.Remote))
		}
		if fields := strings.Fields(out); len(fields) > 0 {
			if object != "" && fields[0] == object {
				release.Pushed = true
				logging.Summarize(ctx, fmt.Sprintf("released %s as %s, which the remote took though its push failed",
					short(st.Commit), described(plan.Version)))
				return release, nil
			}
			return release, refused(ctx, "pushing failed, and the remote has another tag of the name", fmt.Errorf(
				"pushing %s to %s: %w; %s has another tag of that name, and the one here stays to be compared", plan.Tag, st.Remote, err, st.Remote))
		}
		release.Created = false
		if undoErr := undo(object); undoErr != nil || object == "" {
			return release, refused(ctx, "pushing failed, and the tag is left here", fmt.Errorf(
				"pushing %s to %s: %w; the tag is still here, and must be deleted before trying again", plan.Tag, st.Remote, err))
		}
		return release, refused(ctx, "pushing failed, so the tag was deleted again",
			fmt.Errorf("pushing %s to %s: %w; the tag was deleted again, so trying again starts afresh", plan.Tag, st.Remote, err))
	}
	release.Pushed = true
	logging.Summarize(ctx, fmt.Sprintf("released %s as %s and pushed it", short(st.Commit), described(plan.Version)))
	return release, nil
}

// described is a version as the log may say it: its numbers, and whether it is a
// pre-release, whose stage can be the value given.
func described(v core.Version) string {
	if v.Stable() {
		return v.Core().String()
	}
	return "a pre-release of " + v.Core().String()
}

// Recheck reads the repository again, as InspectRelease did for st, and refuses
// a plan the repository has moved on from since: a branch moved, or a tag made,
// while the person was asked. It returns the state read again.
func (s *Service) Recheck(ctx context.Context, st *ReleaseState, plan ReleasePlan) (*ReleaseState, error) {
	again, err := s.InspectRelease(ctx, st.located, st.options)
	if err != nil {
		return nil, err
	}
	if again.Commit != st.Commit {
		return nil, refused(ctx, "the branch moved on while asking",
			fmt.Errorf("%s moved on to %s while you were asked: run again to release it", st.Branch, short(again.Commit)))
	}
	replanned, err := again.Plan(plan.Choice)
	if err != nil {
		return nil, refused(ctx, "the tags changed while asking", fmt.Errorf("the tags changed while you were asked: %w", err))
	}
	if replanned.Tag != plan.Tag {
		return nil, refused(ctx, "the tags changed while asking",
			fmt.Errorf("the tags changed while you were asked: it would be %s now, so run again", replanned.Tag))
	}
	return again, nil
}

// madeTag is the tag object ref names when it is the one this run made, an
// annotated tag of commit with message, or empty.
func (t *task) madeTag(ref, commit, message string) string {
	out, err := t.run("for-each-ref",
		"--format=%(objecttype)%00%(objectname)%00%(*objectname)%00%(contents:signature)%00%(contents)", ref)
	if err != nil {
		return ""
	}
	fields := strings.SplitN(out, "\x00", 5)
	if len(fields) != 5 || fields[0] != "tag" || fields[2] != commit {
		return ""
	}
	// A signed tag's contents end in its signature; a message can look like one.
	contents := strings.TrimRight(fields[4], "\n")
	unsigned := strings.TrimSuffix(contents, strings.TrimRight(fields[3], "\n"))
	if strings.TrimSpace(contents) != strings.TrimSpace(message) && strings.TrimSpace(unsigned) != strings.TrimSpace(message) {
		return ""
	}
	return fields[1]
}

func (s *Service) task(ctx context.Context, repo Repository) *task {
	return &task{ctx: ctx, git: s.Git, repo: repo, progress: func(string) {}}
}

// bounded is ctx bounded by Timeout, when there is one.
func (s *Service) bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	if s.Timeout > 0 {
		return context.WithTimeout(ctx, s.Timeout)
	}
	return ctx, func() {}
}
