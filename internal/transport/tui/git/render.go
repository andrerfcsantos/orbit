package git

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	appgit "github.com/andrerfcsantos/orbit/internal/app/git"
	"github.com/andrerfcsantos/orbit/internal/transport/tui/styles"
)

// RenderJSON writes what the command found, as a script would read it.
func RenderJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

// RenderSummary writes how the batch went, and repeats what needs attention --
// failures, skips and notes such as changes left in a stash -- which a long
// batch has scrolled away.
func RenderSummary[T any](writer io.Writer, labels Labels, results []appgit.Result[T], took time.Duration, describe Describe[T]) error {
	s := styles.For(writer)
	var b strings.Builder
	tally := appgit.Count(results)
	if len(results) > 1 {
		var attention []appgit.Result[T]
		for _, result := range results {
			if result.Outcome == appgit.OutcomeSkipped || result.Outcome == appgit.OutcomeFailed || len(result.Notes) > 0 {
				attention = append(attention, result)
			}
		}
		if len(attention) > 0 {
			width := 0
			for _, result := range attention {
				width = max(width, lipgloss.Width(result.Repository.Name))
			}
			fmt.Fprintf(&b, "\n%s\n", s.Title.Render("Needs your attention"))
			for _, result := range attention {
				fmt.Fprintf(&b, "%s\n", Line(s, result, min(width, maxNameWidth), describe))
			}
		}
	}
	counts := Counts(s, labels, tally)
	fmt.Fprintf(&b, "\n%d %s in %s", len(results), plural(len(results), "repository", "repositories"), took.Round(100*time.Millisecond))
	if len(counts) > 0 {
		fmt.Fprintf(&b, ": %s", strings.Join(counts, ", "))
	}
	b.WriteString("\n")
	_, err := io.WriteString(writer, b.String())
	return err
}

// Labels of each operation's outcomes.
var (
	UpdateLabels = Labels{Changed: "updated", Unchanged: "up to date"}
	BranchLabels = Labels{Changed: "with branches deleted", Unchanged: "with none to delete"}
	GCLabels     = Labels{Changed: "with space freed", Unchanged: "with nothing to free"}
	CleanLabels  = Labels{Changed: "cleaned", Unchanged: "already clean"}
)

// DescribeUpdate says what updating did.
func DescribeUpdate(result appgit.Result[appgit.Update]) string {
	update := result.Detail
	var parts []string
	if update.Switched {
		parts = append(parts, fmt.Sprintf("switched from %s to %s", update.From, update.Branch))
	}
	switch {
	case update.Pulled > 0:
		parts = append(parts, fmt.Sprintf("pulled %d %s into %s", update.Pulled, plural(update.Pulled, "commit", "commits"), update.Branch))
	case result.Outcome == appgit.OutcomeChanged || result.Outcome == appgit.OutcomeUnchanged:
		text := "up to date on " + update.Branch
		if update.Ahead > 0 {
			text += fmt.Sprintf(", %d %s ahead", update.Ahead, plural(update.Ahead, "commit", "commits"))
		}
		parts = append(parts, text)
	}
	if update.Restored {
		parts = append(parts, "your changes were stashed and put back")
	}
	return strings.Join(parts, ", ")
}

// DescribeDeleted says what deleting branches did.
func DescribeDeleted(result appgit.Result[appgit.Branches]) string {
	local, remote := deleted(result.Detail.Local), deleted(result.Detail.Remote)
	switch {
	case local == 0 && remote == 0:
		return ""
	case remote == 0:
		return fmt.Sprintf("deleted %d local %s", local, plural(local, "branch", "branches"))
	case local == 0:
		return fmt.Sprintf("deleted %d %s on %s", remote, plural(remote, "branch", "branches"), result.Detail.Remote[0].Remote)
	}
	return fmt.Sprintf("deleted %d local and %d remote branches", local, remote)
}

func deleted(branches []appgit.Branch) int {
	count := 0
	for _, branch := range branches {
		if branch.Deleted {
			count++
		}
	}
	return count
}

// DescribeCollected says what collecting garbage did.
func DescribeCollected(result appgit.Result[appgit.Collected]) string {
	return collected(result.Detail, result.Outcome)
}

func collected(c appgit.Collected, outcome appgit.Outcome) string {
	switch {
	case c.BytesBefore == 0 && c.BytesAfter == 0 && outcome == appgit.OutcomeFailed:
		return ""
	case c.Freed() > 0:
		return fmt.Sprintf("freed %s (%s → %s)", Bytes(c.Freed()), Bytes(c.BytesBefore), Bytes(c.BytesAfter))
	}
	return fmt.Sprintf("nothing to free (%s)", Bytes(c.BytesAfter))
}

