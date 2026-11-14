// Package git renders orbit's batch git for the terminal: a live view of every
// repository being worked on and of the whole batch, a line per repository as it
// finishes, and the tables and plans the commands print.
package git

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	appgit "github.com/andrerfcsantos/orbit/internal/app/git"
	"github.com/andrerfcsantos/orbit/internal/transport/tui/styles"
	"github.com/charmbracelet/x/ansi"
)

// Labels name outcomes as an operation would: "updated", "up to date".
type Labels struct {
	Changed, Unchanged string
}

// Batch is how a batch is shown while it runs.
type Batch struct {
	// Title says what the batch does, as the live view's heading: "Updating".
	Title  string
	Labels Labels
	Out    io.Writer
	// In is where the live view reads keys from; nil reads none.
	In io.Reader
	// Live shows the live view, for a terminal. Otherwise a line is written per
	// repository as it finishes.
	Live bool
	// Quiet writes nothing, as --json wants.
	Quiet bool
	// Hide writes no line per repository, for a plan printed whole once it is made.
	Hide bool
	// Cancel stops the batch, when the person at the live view asks to.
	Cancel context.CancelFunc
}

// Describe says what an operation did in one repository. Run adds why it was
// skipped or failed.
type Describe[T any] func(appgit.Result[T]) string

// Run finds the repositories, runs work over them and shows its progress.
func Run[T any](
	batch Batch,
	find func() ([]appgit.Repository, error),
	describe Describe[T],
	work func([]appgit.Repository, appgit.Progress[T]) []appgit.Result[T],
) ([]appgit.Result[T], error) {
	if batch.Quiet {
		repos, err := find()
		if err != nil {
			return nil, err
		}
		return work(repos, nil), nil
	}
	s := styles.For(batch.Out)
	if !batch.Live {
		repos, err := find()
		if err != nil {
			return nil, err
		}
		width := nameWidth(repos)
		var mu sync.Mutex
		var reached reached
		return work(repos, func(event appgit.Event[T]) {
			if !reached.shown(event.Kind, event.Repository.Path) || batch.Hide {
				return
			}
			line := Line(s, *event.Result, width, describe)
			mu.Lock()
			defer mu.Unlock()
			fmt.Fprintln(batch.Out, line)
		}), nil
	}

	program := tea.NewProgram(newModel(batch, s), tea.WithOutput(batch.Out), tea.WithInput(batch.In),
		tea.WithColorProfile(s.Profile), tea.WithoutSignalHandler())
	var (
		results []appgit.Result[T]
		failure error
		worked  = make(chan struct{})
	)
	go func() {
		defer close(worked)
		defer program.Send(doneMsg{})
		repos, err := find()
		if err != nil {
			failure = err
			return
		}
		width := nameWidth(repos)
		program.Send(foundMsg{total: len(repos), width: width})
		var reached reached
		results = work(repos, func(event appgit.Event[T]) {
			shown := reached.shown(event.Kind, event.Repository.Path)
			switch event.Kind {
			case appgit.EventStarted:
				program.Send(startedMsg{path: event.Repository.Path, name: event.Repository.Name})
			case appgit.EventStep:
				program.Send(stepMsg{path: event.Repository.Path, step: event.Step})
			case appgit.EventFinished:
				program.Send(finishedMsg{path: event.Repository.Path, outcome: event.Result.Outcome})
				// Sent rather than returned from Update as a command, which runs on a
				// goroutine of its own and could print after the view has quit, or
				// out of order. Send gives up once the program has stopped.
				if shown && !batch.Hide {
					program.Send(tea.Println(Line(s, *event.Result, width, describe))())
				}
			}
		})
	}()
	if _, err := program.Run(); err != nil {
		if batch.Cancel != nil {
			batch.Cancel()
		}
		<-worked
		return nil, err
	}
	<-worked
	return results, failure
}

// reached remembers the repositories whose work started, so that those a stopped
// batch never reached are counted rather than listed one by one.
type reached struct {
	mu      sync.Mutex
	started map[string]bool
}

// shown reports whether an event finishes a repository whose work started.
func (r *reached) shown(kind appgit.EventKind, path string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch kind {
	case appgit.EventStarted:
		if r.started == nil {
			r.started = make(map[string]bool)
		}
		r.started[path] = true
	case appgit.EventFinished:
		return r.started[path]
	}
	return false
}

