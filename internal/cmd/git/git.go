// Package git is `orb git`: git, in every repository beneath the directories
// given, all at once.
package git

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/andrerfcsantos/orbit/internal/app"
	appgit "github.com/andrerfcsantos/orbit/internal/app/git"
	"github.com/andrerfcsantos/orbit/internal/cmd/internal/exit"
	core "github.com/andrerfcsantos/orbit/internal/core/git"
	platformconfig "github.com/andrerfcsantos/orbit/internal/platform/config"
	platformgit "github.com/andrerfcsantos/orbit/internal/platform/git"
	tuigit "github.com/andrerfcsantos/orbit/internal/transport/tui/git"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

const pathsHelp = `Each path is a repository, or a directory holding repositories at any depth
beneath it, which are all found: "." when no path is given. A directory inside a
repository that holds no repository of its own names that repository, as it would
for git. Hidden directories and node_modules are not searched.`

// New builds the `git` command group.
func New(provider app.RuntimeProvider, manager *platformconfig.Manager) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "git",
		Short: "Run git in every repository beneath a directory, all at once",
		Long: `Run git in every repository beneath the directories you give, all at once:
list their branches, update them, delete their old branches, clean them, and
collect their garbage. Give one repository and it is as if you ran git there.
release-tag works in one repository: it tags and pushes the next version.

` + pathsHelp + `

Each repository is worked on in a goroutine of its own, as many at once as the
command suits (--concurrency changes it), and a terminal shows each one's progress
and the batch's as they go. Nothing git runs can ask for a password: a remote that
needs one fails, and says so. Ctrl-C stops what is running and starts nothing more.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return exit.Usage(errors.New("unknown command: orb git has list, update, branch delete, gc, clean and release-tag"))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	manager.RegisterPersistentBoolFlag(cmd, platformconfig.GitJSONKey)
	manager.RegisterPersistentIntFlag(cmd, platformconfig.GitConcurrencyKey)
	manager.RegisterPersistentDurationFlag(cmd, platformconfig.GitTimeoutKey)
	cmd.AddCommand(newList(provider), newUpdate(provider), newBranch(provider, manager), newGC(provider), newClean(provider),
		newReleaseTag(provider, manager))
	return cmd
}

// session is one command's service and how it shows what it does.
type session struct {
	cmd     *cobra.Command
	service *appgit.Service
	ctx     context.Context
	cancel  context.CancelFunc
	json    bool
	live    bool
	started time.Time

	paths []string
	repos []appgit.Repository
}

// open builds the service from configuration. Ctrl-C, a SIGTERM, or the
// terminal closing stops it: git runs detached from the terminal, so nothing
// else would.
func open(cmd *cobra.Command, provider app.RuntimeProvider, paths []string) (*session, error) {
	runtime := provider()
	if runtime == nil {
		return nil, errors.New("runtime is not initialized")
	}
	concurrency := runtime.Config.GetInt(platformconfig.KeyGitConcurrency)
	if concurrency < 0 {
		return nil, exit.Usage(errors.New("--concurrency cannot be negative"))
	}
	timeout, err := runtime.Config.Duration(platformconfig.KeyGitTimeout)
	if err != nil {
		return nil, exit.Usage(err)
	}
	if timeout < 0 {
		return nil, exit.Usage(errors.New("--timeout cannot be negative"))
	}
	if _, err := exec.LookPath("git"); err != nil {
		return nil, exit.Fail(errors.New("git is not installed, or not on your PATH"))
	}
	workDir, err := os.Getwd()
	if err != nil {
		return nil, exit.Fail(fmt.Errorf("reading the working directory: %w", err))
	}
	home, _ := os.UserHomeDir()

	// SIGPIPE too: unhandled, a closed stdout (orb git list | head) ends orbit
	// at once, leaving the git it started, in its own session, running.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGPIPE)
	ctx, cancel := context.WithCancel(ctx)
	asJSON := runtime.Config.GetBool(platformconfig.KeyGitJSON)
	return &session{
		cmd: cmd,
		service: &appgit.Service{
			Git:         platformgit.Runner{},
			Concurrency: concurrency,
			Timeout:     timeout,
			WorkDir:     workDir,
			Home:        home,
		},
		ctx:     ctx,
		cancel:  func() { cancel(); stop() },
		json:    asJSON,
		live:    !asJSON && isTerminal(cmd.OutOrStdout()),
		started: time.Now(),
		paths:   paths,
	}, nil
}

// find discovers the repositories once, however many batches ask.
func (s *session) find() ([]appgit.Repository, error) {
	if s.repos != nil {
		return s.repos, nil
	}
	repos, err := s.service.Discover(s.ctx, s.paths)
	if err != nil {
		if s.ctx.Err() != nil {
			return nil, exit.Fail(errors.New("stopped while looking for repositories"))
		}
		return nil, exit.Usage(err)
	}
	if len(repos) == 0 {
		return nil, exit.Fail(errors.New("no git repository was found there"))
	}
	s.repos = repos
	return repos, nil
}

// these is find for a batch over repositories already chosen.
func these(repos []appgit.Repository) func() ([]appgit.Repository, error) {
	return func() ([]appgit.Repository, error) { return repos, nil }
}

func (s *session) batch(title string, labels tuigit.Labels, hide bool) tuigit.Batch {
	var in io.Reader
	if isTerminal(s.cmd.InOrStdin()) {
		in = s.cmd.InOrStdin()
	}
	return tuigit.Batch{
		Title:  title,
		Labels: labels,
		Out:    s.cmd.OutOrStdout(),
		In:     in,
		Live:   s.live,
		Quiet:  s.json,
		Hide:   hide,
		Cancel: s.cancel,
	}
}

// finish writes the results, as JSON or with a summary, and says how the batch
// ended in the exit code.
func finish[T any](s *session, results []appgit.Result[T], labels tuigit.Labels, describe tuigit.Describe[T]) error {
	out := s.cmd.OutOrStdout()
	if s.json {
		if err := tuigit.RenderJSON(out, results); err != nil {
			return err
		}
	} else if err := tuigit.RenderSummary(out, labels, results, time.Since(s.started), describe); err != nil {
		return err
	}
	return outcome(results)
}

// stopped ends a command whose plan was stopped, doing none of it: with --json
// the plans gathered are still the document printed.
func stopped[T any](s *session, plans []appgit.Result[T]) error {
	if s.json {
		if err := tuigit.RenderJSON(s.cmd.OutOrStdout(), plans); err != nil {
			return err
		}
	}
	return outcome(plans)
}

func outcome[T any](results []appgit.Result[T]) error {
	tally := appgit.Count(results)
	switch {
	case tally.Cancelled > 0:
		return exit.Fail(errors.New("stopped before every repository was done"))
	case tally.Failed > 0:
		return exit.Fail(nil)
	}
	return nil
}

func newList(provider app.RuntimeProvider) *cobra.Command {
	return &cobra.Command{
		Use:   "list [path...]",
		Short: "List the repositories and the branch each has checked out",
		Long: `List the repositories, the branch each has checked out, and where each stands:
