package styles

import (
	"bytes"
	"testing"

	"github.com/charmbracelet/colorprofile"
)

func TestAPipeGetsNoEscapes(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "")
	var out bytes.Buffer
	s := For(&out)
	if s.Profile != colorprofile.NoTTY {
		t.Errorf("profile = %v", s.Profile)
	}
	if got := s.Error.Render("x") + s.Title.Render("y"); got != "xy" {
		t.Errorf("rendered %q", got)
	}
}

// Forced colour, as CLICOLOR_FORCE asks for, is what is wanted even in a pipe;
// NO_COLOR of any value then turns the colours off again, keeping bold.
func TestNoColorOfAnyValueTurnsColoursOff(t *testing.T) {
	t.Setenv("CLICOLOR_FORCE", "1")
	t.Setenv("TERM", "xterm-256color")
	for _, value := range []string{"1", "Y", "yes"} {
		t.Setenv("NO_COLOR", value)
		if s := For(&bytes.Buffer{}); s.Profile != colorprofile.ASCII {
			t.Errorf("NO_COLOR=%s: profile = %v", value, s.Profile)
		}
	}
	t.Setenv("NO_COLOR", "")
	if s := For(&bytes.Buffer{}); s.Profile <= colorprofile.ASCII {
		t.Errorf("forced: profile = %v", s.Profile)
	}
}