// maxNameWidth caps the column repositories' names are padded to.
const maxNameWidth = 40

func nameWidth(repos []appgit.Repository) int {
	width := 0
	for _, repo := range repos {
		width = max(width, lipgloss.Width(repo.Name))
	}
	return min(width, maxNameWidth)
}

// Line is how one repository's result is shown: a mark, its name, what was done
// and why not, and its notes beneath.
func Line[T any](s styles.Styles, result appgit.Result[T], width int, describe Describe[T]) string {
	text := describe(result)
	switch result.Outcome {
	case appgit.OutcomeSkipped, appgit.OutcomeCancelled:
		text = join(text, Printable(result.Reason))
	case appgit.OutcomeFailed:
		text = join(text, s.Error.Render(Printable(result.Error)))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s  %s", mark(s, result.Outcome), pad(result.Repository.Name, width), text)
	for _, note := range result.Notes {
		fmt.Fprintf(&b, "\n  %s %s", s.Warning.Render("↳"), Printable(note))
	}
	return b.String()
}

// Printable is what came from disk or from git as it can be shown, and told
// apart from anything else: a name with a control character in it -- a file
// called "\x1b[2J", which written as it is would clear the screen and hide what
// is about to be deleted -- a backslash, a byte that is no UTF-8, or a leading
// quote is shown quoted, so a newline and a backslash followed by n never look
// alike. Anything else is shown as it is.
func Printable(text string) string {
	plain := !strings.HasPrefix(text, `"`) && !strings.ContainsFunc(text, func(r rune) bool {
		return unicode.IsControl(r) || r == '\\' || r == utf8.RuneError
	})
	if plain {
		return text
	}
	return strconv.Quote(text)
}

func join(text, more string) string {
	switch {
	case text == "":
		return more
	case more == "":
		return text
	}
	return text + "; " + more
}

func mark(s styles.Styles, outcome appgit.Outcome) string {
	switch outcome {
	case appgit.OutcomeChanged:
		return s.Success.Render("✓")
	case appgit.OutcomeSkipped:
		return s.Warning.Render("!")
	case appgit.OutcomeFailed:
		return s.Error.Render("✗")
	case appgit.OutcomeCancelled:
		return s.Subtle.Render("–")
	}
	return s.Subtle.Render("·")
}

func pad(name string, width int) string {
	name = Printable(name)
	if lipgloss.Width(name) > width {
		return ansi.Truncate(name, width-1, "") + "…"
	}
	return name + strings.Repeat(" ", width-lipgloss.Width(name))
}

type (
	foundMsg struct {
		total, width int
	}
	startedMsg  struct{ path, name string }
	stepMsg     struct{ path, step string }
	finishedMsg struct {
		path    string
		outcome appgit.Outcome
	}
	doneMsg struct{}
)

// active is a repository being worked on.
type active struct {
	path, name, step string
	since            time.Time
}

type model struct {
	batch   Batch
	s       styles.Styles
	spinner spinner.Model
	bar     progress.Model
	started time.Time

	searching    bool
	total, done  int
	width, names int
	height       int
	tally        appgit.Tally
	active       []active

	stopping bool
	finished bool
}

// newModel's view is drawn in the palette's own colours, which the program
// downsamples to what the terminal shows.
func newModel(batch Batch, s styles.Styles) model {
	return model{
		batch: batch,
		s:     s,
		spinner: spinner.New(spinner.WithSpinner(spinner.MiniDot),
			spinner.WithStyle(lipgloss.NewStyle().Foreground(styles.Purple))),
		bar: progress.New(progress.WithColors(styles.Purple, styles.Green), progress.WithWidth(32),
			progress.WithoutPercentage()),
		started:   time.Now(),
		searching: true,
		width:     80,
		height:    24,
	}
}

func (m model) Init() tea.Cmd { return m.spinner.Tick }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// A terminal that does not know its size says 0.
		if msg.Width > 0 {
			m.width = msg.Width
			m.bar.SetWidth(max(10, min(32, msg.Width/3)))
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			if !m.stopping && m.batch.Cancel != nil {
				m.stopping = true
				m.batch.Cancel()
			}
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case progress.FrameMsg:
		var cmd tea.Cmd
		m.bar, cmd = m.bar.Update(msg)
		return m, cmd
	case foundMsg:
		m.searching, m.total, m.names = false, msg.total, msg.width
	case startedMsg:
		m.active = append(m.active, active{path: msg.path, name: msg.name, since: time.Now()})
	case stepMsg:
		for i := range m.active {
			if m.active[i].path == msg.path {
				m.active[i].step = msg.step
			}
		}
	case finishedMsg:
		for i := range m.active {
			if m.active[i].path == msg.path {
				m.active = append(m.active[:i], m.active[i+1:]...)
				break
			}
		}
		m.done++
		switch msg.outcome {
		case appgit.OutcomeChanged:
			m.tally.Changed++
		case appgit.OutcomeUnchanged:
			m.tally.Unchanged++
		case appgit.OutcomeSkipped:
			m.tally.Skipped++
		case appgit.OutcomeFailed:
			m.tally.Failed++
		case appgit.OutcomeCancelled:
			m.tally.Cancelled++
		}
		return m, m.bar.SetPercent(float64(m.done) / float64(max(1, m.total)))
	case doneMsg:
		m.finished = true
		return m, tea.Quit
	}
	return m, nil
}