// DescribeCleaned says what cleaning did.
func DescribeCleaned(result appgit.Result[appgit.Cleaned]) string {
	paths := len(result.Detail.Paths)
	if result.Outcome == appgit.OutcomeUnchanged {
		return "already clean"
	}
	var parts []string
	if paths > 0 {
		parts = append(parts, fmt.Sprintf("removed %d %s", paths, plural(paths, "path", "paths")))
	}
	if freed := result.Detail.Collected.Freed(); freed > 0 {
		parts = append(parts, "freed "+Bytes(freed))
	}
	return strings.Join(parts, ", ")
}

// DescribeNothing describes nothing: the plan is printed whole, after.
func DescribeNothing[T any](appgit.Result[T]) string { return "" }

// RenderList writes each repository's branch and where it stands.
func RenderList(writer io.Writer, results []appgit.Result[appgit.Status]) error {
	s := styles.For(writer)
	names, branches := len("REPOSITORY"), len("BRANCH")
	for _, result := range results {
		names = max(names, lipgloss.Width(result.Repository.Name))
		branches = max(branches, lipgloss.Width(branchOf(result.Detail)))
	}
	names, branches = min(names, maxNameWidth), min(branches, maxNameWidth)

	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s  %s\n", s.Key.Render(pad("REPOSITORY", names)), s.Key.Render(pad("BRANCH", branches)), s.Key.Render("STATUS"))
	onDefault, other, changed, unread := 0, 0, 0, 0
	for _, result := range results {
		status := result.Detail
		if result.Outcome == appgit.OutcomeFailed || result.Outcome == appgit.OutcomeCancelled {
			unread++
			fmt.Fprintf(&b, "%s  %s  %s\n", pad(result.Repository.Name, names), pad("", branches),
				s.Error.Render(Printable(join(result.Error, result.Reason))))
			continue
		}
		branch := pad(branchOf(status), branches)
		switch {
		case status.Detached:
			branch = s.Warning.Render(branch)
		case status.Branch == status.Default || status.Default == "" && (status.Branch == "main" || status.Branch == "master"):
			onDefault++
		default:
			other++
			branch = s.Title.Render(branch)
		}
		if status.Changes > 0 {
			changed++
		}
		fmt.Fprintf(&b, "%s  %s  %s\n", pad(result.Repository.Name, names), branch, standing(s, status))
	}
	fmt.Fprintf(&b, "\n%d %s: %d on the default branch, %d on another, %d with changes",
		len(results), plural(len(results), "repository", "repositories"), onDefault, other, changed)
	if unread > 0 {
		fmt.Fprintf(&b, ", %d not read", unread)
	}
	b.WriteString("\n")
	_, err := io.WriteString(writer, b.String())
	return err
}

func branchOf(status appgit.Status) string {
	switch {
	case status.Detached:
		return "detached at " + short(status.Commit)
	case status.Branch == "":
		return "(no commits)"
	}
	return status.Branch
}

// standing is where a repository stands: its changes, how far from its
// upstream, and its stashes.
func standing(s styles.Styles, status appgit.Status) string {
	var parts []string
	if status.Changes > 0 {
		parts = append(parts, s.Warning.Render(fmt.Sprintf("%d %s", status.Changes, plural(status.Changes, "change", "changes"))))
	}
	switch {
	case status.Detached:
	case status.Upstream == "":
		parts = append(parts, s.Subtle.Render("no upstream"))
	case status.UpstreamGone:
		parts = append(parts, s.Warning.Render(status.Upstream+" is gone"))
	case status.Ahead > 0 || status.Behind > 0:
		parts = append(parts, fmt.Sprintf("↑%d ↓%d %s", status.Ahead, status.Behind, s.Subtle.Render(status.Upstream)))
	}
	if status.Stashes > 0 {
		parts = append(parts, s.Subtle.Render(fmt.Sprintf("%d %s", status.Stashes, plural(status.Stashes, "stash", "stashes"))))
	}
	if len(parts) == 0 {
		return s.Success.Render("clean")
	}
	return strings.Join(parts, s.Subtle.Render(" · "))
}

