package git

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/andrerfcsantos/orbit/internal/transport/tui/styles"
)

// ErrCancelled is a question left unanswered, with Esc or Ctrl-C.
var ErrCancelled = errors.New("stopped: nothing was changed")

// Prompter asks questions at a terminal, each once, leaving its answer on the
// screen.
type Prompter struct {
	Ctx context.Context
	In  io.Reader
	Out io.Writer
}

// Option is an answer offered.
type Option struct {
	Label string
	// Detail is what it means, such as the tag it makes.
	Detail string
}

// Select asks which option, with preselected highlighted first, and returns the
// index of the one chosen.
func (p Prompter) Select(question string, options []Option, preselected int) (int, error) {
	m := &selectModel{prompt: p.newPrompt(question), options: options, cursor: max(0, min(preselected, len(options)-1))}
	if err := p.run(m); err != nil {
		return 0, err
	}
	return m.cursor, nil
}

// Input asks for a line of text, beginning with initial, until validate takes
// it.
func (p Prompter) Input(question, initial string, validate func(string) error) (string, error) {
	m := &inputModel{prompt: p.newPrompt(question), value: []rune(initial), validate: validate}
	if err := p.run(m); err != nil {
		return "", err
	}
	return m.answerText, nil
}

// Confirm asks a yes or no question, whose answer is no unless it is yes.
func (p Prompter) Confirm(question string) (bool, error) {
	m := &confirmModel{prompt: p.newPrompt(question)}
	if err := p.run(m); err != nil {
		return false, err
	}
	return m.yes, nil
}

// answered is a question's model once it is answered or left.
type answered interface {
	tea.Model
	outcome() (done, cancelled bool)
}

func (p Prompter) run(m answered) error {
	ctx := p.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	s := styles.For(p.Out)
	program := tea.NewProgram(m, tea.WithOutput(p.Out), tea.WithInput(p.In), tea.WithContext(ctx),
		tea.WithColorProfile(s.Profile), tea.WithoutSignalHandler())
	if _, err := program.Run(); err != nil {
		if ctx.Err() != nil || errors.Is(err, tea.ErrInterrupted) {
			return ErrCancelled
		}
		return err
	}
	if done, cancelled := m.outcome(); cancelled || !done {
		return ErrCancelled
	}
	return nil
}

// prompt is what every question shares: the question, and how it ended.
type prompt struct {
	s                     styles.Styles
	question              string
	done, cancelled       bool
	answerText, errorText string
}

func (p Prompter) newPrompt(question string) prompt {
	return prompt{s: styles.For(p.Out), question: question}
}

func (p *prompt) outcome() (bool, bool) { return p.done, p.cancelled }

// leave reports whether a key leaves the question unanswered.
func (p *prompt) leave(key string) bool {
	if key == "ctrl+c" || key == "esc" || key == "ctrl+d" {
		p.cancelled = true
		return true
	}
	return false
}

// header is the question, and once it is answered, the answer beside it.
func (p *prompt) header() string {
	switch {
	case p.done:
		return p.s.Success.Render("✓") + " " + p.question + " " + p.s.Key.Render(p.answerText) + "\n"
	case p.cancelled:
		return p.s.Error.Render("✗") + " " + p.question + "\n"
	}
	return p.s.Key.Render("?") + " " + p.s.Title.Render(p.question) + "\n"
}

func (p *prompt) hint(text string) string {
	line := ""
	if p.errorText != "" {
		line += "  " + p.s.Error.Render(p.errorText) + "\n"
	}
	return line + "  " + p.s.Subtle.Render(text) + "\n"
}

type selectModel struct {
	prompt
	options []Option
	cursor  int
}

func (m *selectModel) Init() tea.Cmd { return nil }

