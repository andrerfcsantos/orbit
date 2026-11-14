package styles

import (
	"image/color"
	"io"
	"os"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

// Styles is the palette used to render command output.
type Styles struct {
	Title   lipgloss.Style
	Key     lipgloss.Style
	Value   lipgloss.Style
	Subtle  lipgloss.Style
	Success lipgloss.Style
	Warning lipgloss.Style
	Error   lipgloss.Style

	// Profile is what the writer can show: NoTTY for a pipe or a file.
	Profile colorprofile.Profile
}

// The palette's colours.
var (
	Purple = lipgloss.Color("#7D56F4")
	Grey   = lipgloss.Color("#6C6C6C")
	Green  = lipgloss.Color("#04B575")
	Amber  = lipgloss.Color("#FFB454")
	Red    = lipgloss.Color("#FF4672")
)

// For builds styles appropriate to a writer.
//
// lipgloss renders every colour it is given, so the styles are made for what
// the writer can show, from the writer itself and the environment (NO_COLOR,
// CLICOLOR_FORCE, TERM): colours a terminal can show, and nothing at all for a
// pipe or a file. A CLI whose output is routinely piped into jq or grep must not
// write escape codes there -- in that stream they are corruption, not decoration.
func For(w io.Writer) Styles {
	profile := colorprofile.Detect(w, os.Environ())
	// colorprofile reads NO_COLOR as a boolean, so NO_COLOR=yes would keep the
	// colours; any value turns them off (no-color.org), as CLICOLOR=0 does.
	noColor := os.Getenv("NO_COLOR") != "" || os.Getenv("CLICOLOR") == "0" && os.Getenv("CLICOLOR_FORCE") == ""
	if noColor && profile > colorprofile.ASCII {
		profile = colorprofile.ASCII
	}
	s := Styles{Profile: profile}
	if profile == colorprofile.NoTTY {
		plain := lipgloss.NewStyle()
		s.Title, s.Key, s.Value, s.Subtle, s.Success, s.Warning, s.Error = plain, plain, plain, plain, plain, plain, plain
		return s
	}
	s.Title = lipgloss.NewStyle().Bold(true).Foreground(s.Color(Purple))
	s.Key = lipgloss.NewStyle().Bold(true).Foreground(s.Color(Purple))
	s.Value = lipgloss.NewStyle()
	s.Subtle = lipgloss.NewStyle().Foreground(s.Color(Grey))
	s.Success = lipgloss.NewStyle().Bold(true).Foreground(s.Color(Green))
	s.Warning = lipgloss.NewStyle().Bold(true).Foreground(s.Color(Amber))
	s.Error = lipgloss.NewStyle().Bold(true).Foreground(s.Color(Red))
	return s
}

// Color is c as the writer can show it, or no colour at all, as with NO_COLOR.
func (s Styles) Color(c color.Color) color.Color {
	if converted := s.Profile.Convert(c); converted != nil {
		return converted
	}
	return lipgloss.NoColor{}
}