// RenderBranchPlan writes the branches that would be deleted, and returns how
// many there are, locally and on remotes.
func RenderBranchPlan(writer io.Writer, plans []appgit.Result[appgit.Branches], olderThan string, now time.Time) (int, int, error) {
	s := styles.For(writer)
	var b strings.Builder
	local, remote, repos := 0, 0, 0
	width := 0
	for _, plan := range plans {
		for _, branch := range plan.Detail.Local {
			width = max(width, lipgloss.Width(Printable(branch.Name)))
		}
		for _, branch := range plan.Detail.Remote {
			width = max(width, lipgloss.Width(Printable(branch.Remote+"/"+branch.Name)))
		}
	}
	for _, plan := range plans {
		problem := plan.Outcome == appgit.OutcomeFailed || plan.Outcome == appgit.OutcomeSkipped
		if plan.Detail.Total() == 0 && !problem && len(plan.Notes) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s\n", s.Title.Render(Printable(plan.Repository.Name)))
		if problem {
			fmt.Fprintf(&b, "  %s %s\n", mark(s, plan.Outcome), Printable(join(plan.Error, plan.Reason)))
		}
		for _, note := range plan.Notes {
			fmt.Fprintf(&b, "  %s %s\n", s.Warning.Render("↳"), Printable(note))
		}
		if plan.Detail.Total() > 0 {
			repos++
		}
		for _, branch := range plan.Detail.Local {
			local++
			fmt.Fprintf(&b, "  %s  %s  %s\n", padFull(branch.Name, width), pad(Ago(now, branch.Date), 14), s.Subtle.Render(branch.Commit))
		}
		for _, branch := range plan.Detail.Remote {
			remote++
			fmt.Fprintf(&b, "  %s  %s  %s\n", s.Warning.Render(padFull(branch.Remote+"/"+branch.Name, width)),
				pad(Ago(now, branch.Date), 14), s.Subtle.Render(branch.Commit))
		}
	}
	if local+remote == 0 {
		fmt.Fprintf(&b, "No branch is older than %s in %d %s.\n", olderThan, len(plans), plural(len(plans), "repository", "repositories"))
	} else {
		fmt.Fprintf(&b, "\n%s older than %s, in %d of %d %s.\n", BranchCount(local, remote), olderThan,
			repos, len(plans), plural(len(plans), "repository", "repositories"))
	}
	_, err := io.WriteString(writer, b.String())
	return local, remote, err
}

// padFull pads a name to width and never shortens it: a branch shown is one
// to be deleted, and two names cut alike would read as one.
func padFull(name string, width int) string {
	name = Printable(name)
	return name + strings.Repeat(" ", max(0, width-lipgloss.Width(name)))
}

// BranchCount says how many branches: "3 local and 2 remote branches".
func BranchCount(local, remote int) string {
	switch {
	case remote == 0:
		return fmt.Sprintf("%d local %s", local, plural(local, "branch", "branches"))
	case local == 0:
		return fmt.Sprintf("%d remote %s", remote, plural(remote, "branch", "branches"))
	}
	return fmt.Sprintf("%d local and %d remote branches", local, remote)
}

// RenderCleanPlan writes every path cleaning would remove, and returns how many
// there are. Every one, since what is removed is what is shown: a directory is
// one path, so node_modules/ is one line.
func RenderCleanPlan(writer io.Writer, plans []appgit.Result[appgit.Cleaned]) (int, error) {
	s := styles.For(writer)
	var b strings.Builder
	total, repos := 0, 0
	for _, plan := range plans {
		paths := plan.Detail.Paths
		problem := plan.Outcome == appgit.OutcomeFailed || plan.Outcome == appgit.OutcomeSkipped
		if len(paths) == 0 && !problem {
			continue
		}
		total += len(paths)
		fmt.Fprintf(&b, "%s %s\n", s.Title.Render(Printable(plan.Repository.Name)),
			s.Subtle.Render(fmt.Sprintf("%d %s", len(paths), plural(len(paths), "path", "paths"))))
		if problem {
			fmt.Fprintf(&b, "  %s %s\n", mark(s, plan.Outcome), Printable(join(plan.Error, plan.Reason)))
		}
		if len(paths) > 0 {
			repos++
		}
		for _, path := range paths {
			fmt.Fprintf(&b, "  %s\n", Printable(path))
		}
	}
	if total == 0 {
		fmt.Fprintf(&b, "Nothing to remove in %d %s.\n", len(plans), plural(len(plans), "repository", "repositories"))
	} else {
		fmt.Fprintf(&b, "\n%d untracked and ignored %s in %d of %d %s.\n", total, plural(total, "path", "paths"),
			repos, len(plans), plural(len(plans), "repository", "repositories"))
	}
	_, err := io.WriteString(writer, b.String())
	return total, err
}

// Ago says how long before now a moment was, roughly: "8 months ago".
func Ago(now, then time.Time) string {
	days := int(now.Sub(then).Hours() / 24)
	switch {
	case days >= 365:
		years := days / 365
		return fmt.Sprintf("%d %s ago", years, plural(years, "year", "years"))
	case days >= 60:
		months := days / 30
		return fmt.Sprintf("%d months ago", months)
	case days >= 14:
		return fmt.Sprintf("%d weeks ago", days/7)
	case days >= 1:
		return fmt.Sprintf("%d %s ago", days, plural(days, "day", "days"))
	}
	return "today"
}

// Bytes says a size as a person reads it.
func Bytes(count int64) string {
	const unit = 1024
	if count < unit {
		return fmt.Sprintf("%d B", count)
	}
	value := float64(count)
	for _, suffix := range []string{"KB", "MB", "GB", "TB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f PB", value/unit)
}

func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}