func (m *selectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	if m.leave(key.String()) {
		return m, tea.Quit
	}
	switch k := key.String(); k {
	case "up", "k", "shift+tab":
		m.cursor = (m.cursor + len(m.options) - 1) % len(m.options)
	case "down", "j", "tab":
		m.cursor = (m.cursor + 1) % len(m.options)
	case "enter", "space":
		m.choose()
		return m, tea.Quit
	default:
		if len(k) == 1 && k[0] >= '1' && int(k[0]-'0') <= len(m.options) {
			m.cursor = int(k[0] - '1')
			m.choose()
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *selectModel) choose() {
	m.done = true
	m.answerText = m.options[m.cursor].Label
	if detail := m.options[m.cursor].Detail; detail != "" {
		m.answerText += " " + detail
	}
}

func (m *selectModel) View() tea.View {
	var b strings.Builder
	b.WriteString(m.header())
	if m.done || m.cancelled {
		return tea.NewView(b.String())
	}
	width := 0
	for _, option := range m.options {
		width = max(width, len(option.Label))
	}
	for i, option := range m.options {
		label := fmt.Sprintf("%-*s", width, option.Label)
		line := fmt.Sprintf("  %d. %s", i+1, label)
		if i == m.cursor {
			line = m.s.Key.Render(fmt.Sprintf("› %d. %s", i+1, label))
		}
		if option.Detail != "" {
			line += "  " + m.s.Subtle.Render(option.Detail)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString(m.hint("↑/↓ to move, enter or a number to choose, esc to stop"))
	return tea.NewView(b.String())
}

// inputModel edits one line, with the cursor at its end: what is asked for is a
// word, such as a branch's name.
type inputModel struct {
	prompt
	value    []rune
	validate func(string) error
}

func (m *inputModel) Init() tea.Cmd { return nil }

func (m *inputModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.PasteMsg:
		m.value = append(m.value, []rune(strings.TrimSpace(msg.Content))...)
		m.errorText = ""
	case tea.KeyPressMsg:
		if m.leave(msg.String()) {
			return m, tea.Quit
		}
		switch msg.String() {
		case "enter":
			value := strings.TrimSpace(string(m.value))
			if m.validate != nil {
				if err := m.validate(value); err != nil {
					m.errorText = err.Error()
					return m, nil
				}
			}
			m.done, m.answerText = true, value
			return m, tea.Quit
		case "backspace":
			if len(m.value) > 0 {
				m.value = m.value[:len(m.value)-1]
			}
		case "ctrl+u":
			m.value = nil
		case "ctrl+w", "alt+backspace":
			// The separators ending it first, so each press takes a word.
			trimmed := strings.TrimRight(string(m.value), " ./-")
			m.value = []rune(trimmed[:strings.LastIndexAny(trimmed, " ./-")+1])
		default:
			if msg.Text != "" {
				m.value = append(m.value, []rune(msg.Text)...)
			}
		}
		m.errorText = ""
	}
	return m, nil
}

func (m *inputModel) View() tea.View {
	if m.done || m.cancelled {
		return tea.NewView(m.header())
	}
	return tea.NewView(m.header() + "  " + m.s.Value.Render(string(m.value)) + m.s.Key.Render("▏") + "\n" +
		m.hint("enter to go on, esc to stop"))
}

type confirmModel struct {
	prompt
	yes bool
}

func (m *confirmModel) Init() tea.Cmd { return nil }

func (m *confirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	if m.leave(key.String()) {
		return m, tea.Quit
	}
	switch strings.ToLower(key.String()) {
	case "y":
		m.done, m.yes, m.answerText = true, true, "yes"
		return m, tea.Quit
	case "n", "enter":
		m.done, m.answerText = true, "no"
		return m, tea.Quit
	}
	return m, nil
}

func (m *confirmModel) View() tea.View {
	if m.done || m.cancelled {
		return tea.NewView(m.header())
	}
	return tea.NewView(strings.TrimSuffix(m.header(), "\n") + " " + m.s.Subtle.Render("[y/N]") + "\n")
}