its changes, how far it is from its upstream as last fetched, and its stashes.
A branch other than the repository's default is highlighted. Nothing is fetched.

` + pathsHelp,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := open(cmd, provider, args)
			if err != nil {
				return err
			}
			defer s.cancel()
			results, err := tuigit.Run(s.batch("Reading", tuigit.Labels{Unchanged: "read"}, true), s.find,
				tuigit.DescribeNothing[appgit.Status],
				func(repos []appgit.Repository, progress appgit.Progress[appgit.Status]) []appgit.Result[appgit.Status] {
					return s.service.List(s.ctx, repos, progress)
				})
			if err != nil {
				return err
			}
			if s.json {
				err = tuigit.RenderJSON(cmd.OutOrStdout(), results)
			} else {
				err = tuigit.RenderList(cmd.OutOrStdout(), results)
			}
			if err != nil {
				return err
			}
			return outcome(results)
		},
	}
}

func newUpdate(provider app.RuntimeProvider) *cobra.Command {
	var options appgit.UpdateOptions
	cmd := &cobra.Command{
		Use:   "update [path...]",
		Short: "Fetch every repository and pull into the branch checked out, or the default branch",
		Long: `Fetch every remote of every repository, pruning the branches deleted on them, and
fast-forward the branch checked out to its upstream.

Your changes, untracked files included, are stashed for the pull and put back after
it. When they touch a file the pull changed, they are left in the stash instead,
rather than risk a conflict, and orbit says so: "git stash pop" brings them back.

