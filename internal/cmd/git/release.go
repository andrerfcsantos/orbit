package git

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/andrerfcsantos/orbit/internal/app"
	appgit "github.com/andrerfcsantos/orbit/internal/app/git"
	"github.com/andrerfcsantos/orbit/internal/cmd/internal/exit"
	core "github.com/andrerfcsantos/orbit/internal/core/git"
	platformconfig "github.com/andrerfcsantos/orbit/internal/platform/config"
	tuigit "github.com/andrerfcsantos/orbit/internal/transport/tui/git"
	"github.com/spf13/cobra"
)

func newReleaseTag(provider app.RuntimeProvider, manager *platformconfig.Manager) *cobra.Command {
	var (
		version     string
		dryRun, yes bool
	)
	cmd := &cobra.Command{
		Use:   "release-tag [path]",
		Short: "Tag the branch's tip with the next version and push the tag, which starts a release",
		Long: `Tag the tip of a branch, as the remote has it, with the next semantic version, and
push the tag. The repository is the one the path is, or is inside: "." when no
path is given. The branch is the one checked out, unless --branch says otherwise.

The branch and every tag are fetched first. The next version bumps the latest
release the branch has, as the remote has it: a minor bump by default, --bump
patch or --bump major otherwise, or --version names it. With no release yet, the
first is 0.1.0 (0.0.1 with --bump patch, 1.0.0 with --bump major).

The tag follows the repository's convention: the prefix of its latest version tag,
such as v (v1.4.0) or release- (release-1.4.0), or v for the first. --prefix sets
it, "" for none.

--pre makes a pre-release of the next version instead: alpha, beta, rc, or a
word of your own. It is numbered after the last of its stage, from 1, and goes on
with a series under way: v2.0.0-beta.2 is followed by v2.0.0-beta.3.

Never made: a tag that exists already, here or on the remote; a pre-release of a
version released already; a release of a commit released already. When a push
fails or is stopped, the remote is asked whether it took the tag: if it did, the
tag is released; if it has none, the tag here is deleted again, so trying again
starts afresh; otherwise the tag here stays, and orbit says why. A remote that
pushes somewhere other than it fetches from is refused.

In a terminal, what is not given is asked, with the default first (the bump, or
the branch when HEAD is detached), and so is whether to go ahead, unless --yes.
The command that does the same without asking is printed at the end: after a
--dry-run, that command is a dry run too. Elsewhere,
and with --json, it needs --yes or --dry-run.

Examples:
  orb git release-tag                       # asks, minor first
  orb git release-tag --bump patch --yes
  orb git release-tag --pre rc              # v1.5.0-rc.1, then rc.2, ...
  orb git release-tag --pre beta --branch next
  orb git release-tag --version 2.0.0 --dry-run`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReleaseTag(cmd, provider, manager, args, version, dryRun, yes)
		},
	}
	manager.RegisterStringFlag(cmd, platformconfig.GitReleaseTagBumpKey)
	manager.RegisterStringFlag(cmd, platformconfig.GitReleaseTagPreKey)
	manager.RegisterStringFlag(cmd, platformconfig.GitReleaseTagPrefixKey)
	manager.RegisterStringFlag(cmd, platformconfig.GitReleaseTagBranchKey)
	manager.RegisterStringFlag(cmd, platformconfig.GitReleaseTagRemoteKey)
	manager.RegisterBoolFlag(cmd, platformconfig.GitReleaseTagPushKey)
	manager.RegisterStringFlag(cmd, platformconfig.GitReleaseTagMessageKey)
	cmd.Flags().StringVar(&version, "version", "", "The version to release, such as 2.0.0, instead of a bump")
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "Say what the tag would be, and make none")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Go ahead without asking anything")
	return cmd
}

// releaseAnswers are what was asked, which the command printed at the end
// gives instead.
type releaseAnswers struct {
	branch string
	choice *core.Choice
}