func (m model) View() tea.View { return tea.NewView(m.content()) }

func (m model) content() string {
	if m.finished {
		return ""
	}
	var b strings.Builder
	elapsed := time.Since(m.started).Round(100 * time.Millisecond)
	if m.searching {
		fmt.Fprintf(&b, "%s Looking for repositories… %s\n", m.spinner.View(), m.s.Subtle.Render(elapsed.String()))
		return b.String()
	}

	fmt.Fprintf(&b, "%s %s %s\n", m.spinner.View(), m.s.Title.Render(m.batch.Title),
		m.s.Subtle.Render(fmt.Sprintf("· %d %s", m.total, plural(m.total, "repository", "repositories"))))
	rows := max(1, min(len(m.active), m.height-6, 12))
	for _, repo := range m.active[:min(rows, len(m.active))] {
		line := fmt.Sprintf("  %s  %s", pad(repo.name, m.names), m.s.Subtle.Render(repo.step))
		if took := time.Since(repo.since); took >= 2*time.Second {
			line += m.s.Subtle.Render(fmt.Sprintf("  %ds", int(took.Seconds())))
		}
		b.WriteString(ansi.Truncate(line, m.width, "…") + "\n")
	}
	if more := len(m.active) - rows; more > 0 {
		fmt.Fprintf(&b, "  %s\n", m.s.Subtle.Render(fmt.Sprintf("… and %d more", more)))
	}

	counts := []string{m.s.Value.Render(fmt.Sprintf("%d/%d", m.done, m.total))}
	counts = append(counts, Counts(m.s, m.batch.Labels, m.tally)...)
	counts = append(counts, m.s.Subtle.Render(elapsed.String()))
	fmt.Fprintf(&b, "\n%s  %s\n", m.bar.View(), strings.Join(counts, m.s.Subtle.Render(" · ")))
	if m.stopping {
		b.WriteString(m.s.Warning.Render("Stopping: waiting for the git already running to stop") + "\n")
	} else {
		b.WriteString(m.s.Subtle.Render("ctrl+c to stop") + "\n")
	}
	return b.String()
}

// Counts names how many repositories ended each way, leaving out the outcomes
// none did.
func Counts(s styles.Styles, labels Labels, tally appgit.Tally) []string {
	var counts []string
	for _, part := range []struct {
		count int
		label string
		style lipgloss.Style
	}{
		{tally.Changed, labels.Changed, s.Success},
		{tally.Unchanged, labels.Unchanged, s.Value},
		{tally.Skipped, "skipped", s.Warning},
		{tally.Failed, "failed", s.Error},
		{tally.Cancelled, "stopped", s.Subtle},
	} {
		if part.count > 0 {
			counts = append(counts, part.style.Render(fmt.Sprintf("%d %s", part.count, part.label)))
		}
	}
	return counts
}

func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}