--default switches each repository to its default branch first, and pulls that.
The default branch is the one the remote says its HEAD names. origin/HEAD, which
clone records and git never updates, is used only when the remote cannot be
reached, and failing that main or master. Changes on the branch you leave are stashed, and stay in the
stash, named after that branch: "git stash list" shows them.

A pull is only ever a fast-forward, so update never makes a merge commit nor leaves
a conflict: a branch that has diverged from its upstream is left as it is, as is a
repository in the middle of a merge or a rebase.

` + pathsHelp + `

Examples:
  orb git update                 # every repository beneath here
  orb git update ~/code --default
  orb git update ~/code/orbit    # just the one`,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := open(cmd, provider, args)
			if err != nil {
				return err
			}
			defer s.cancel()
			title := "Updating"
			if options.Default {
				title = "Updating to the default branch"
			}
			results, err := tuigit.Run(s.batch(title, tuigit.UpdateLabels, false), s.find, tuigit.DescribeUpdate,
				func(repos []appgit.Repository, progress appgit.Progress[appgit.Update]) []appgit.Result[appgit.Update] {
					return s.service.Update(s.ctx, repos, options, progress)
				})
			if err != nil {
				return err
			}
			return finish(s, results, tuigit.UpdateLabels, tuigit.DescribeUpdate)
		},
	}
	cmd.Flags().BoolVar(&options.Default, "default", false, "Switch to each repository's default branch (main or master) before pulling")
	return cmd
}

func newBranch(provider app.RuntimeProvider, manager *platformconfig.Manager) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "branch",
		Short: "Work on the branches of every repository",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return exit.Usage(errors.New("unknown command: orb git branch has delete"))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newBranchDelete(provider, manager))
	return cmd
}

func newBranchDelete(provider app.RuntimeProvider, manager *platformconfig.Manager) *cobra.Command {
	var remote, dryRun, yes bool
	cmd := &cobra.Command{
		Use:   "delete [path...]",
		Short: "Delete the branches whose last commit is older than 6 months, or --older-than",
		Long: `Delete every repository's branches whose last commit is older than 6 months, or
than --older-than says: a number and h, d, w, mo or y, such as 90d, 12w or 1y.

The branches are listed first, and deleted once you say so (--yes deletes without
asking, --dry-run only lists). Never deleted: the branch checked out, any branch
checked out in another worktree, the default branch, main and master.

A local branch is deleted whether or not it was merged, as git branch -D does,
since a branch squash-merged on GitHub never reads as merged. The list shows each
one's last commit, which recreates it (git branch <name> <commit>) until git
collects it as garbage: orb git gc and clean do, and so does git now and then.

--remote also deletes, on origin, the branches whose last commit is as old, after
fetching it, but for the default branch, main, master and any branch a local
branch kept tracks. They are deleted for everyone, so look at the list first. A
branch somebody has committed to since it was listed is left alone, locally and
on the remote. Deleting runs no pre-push hook.

` + pathsHelp + `

Examples:
  orb git branch delete ~/code --dry-run
  orb git branch delete ~/code --older-than 1y
  orb git branch delete ~/code/orbit --remote`,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := open(cmd, provider, args)
			if err != nil {
				return err
			}
			defer s.cancel()
			runtime := provider()
			age, err := core.ParseAge(runtime.Config.GetString(platformconfig.KeyGitBranchOlderThan))
			if err != nil {
				return exit.Usage(fmt.Errorf("--older-than: %w", err))
			}
			if err := s.mayAsk(yes, dryRun); err != nil {
				return err
			}
			options := appgit.BranchOptions{OlderThan: age, Remote: remote}

			plans, err := tuigit.Run(s.batch("Finding branches older than "+age.String(), tuigit.Labels{Changed: "with some", Unchanged: "with none"}, true),
				s.find, tuigit.DescribeNothing[appgit.Branches],
				func(repos []appgit.Repository, progress appgit.Progress[appgit.Branches]) []appgit.Result[appgit.Branches] {
					return s.service.StaleBranches(s.ctx, repos, options, progress)
				})
			if err != nil {
				return err
			}
			if appgit.Count(plans).Cancelled > 0 {
				return stopped(s, plans)
			}
			local, remotes := 0, 0
			for _, plan := range plans {
				local += len(plan.Detail.Local)
				remotes += len(plan.Detail.Remote)
			}
			if !s.json {
				if _, _, err := tuigit.RenderBranchPlan(cmd.OutOrStdout(), plans, age.String(), time.Now()); err != nil {
					return err
				}
			}
			if dryRun || local+remotes == 0 {
				if s.json {
					if err := tuigit.RenderJSON(cmd.OutOrStdout(), plans); err != nil {
						return err
					}
				}
				return outcome(plans)
			}

			question := "Delete " + tuigit.BranchCount(local, remotes) + "?"
			if remotes > 0 {
				question = "Delete " + tuigit.BranchCount(local, remotes) + "? The remote ones are deleted on the remote, for everyone."
			}
			if ok, err := s.confirm(question, yes); !ok || err != nil {
				return err
			}

			var repos []appgit.Repository
			for _, plan := range plans {
				if plan.Detail.Total() > 0 {
					repos = append(repos, plan.Repository)
				}
			}
			s.started = time.Now()
			results, err := tuigit.Run(s.batch("Deleting branches", tuigit.BranchLabels, false), these(repos), tuigit.DescribeDeleted,
				func(_ []appgit.Repository, progress appgit.Progress[appgit.Branches]) []appgit.Result[appgit.Branches] {
					return s.service.DeleteBranches(s.ctx, plans, progress)
				})
			if err != nil {
				return err
			}
			return finish(s, results, tuigit.BranchLabels, tuigit.DescribeDeleted)
		},
	}
	manager.RegisterStringFlag(cmd, platformconfig.GitBranchOlderThanKey)
	cmd.Flags().BoolVarP(&remote, "remote", "r", false, "Also delete origin's branches as old, on origin itself")
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "List the branches that would be deleted, and delete none")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Delete without asking")
	return cmd
}