func runReleaseTag(cmd *cobra.Command, provider app.RuntimeProvider, manager *platformconfig.Manager, args []string,
	version string, dryRun, yes bool) error {
	s, err := open(cmd, provider, args)
	if err != nil {
		return err
	}
	defer s.cancel()
	config := provider().Config
	stdout, stderr := cmd.OutOrStdout(), cmd.ErrOrStderr()
	interactive := !s.json && !yes && isTerminal(cmd.InOrStdin()) && isTerminal(stderr)
	if !yes && !dryRun && !interactive {
		if s.json {
			return exit.Usage(errors.New("--json prints one document, so it cannot ask first: add --yes, or --dry-run"))
		}
		return exit.Usage(errors.New("there is no terminal to ask at: add --yes to go ahead without asking, or --dry-run to only see the tag"))
	}

	// What to release is asked in a terminal unless something says it.
	ask := interactive && version == "" && !manager.Explicit(platformconfig.KeyGitReleaseTagBump) &&
		!manager.Explicit(platformconfig.KeyGitReleaseTagPre)
	choice := core.Choice{Stage: strings.TrimSpace(config.GetString(platformconfig.KeyGitReleaseTagPre))}
	if version != "" {
		if cmd.Flags().Changed("bump") {
			return exit.Usage(errors.New("--version names the version, so it takes no --bump"))
		}
		v, err := core.ParseVersion(version)
		if err != nil {
			return exit.Usage(fmt.Errorf("--version: %w", err))
		}
		choice.Version = &v
	} else if manager.Explicit(platformconfig.KeyGitReleaseTagBump) {
		if choice.Bump, err = core.ParseBump(config.GetString(platformconfig.KeyGitReleaseTagBump)); err != nil {
			return exit.Usage(fmt.Errorf("--bump: %w", err))
		}
	}
	if err := choice.Validate(); err != nil {
		return exit.Usage(err)
	}
	push := config.GetBool(platformconfig.KeyGitReleaseTagPush)
	options := appgit.ReleaseOptions{
		Remote:    strings.TrimSpace(config.GetString(platformconfig.KeyGitReleaseTagRemote)),
		Branch:    strings.TrimSpace(config.GetString(platformconfig.KeyGitReleaseTagBranch)),
		Prefix:    strings.TrimSpace(config.GetString(platformconfig.KeyGitReleaseTagPrefix)),
		PrefixSet: manager.Explicit(platformconfig.KeyGitReleaseTagPrefix),
		Push:      push,
	}
	if strings.ContainsAny(options.Prefix, " \t") {
		return exit.Usage(errors.New("--prefix cannot hold spaces"))
	}

	path := "."
	if len(args) == 1 {
		path = args[0]
	}
	repo, err := s.service.LocateRelease(s.ctx, path)
	if err != nil {
		return exit.Usage(err)
	}
	prompter := tuigit.Prompter{Ctx: s.ctx, In: cmd.InOrStdin(), Out: stderr}
	var answers releaseAnswers
	if options.Branch == "" && repo.Branch == "" {
		if !interactive {
			return exit.Usage(errors.New("HEAD is detached: give the branch to release with --branch"))
		}
		branch, err := prompter.Input("HEAD is detached. Which branch to release?", s.service.DefaultBranch(s.ctx, repo.Repository),
			func(value string) error {
				if value == "" {
					return errors.New("give a branch")
				}
				return nil
			})
		if err != nil {
			return exit.Fail(err)
		}
		options.Branch, answers.branch = branch, branch
	}

	if interactive {
		fmt.Fprintf(stderr, "Reading %s and its tags...\n", cmp.Or(options.Branch, repo.Branch))
	}
	st, err := s.service.InspectRelease(s.ctx, repo, options)
	if err != nil {
		if s.ctx.Err() != nil {
			return exit.Fail(errors.New("stopped: nothing was changed"))
		}
		return said(stderr, err)
	}

	if ask {
		chosen, err := askChoice(prompter, stderr, st)
		if err != nil {
			return exit.Fail(err)
		}
		choice, answers.choice = chosen, &chosen
	}
	plan, err := s.service.PlanRelease(s.ctx, st, choice)
	if err != nil {
		return said(stderr, err)
	}

	planOut := stdout
	if s.json {
		planOut = io.Discard
	}
	if err := tuigit.RenderReleasePlan(planOut, st, plan); err != nil {
		return err
	}
	command := releaseCommand(cmd, args, answers, dryRun)
	if dryRun {
		release := appgit.Release{ReleaseState: st, ReleasePlan: plan}
		if s.json {
			return tuigit.RenderJSON(stdout, release)
		}
		if err := tuigit.RenderRelease(stdout, release, true, push); err != nil {
			return err
		}
		if interactive && (answers.branch != "" || answers.choice != nil) {
			return tuigit.RenderCommand(stdout, command)
		}
		return nil
	}
	if interactive {
		question := fmt.Sprintf("Tag %s as %s and push it to %s?", shortCommit(st.Commit), plan.Tag, st.Remote)
		if !push {
			question = fmt.Sprintf("Tag %s as %s?", shortCommit(st.Commit), plan.Tag)
		}
		fmt.Fprintln(stderr)
		ok, err := prompter.Confirm(question)
		if err != nil {
			return exit.Fail(err)
		}
		if !ok {
			fmt.Fprintln(stdout, "Nothing was changed.")
			return nil
		}
		// The branch or the tags may have moved on while the question was open.
		if st, err = s.service.Recheck(s.ctx, st, plan); err != nil {
			return said(stderr, err)
		}
	}

	release, err := s.service.CreateRelease(s.ctx, st, plan, config.GetString(platformconfig.KeyGitReleaseTagMessage), push)
	if err != nil {
		return said(stderr, err)
	}
	if s.json {
		return tuigit.RenderJSON(stdout, release)
	}
	if err := tuigit.RenderRelease(stdout, release, false, push); err != nil {
		return err
	}
	if interactive {
		return tuigit.RenderCommand(stdout, command)
	}
	return nil
}

