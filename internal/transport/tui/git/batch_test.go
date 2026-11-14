package git

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	appgit "github.com/andrerfcsantos/orbit/internal/app/git"
	"github.com/andrerfcsantos/orbit/internal/transport/tui/styles"
)

func update(t *testing.T, m model, msgs ...tea.Msg) model {
	t.Helper()
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		m = next.(model)
	}
	return m
}

func TestTheViewShowsEachRepositoryAndTheBatch(t *testing.T) {
	var out bytes.Buffer
	stopped := false
	batch := Batch{Title: "Updating", Labels: UpdateLabels, Out: &out, Cancel: func() { stopped = true }}
	m := newModel(batch, styles.For(&out))
	if view := m.View().Content; !strings.Contains(view, "Looking for repositories") {
		t.Errorf("searching: %q", view)
	}

	m = update(t, m,
		// A terminal that does not know its size keeps the view's own.
		tea.WindowSizeMsg{Width: 0, Height: 0},
		foundMsg{total: 3, width: 12},
		startedMsg{path: "/a", name: "team/a"},
		startedMsg{path: "/b", name: "team/b"},
		stepMsg{path: "/a", step: "fetching"},
		stepMsg{path: "/b", step: "pulling 2 commits"},
		finishedMsg{path: "/b", outcome: appgit.OutcomeChanged},
	)
	view := m.View().Content
	for _, want := range []string{"Updating", "3 repositories", "team/a", "fetching", "1/3", "1 updated", "ctrl+c to stop"} {
		if !strings.Contains(view, want) {
			t.Errorf("the view lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "team/b") {
		t.Errorf("a finished repository is still shown as running:\n%s", view)
	}

	m = update(t, m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !stopped || !strings.Contains(m.View().Content, "Stopping") {
		t.Errorf("ctrl+c did not stop the batch:\n%s", m.View().Content)
	}
	if m = update(t, m, doneMsg{}); m.View().Content != "" {
		t.Errorf("a finished view is left on the screen: %q", m.View().Content)
	}
}

func TestPlainLinesAndTheSummary(t *testing.T) {
	var out bytes.Buffer
	repos := []appgit.Repository{{Path: "/a", Name: "team/a"}, {Path: "/b", Name: "b"}, {Path: "/c", Name: "c"}}
	results, err := Run(Batch{Out: &out, Labels: UpdateLabels}, these(repos), DescribeUpdate,
		func(repos []appgit.Repository, progress appgit.Progress[appgit.Update]) []appgit.Result[appgit.Update] {
			results := []appgit.Result[appgit.Update]{
				{Repository: repos[0], Outcome: appgit.OutcomeChanged, Detail: appgit.Update{Branch: "main", Pulled: 2, Restored: true}},
				{Repository: repos[1], Outcome: appgit.OutcomeFailed, Error: "git fetch: Could not read from remote repository."},
				{Repository: repos[2], Outcome: appgit.OutcomeCancelled, Reason: "stopped before it started"},
			}
			for i := range results[:2] {
				progress(appgit.Event[appgit.Update]{Kind: appgit.EventStarted, Repository: repos[i]})
			}
			for i := range results {
				progress(appgit.Event[appgit.Update]{Kind: appgit.EventFinished, Repository: repos[i], Result: &results[i]})
			}
			return results
		})
	if err != nil {
		t.Fatal(err)
	}
	printed := out.String()
	if want := "✓ team/a  pulled 2 commits into main, your changes were stashed and put back\n" +
		"✗ b       git fetch: Could not read from remote repository.\n"; printed != want {
		t.Errorf("printed:\n%s\nwant:\n%s", printed, want)
	}

	out.Reset()
	if err := RenderSummary(&out, UpdateLabels, results, 0, DescribeUpdate); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Needs your attention", "✗ b", "3 repositories in 0s: 1 updated, 1 failed, 1 stopped"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the summary lacks %q:\n%s", want, out.String())
		}
	}
}