func newGC(provider app.RuntimeProvider) *cobra.Command {
	var options appgit.GCOptions
	cmd := &cobra.Command{
		Use:   "gc [path...]",
		Short: "Run git's garbage collector in every repository",
		Long: `Run git gc in every repository, which packs its objects and prunes those nothing
refers to any more, and say how much space each freed. git gc repacks on every
core, so half as many repositories as there are cores are collected at once.

` + pathsHelp,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := open(cmd, provider, args)
			if err != nil {
				return err
			}
			defer s.cancel()
			results, err := tuigit.Run(s.batch("Collecting garbage", tuigit.GCLabels, false), s.find, tuigit.DescribeCollected,
				func(repos []appgit.Repository, progress appgit.Progress[appgit.Collected]) []appgit.Result[appgit.Collected] {
					return s.service.GC(s.ctx, repos, options, progress)
				})
			if err != nil {
				return err
			}
			failed := finish(s, results, tuigit.GCLabels, tuigit.DescribeCollected)
			if !s.json {
				var freed int64
				for _, result := range results {
					freed += result.Detail.Freed()
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s freed in all.\n", tuigit.Bytes(freed))
			}
			return failed
		},
	}
	cmd.Flags().BoolVar(&options.Aggressive, "aggressive", false, "Repack everything anew, as git gc --aggressive: much slower, for a somewhat smaller repository")
	return cmd
}