// askChoice asks what to release: a bump of the latest release, a series under
// way gone on with, or a pre-release, each with the tag it makes.
func askChoice(prompter tuigit.Prompter, out io.Writer, st *appgit.ReleaseState) (core.Choice, error) {
	question := "What to release? There is no release on " + st.Branch + " yet."
	if latest, ok := st.Latest(); ok {
		question = "What to release after " + latest.Name + "?"
	}
	choices := []core.Choice{{Bump: core.BumpMinor}, {Bump: core.BumpPatch}, {Bump: core.BumpMajor}}
	var options []tuigit.Option
	for _, choice := range choices {
		options = append(options, tuigit.Option{Label: string(choice.Bump), Detail: detail(st, choice)})
	}
	// Each series under way, once, highest first, to go on with.
	var seen []string
	for _, tag := range st.InProgress() {
		stage, ok := seriesOf(tag.Version.Pre)
		if !ok || slices.Contains(seen, stage) {
			continue
		}
		seen = append(seen, stage)
		choice := core.Choice{Stage: stage}
		choices = append(choices, choice)
		options = append(options, tuigit.Option{Label: "next " + stage, Detail: detail(st, choice) + ", after " + tag.Name})
	}
	choices = append(choices, core.Choice{})
	options = append(options, tuigit.Option{Label: "pre-release", Detail: "alpha, beta, rc or another"})

	for {
		i, err := prompter.Select(question, options, 0)
		if err != nil {
			return core.Choice{}, err
		}
		choice := choices[i]
		if i == len(choices)-1 {
			if choice, err = askPreRelease(prompter, st); err != nil {
				return core.Choice{}, err
			}
		}
		if _, err := st.Plan(choice); err != nil {
			fmt.Fprintf(out, "  %s\n", err)
			continue
		}
		return choice, nil
	}
}