// What clean removes is what the plan shows: every path, however many.
func TestTheCleanPlanShowsEveryPath(t *testing.T) {
	var paths []string
	for i := range 20 {
		paths = append(paths, fmt.Sprintf("scratch-%02d.txt", i))
	}
	var out bytes.Buffer
	total, err := RenderCleanPlan(&out, []appgit.Result[appgit.Cleaned]{{
		Repository: appgit.Repository{Path: "/a", Name: "a"}, Outcome: appgit.OutcomeChanged, Detail: appgit.Cleaned{Paths: paths},
	}})
	if err != nil || total != 20 {
		t.Fatalf("total %d, %v", total, err)
	}
	for _, path := range paths {
		if !strings.Contains(out.String(), path) {
			t.Errorf("%s is not shown:\n%s", path, out.String())
		}
	}
}

// A repository that could not be read is on no branch at all.
func TestTheListCountsOnlyWhatItRead(t *testing.T) {
	var out bytes.Buffer
	results := []appgit.Result[appgit.Status]{
		{Repository: appgit.Repository{Path: "/a", Name: "a"}, Outcome: appgit.OutcomeUnchanged, Detail: appgit.Status{Branch: "main", Default: "main"}},
		{Repository: appgit.Repository{Path: "/b", Name: "b"}, Outcome: appgit.OutcomeFailed, Error: "unreadable"},
	}
	if err := RenderList(&out, results); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "2 repositories: 1 on the default branch, 0 on another, 0 with changes, 1 not read") {
		t.Errorf("printed:\n%s", out.String())
	}
}

// Every branch to delete is named whole, however long: two names cut alike
// would read as one.
func TestTheBranchPlanNamesEveryBranchWhole(t *testing.T) {
	long := "feature/" + strings.Repeat("x", 120)
	var out bytes.Buffer
	plans := []appgit.Result[appgit.Branches]{{
		Repository: appgit.Repository{Path: "/a", Name: "a"}, Outcome: appgit.OutcomeChanged,
		Detail: appgit.Branches{
			Local:  []appgit.Branch{{Name: long + "-one", Commit: "aaaaaaaa"}, {Name: long + "-two", Commit: "bbbbbbbb"}},
			Remote: []appgit.Branch{{Name: long + "-três", Remote: "origin", Commit: "cccccccc"}},
		},
	}}
	if _, _, err := RenderBranchPlan(&out, plans, "6 months", time.Now()); err != nil {
		t.Fatal(err)
	}
	// The commit recreates the branch, so it is named whole too.
	for _, want := range []string{long + "-one", long + "-two", "origin/" + long + "-três", "aaaaaaaa", "cccccccc"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("%s is not named whole:\n%s", want, out.String())
		}
	}
}

// A newline and a backslash followed by n are two names, and look it.
func TestNamesThatDifferLookDifferent(t *testing.T) {
	for _, pair := range [][2]string{
		{"cache\n.env", `cache\n.env`},
		{"tab\there", `tab\there`},
		{"latin-\xff", `latin-\xff`},
		// A name spelled as another's quoted form is not that other.
		{"a\nb", `"a\nb"`},
	} {
		if Printable(pair[0]) == Printable(pair[1]) {
			t.Errorf("%q and %q are both shown as %s", pair[0], pair[1], Printable(pair[0]))
		}
	}
	if got := Printable("team/orbit"); got != "team/orbit" {
		t.Errorf("a plain name is shown as %s", got)
	}
}

// A file named to move the cursor or clear the screen is shown, not obeyed.
func TestControlCharactersAreShownNotWritten(t *testing.T) {
	var out bytes.Buffer
	paths := []string{"z\x1b[2J\x1b[H", "a\rb", "c\nd"}
	if _, err := RenderCleanPlan(&out, []appgit.Result[appgit.Cleaned]{{
		Repository: appgit.Repository{Path: "/a", Name: "team\x1b[1A"}, Outcome: appgit.OutcomeChanged, Detail: appgit.Cleaned{Paths: paths},
	}}); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(out.String(), "\x1b\r") || strings.Count(out.String(), "\n") != 6 {
		t.Errorf("printed %q", out.String())
	}
	for _, want := range []string{`z\x1b[2J\x1b[H`, `a\rb`, `c\nd`, `team\x1b[1A`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("%s is not shown: %q", want, out.String())
		}
	}
}

func these(repos []appgit.Repository) func() ([]appgit.Repository, error) {
	return func() ([]appgit.Repository, error) { return repos, nil }
}
