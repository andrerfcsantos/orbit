package git

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"

	appgit "github.com/andrerfcsantos/orbit/internal/app/git"
	"github.com/andrerfcsantos/orbit/internal/transport/tui/styles"
)

// RenderReleasePlan says what is about to be tagged: where, the version it
// follows and the one it makes, and what to know first.
func RenderReleasePlan(w io.Writer, st *appgit.ReleaseState, plan appgit.ReleasePlan) error {
	s := styles.For(w)
	var b strings.Builder
	where := st.Branch
	if st.Remote != "" {
		where = st.Remote + "/" + st.Branch
	}
	fmt.Fprintf(&b, "%s %s %s at %s\n", s.Title.Render(st.Repository.Name), s.Subtle.Render("·"), s.Key.Render(where),
		s.Subtle.Render(shortCommit(st.Commit)+" "+quote(st.Subject)))
	if plan.Previous == "" {
		fmt.Fprintf(&b, "  %s %s\n", s.Success.Render(plan.Tag), s.Subtle.Render("the first release: the branch has no version tag yet"))
	} else {
		fmt.Fprintf(&b, "  %s %s %s\n", s.Subtle.Render(plan.Previous), s.Subtle.Render("→"), s.Success.Render(plan.Tag))
	}
	if st.Followed && st.Convention != "" && st.Prefix != "v" {
		fmt.Fprintf(&b, "  %s\n", s.Subtle.Render(fmt.Sprintf("prefixed %q, as %s is", st.Prefix, st.Convention)))
	}
	for _, note := range plan.Notes {
		fmt.Fprintf(&b, "  %s\n", s.Subtle.Render(note))
	}
	for _, warning := range plan.Warnings {
		fmt.Fprintf(&b, "  %s %s\n", s.Warning.Render("!"), warning)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// RenderRelease says what was done, or what a dry run would have done, push
// saying whether it pushes.
func RenderRelease(w io.Writer, release appgit.Release, dryRun, push bool) error {
	s := styles.For(w)
	commit := shortCommit(release.Commit)
	var line string
	switch {
	case dryRun && push:
		line = fmt.Sprintf("Would tag %s as %s and push it to %s.", commit, release.Tag, release.Remote)
	case dryRun:
		line = fmt.Sprintf("Would tag %s as %s.", commit, release.Tag)
	case release.Pushed:
		line = s.Success.Render("✓") + fmt.Sprintf(" Tagged %s as %s and pushed it to %s.", commit, release.Tag, release.Remote)
	default:
		remote := "<remote>"
		if release.Remote != "" {
			remote = ShellQuote(release.Remote)
		}
		line = s.Success.Render("✓") + fmt.Sprintf(" Tagged %s as %s, here only: git -C %s push --no-follow-tags %s %s publishes it.",
			commit, release.Tag, ShellQuote(release.Repository.Path), remote, ShellQuote("refs/tags/"+release.Tag))
	}
	_, err := fmt.Fprintln(w, line)
	return err
}

// RenderCommand gives the command that does the same again without asking.
func RenderCommand(w io.Writer, command string) error {
	s := styles.For(w)
	_, err := fmt.Fprintf(w, "\n%s\n  %s\n", s.Subtle.Render("To do the same without being asked:"), s.Key.Render(command))
	return err
}

func shortCommit(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

// quote is a commit's subject as a line of the plan: its control characters,
// which could clear or rewrite what the terminal shows before the question,
// left out.
func quote(subject string) string {
	subject = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, subject)
	if runes := []rune(subject); len(runes) > 60 {
		subject = string(runes[:57]) + "..."
	}
	return `"` + subject + `"`
}

// plainWord is a word no shell changes; one starting with = zsh expands.
var plainWord = regexp.MustCompile(`^[A-Za-z0-9_./:@%+,-][A-Za-z0-9_./:=@%+,-]*$`)

// ShellQuote is value as a POSIX shell reads it back: as it is when that is
// safe, in single quotes otherwise.
func ShellQuote(value string) string {
	if plainWord.MatchString(value) {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