func askPreRelease(prompter tuigit.Prompter, st *appgit.ReleaseState) (core.Choice, error) {
	var options []tuigit.Option
	for _, stage := range core.Stages {
		options = append(options, tuigit.Option{Label: stage, Detail: detail(st, core.Choice{Stage: stage})})
	}
	options = append(options, tuigit.Option{Label: "another", Detail: "a word of your own"})
	i, err := prompter.Select("Which pre-release?", options, 0)
	if err != nil {
		return core.Choice{}, err
	}
	stage := ""
	if i < len(core.Stages) {
		stage = core.Stages[i]
	} else if stage, err = prompter.Input("Which word?", "", func(value string) error {
		if value == "" {
			return errors.New("give a word, such as preview")
		}
		return core.Choice{Stage: value}.Validate()
	}); err != nil {
		return core.Choice{}, err
	}

	var bumps []tuigit.Option
	choices := []core.Choice{{Stage: stage}}
	bumps = append(bumps, tuigit.Option{Label: "the one under way", Detail: detail(st, choices[0])})
	for _, bump := range core.Bumps {
		choice := core.Choice{Stage: stage, Bump: bump}
		choices = append(choices, choice)
		bumps = append(bumps, tuigit.Option{Label: "of the next " + string(bump), Detail: detail(st, choice)})
	}
	// Going on is offered only when it is not what a minor bump makes.
	if bumps[0].Detail == bumps[1].Detail {
		choices, bumps = choices[1:], bumps[1:]
	}
	if i, err = prompter.Select("A pre-release of which version?", bumps, 0); err != nil {
		return core.Choice{}, err
	}
	return choices[i], nil
}

// detail is the tag a choice makes, or why it makes none.
func detail(st *appgit.ReleaseState, choice core.Choice) string {
	plan, err := st.Plan(choice)
	if err != nil {
		return "(" + err.Error() + ")"
	}
	return plan.Tag
}

// seriesOf reads a pre-release as stage.N.
func seriesOf(pre string) (stage string, ok bool) {
	ids := strings.Split(pre, ".")
	if len(ids) == 2 && isNumber(ids[1]) {
		return ids[0], true
	}
	return "", false
}

func isNumber(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

// releaseCommand is the command that does what was just done without asking:
// what was given on the command line, the answers, and --yes.
func releaseCommand(cmd *cobra.Command, args []string, answers releaseAnswers, dryRun bool) string {
	parts := []string{"orb", "git", "release-tag"}
	flag := func(name, value string) { parts = append(parts, "--"+name, tuigit.ShellQuote(value)) }
	given := func(name string) (string, bool) {
		f := cmd.Flags().Lookup(name)
		if f == nil || !f.Changed {
			return "", false
		}
		return f.Value.String(), true
	}
	if answers.branch != "" {
		flag("branch", answers.branch)
	} else if value, ok := given("branch"); ok {
		flag("branch", value)
	}
	for _, name := range []string{"remote", "prefix"} {
		if value, ok := given(name); ok {
			flag(name, value)
		}
	}
	if answers.choice != nil {
		if answers.choice.Bump != "" {
			flag("bump", string(answers.choice.Bump))
		}
		if answers.choice.Stage != "" {
			flag("pre", answers.choice.Stage)
		}
	} else {
		for _, name := range []string{"bump", "pre", "version"} {
			if value, ok := given(name); ok {
				flag(name, value)
			}
		}
	}
	if value, ok := given("push"); ok {
		parts = append(parts, "--push="+value)
	}
	if value, ok := given("message"); ok {
		flag("message", value)
	}
	// A dry run's command is a dry run too: copied, it must not tag anything.
	if dryRun {
		parts = append(parts, "--dry-run")
	} else {
		parts = append(parts, "--yes")
	}
	// The path after the flags, past a -- when it could be read as one.
	if len(args) > 0 && strings.HasPrefix(args[0], "-") {
		parts = append(parts, "--")
	}
	return strings.Join(append(parts, quoteAll(args)...), " ")
}

func quoteAll(values []string) []string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = tuigit.ShellQuote(value)
	}
	return quoted
}

// said prints an error that names the values given, and fails with nothing more
// to say: the log records why in words of its own, as the service summarized it.
func said(stderr io.Writer, err error) error {
	fmt.Fprintln(stderr, "Error:", err)
	return exit.Fail(nil)
}

func shortCommit(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}
