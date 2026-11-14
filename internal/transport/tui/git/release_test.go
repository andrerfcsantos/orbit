package git

import (
	"os/exec"
	"strings"
	"testing"
)

func TestShellQuoteReadsBackAsItIs(t *testing.T) {
	values := []string{"main", "feature/MTP-915", "=git", "a b", "it's", "$(touch x)", "~", "--version", "release-", ""}
	for _, shell := range []string{"sh", "zsh"} {
		if _, err := exec.LookPath(shell); err != nil {
			continue
		}
		for _, value := range values {
			out, err := exec.Command(shell, "-fc", "printf %s "+ShellQuote(value)).Output()
			if err != nil || string(out) != value {
				t.Errorf("%s read %s back as %q (%v)", shell, ShellQuote(value), out, err)
			}
		}
	}
	if got := ShellQuote("main"); strings.Contains(got, "'") {
		t.Errorf("a plain word is quoted: %s", got)
	}
}

func TestASubjectCannotRewriteTheScreen(t *testing.T) {
	if got := quote("fix\x1b[2J\x1b[Hall\r\u009b31m good"); got != `"fix[2J[Hall31m good"` {
		t.Errorf("quote = %q", got)
	}
}