func newClean(provider app.RuntimeProvider) *cobra.Command {
	var dryRun, yes bool
	cmd := &cobra.Command{
		Use:   "clean [path...]",
		Short: "Remove every untracked and ignored file from every repository, then collect its garbage",
		Long: `Remove every untracked file from every repository, ignored ones included, as
git clean -ffxd does, and then run git gc.

That is everything git does not track: build output, dependencies such as
node_modules, .env files and other local configuration, editor settings, and any
repository nested in an untracked directory. It cannot be undone. The paths are
listed first, and removed once you say so (--yes removes them without asking,
--dry-run lists every one and removes none). Only the paths listed are removed:
a file that appears meanwhile is left, unless it is beneath a directory listed.
A repository whose paths could not be listed is left alone. A directory inside a
repository is cleaned alone, as git clean run there would.

` + pathsHelp,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := open(cmd, provider, args)
			if err != nil {
				return err
			}
			defer s.cancel()
			if err := s.mayAsk(yes, dryRun); err != nil {
				return err
			}
			var (
				plans   []appgit.Result[appgit.Cleaned]
				planned []appgit.Repository
			)
			if !yes || dryRun {
				plans, err = tuigit.Run(s.batch("Finding untracked and ignored files", tuigit.Labels{Changed: "with some", Unchanged: "with none"}, true),
					s.find, tuigit.DescribeNothing[appgit.Cleaned],
					func(repos []appgit.Repository, progress appgit.Progress[appgit.Cleaned]) []appgit.Result[appgit.Cleaned] {
						return s.service.Uncleaned(s.ctx, repos, progress)
					})
				if err != nil {
					return err
				}
				if appgit.Count(plans).Cancelled > 0 {
					return stopped(s, plans)
				}
				if s.json && dryRun {
					if err := tuigit.RenderJSON(cmd.OutOrStdout(), plans); err != nil {
						return err
					}
					return outcome(plans)
				}
				paths, err := tuigit.RenderCleanPlan(cmd.OutOrStdout(), plans)
				if err != nil {
					return err
				}
				if dryRun {
					return outcome(plans)
				}
				for _, plan := range plans {
					if plan.Outcome == appgit.OutcomeChanged || plan.Outcome == appgit.OutcomeUnchanged {
						planned = append(planned, plan.Repository)
					}
				}
				if len(planned) == 0 {
					return outcome(plans)
				}
				if paths > 0 {
					question := fmt.Sprintf("Remove these %d paths for good, and then collect garbage?", paths)
					if len(planned) < len(plans) {
						question = fmt.Sprintf("Remove these %d paths for good, and then collect garbage, leaving alone the %d %s whose paths could not be listed?",
							paths, len(plans)-len(planned), plural(len(plans)-len(planned), "repository", "repositories"))
					}
					if ok, err := s.confirm(question, yes); !ok || err != nil {
						return err
					}
				}
			}
			repos := planned
			if plans == nil {
				var err error
				if repos, err = s.find(); err != nil {
					return err
				}
			}
			s.started = time.Now()
			results, err := tuigit.Run(s.batch("Cleaning", tuigit.CleanLabels, false), these(repos), tuigit.DescribeCleaned,
				func(repos []appgit.Repository, progress appgit.Progress[appgit.Cleaned]) []appgit.Result[appgit.Cleaned] {
					if plans != nil {
						return s.service.CleanPlanned(s.ctx, plans, progress)
					}
					return s.service.Clean(s.ctx, repos, progress)
				})
			if err != nil {
				return err
			}
			return finish(s, results, tuigit.CleanLabels, tuigit.DescribeCleaned)
		},
	}
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "List every path that would be removed, and remove none")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Remove without asking")
	return cmd
}

// mayAsk refuses, before anything is done, a command that would have to ask and
// cannot: there is nobody at a terminal, or --json is to print one document.
func (s *session) mayAsk(yes, dryRun bool) error {
	if yes || dryRun {
		return nil
	}
	if s.json {
		return exit.Usage(errors.New("--json prints one document, so it cannot ask first: add --yes, or --dry-run"))
	}
	if !isTerminal(s.cmd.InOrStdin()) {
		return exit.Usage(errors.New("there is no terminal to ask at: add --yes to go ahead without asking, or --dry-run to only list"))
	}
	return nil
}

// confirm asks the question, and reports whether the answer was yes.
func (s *session) confirm(question string, yes bool) (bool, error) {
	if yes {
		return true, nil
	}
	stderr := s.cmd.ErrOrStderr()
	fmt.Fprintf(stderr, "\n%s [y/N] ", question)
	answers := make(chan string, 1)
	go func() {
		answer, _ := bufio.NewReader(s.cmd.InOrStdin()).ReadString('\n')
		answers <- answer
	}()
	select {
	case <-s.ctx.Done():
		fmt.Fprintln(stderr)
		return false, exit.Fail(errors.New("stopped: nothing was changed"))
	case answer := <-answers:
		if slices.Contains([]string{"y", "yes"}, strings.ToLower(strings.TrimSpace(answer))) {
			return true, nil
		}
	}
	fmt.Fprintln(s.cmd.OutOrStdout(), "Nothing was changed.")
	return false, nil
}

func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}

// isTerminal reports whether a stream is a terminal: a character device is not
// enough, since /dev/null is one too.
func isTerminal(stream any) bool {
	file, ok := stream.(*os.File)
	return ok && isatty.IsTerminal(file.Fd())
}
