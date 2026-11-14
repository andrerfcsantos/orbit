package git

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func press(t *testing.T, m tea.Model, keys ...tea.KeyPressMsg) tea.Model {
	t.Helper()
	for _, key := range keys {
		m, _ = m.Update(key)
	}
	return m
}

func letter(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

var (
	down  = tea.KeyPressMsg{Code: tea.KeyDown}
	up    = tea.KeyPressMsg{Code: tea.KeyUp}
	enter = tea.KeyPressMsg{Code: tea.KeyEnter}
	esc   = tea.KeyPressMsg{Code: tea.KeyEscape}
)

func newSelect(preselected int) *selectModel {
	p := Prompter{Out: &bytes.Buffer{}}
	return &selectModel{prompt: p.newPrompt("What to release?"), cursor: preselected,
		options: []Option{{Label: "minor", Detail: "v1.5.0"}, {Label: "patch", Detail: "v1.4.1"}, {Label: "major", Detail: "v2.0.0"}}}
}

func TestSelectMovesAndChooses(t *testing.T) {
	m := press(t, newSelect(0), down, down, down, up, up, enter).(*selectModel)
	if done, cancelled := m.outcome(); !done || cancelled || m.cursor != 1 {
		t.Fatalf("cursor %d, done %v", m.cursor, done)
	}
	if view := m.View().Content; !strings.Contains(view, "✓") || !strings.Contains(view, "patch v1.4.1") {
		t.Errorf("view = %q", view)
	}

	m = press(t, newSelect(0), letter('3')).(*selectModel)
	if done, _ := m.outcome(); !done || m.cursor != 2 {
		t.Errorf("3 chose %d", m.cursor)
	}
	m = press(t, newSelect(2), esc).(*selectModel)
	if done, cancelled := m.outcome(); done || !cancelled {
		t.Error("esc chose")
	}
}

func TestInputIsEditedAndValidated(t *testing.T) {
	p := Prompter{Out: &bytes.Buffer{}}
	m := &inputModel{prompt: p.newPrompt("Which branch?"), value: []rune("main"),
		validate: func(value string) error {
			if value == "" {
				return errors.New("give a branch")
			}
			return nil
		}}
	m = press(t, m, tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}, enter).(*inputModel)
	if done, _ := m.outcome(); done || m.errorText != "give a branch" {
		t.Fatalf("empty taken, error %q", m.errorText)
	}
	m = press(t, m, letter('d'), letter('e'), letter('v'), letter('x'), tea.KeyPressMsg{Code: tea.KeyBackspace}, enter).(*inputModel)
	if done, _ := m.outcome(); !done || m.answerText != "dev" {
		t.Errorf("answer %q, done %v", m.answerText, done)
	}
}

func TestConfirmIsNoUnlessYes(t *testing.T) {
	p := Prompter{Out: &bytes.Buffer{}}
	for key, yes := range map[tea.KeyPressMsg]bool{letter('y'): true, letter('n'): false, enter: false} {
		m := press(t, &confirmModel{prompt: p.newPrompt("Push it?")}, letter('x'), key).(*confirmModel)
		if done, _ := m.outcome(); !done || m.yes != yes {
			t.Errorf("%v: yes %v, done %v", key, m.yes, done)
		}
	}
}

func TestDeletingAWordTakesOneAtATime(t *testing.T) {
	p := Prompter{Out: &bytes.Buffer{}}
	m := &inputModel{prompt: p.newPrompt("Which branch?"), value: []rune("team/feature/foo")}
	ctrlW := tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl}
	for _, want := range []string{"team/feature/", "team/", ""} {
		m = press(t, m, ctrlW).(*inputModel)
		if got := string(m.value); got != want {
			t.Fatalf("after ctrl+w: %q, want %q", got, want)
		}
	}
}
