package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrerfcsantos/orbit/internal/cmd/internal/exit"
	"github.com/andrerfcsantos/orbit/internal/platform/build"
	"github.com/andrerfcsantos/orbit/internal/platform/logging"
	"github.com/spf13/cobra"
)

// run executes the command tree in-process and returns stdout, stderr and the
// exit code.
//
// HOME and the working directory are redirected to temp dirs so a test can never
// read or write the developer's real configuration. XDG_CONFIG_HOME covers Linux,
// HOME covers macOS.
func run(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Chdir(t.TempDir())

	// Logs would otherwise go to the per-user log file; stderr keeps them with the
	// test and asserts the canonical entry is produced.
	t.Setenv("ORBIT_LOG_STDOUT", "false")

	var out, errOut bytes.Buffer
	code = Execute(Inputs{
		Name:      "orbit",
		BuildInfo: build.NewInfo("v9.9.9", "cafe123", "2026-02-03T04:05:06Z"),
		Stdout:    &out,
		Stderr:    &errOut,
	}, args...)
	return out.String(), errOut.String(), code
}

func TestVersionCommandPrintsBuildInfo(t *testing.T) {
	stdout, _, code := run(t, "version")

	if code != exit.Success {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, want := range []string{"v9.9.9", "cafe123", "2026-02-03T04:05:06Z"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
}

func TestVersionCommandJSONIsUnstyled(t *testing.T) {
	stdout, _, code := run(t, "version", "--json")

	if code != exit.Success {
		t.Fatalf("exit = %d, want 0", code)
	}
	// Output to a buffer is not a terminal, so lipgloss must not emit escapes --
	// piping `orb version --json` into jq has to work.
	if strings.Contains(stdout, "\x1b[") {
		t.Errorf("JSON output contains ANSI escapes:\n%q", stdout)
	}
	if !strings.Contains(stdout, `"version": "v9.9.9"`) {
		t.Errorf("output is not the expected JSON:\n%s", stdout)
	}
}

func TestConfigListShowsDefaults(t *testing.T) {
	stdout, _, code := run(t, "config")

	if code != exit.Success {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, want := range []string{"log.level", "git.concurrency"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("config listing missing %q:\n%s", want, stdout)
		}
	}
}

func TestConfigShowsSingleKeyWithProvenance(t *testing.T) {
	stdout, _, code := run(t, "config", "log.level")

	if code != exit.Success {
		t.Fatalf("exit = %d, want 0", code)
	}
	// The point of the single-key form is telling the user where a value can come
	// from, not just what it is.
	for _, want := range []string{"log.level", "ORBIT_LOG_LEVEL", "--log-level"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
}

func TestConfigSetPersistsForLaterRuns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Chdir(t.TempDir())

	execute := func(args ...string) (string, int) {
		var out, errOut bytes.Buffer
		code := Execute(Inputs{
			Name:      "orbit",
			BuildInfo: build.NewInfo("v1", "c1", "d1"),
			Stdout:    &out,
			Stderr:    &errOut,
		}, args...)
		return out.String(), code
	}

	if _, code := execute("config", "log.level", "debug"); code != exit.Success {
		t.Fatalf("set exit = %d, want 0", code)
	}
	// A separate invocation is the real test of persistence: it re-reads the file
	// through the normal lookup path.
	stdout, code := execute("config", "log.level")
	if code != exit.Success {
		t.Fatalf("read-back exit = %d, want 0", code)
	}
	if !strings.Contains(stdout, "debug") {
		t.Errorf("persisted value not read back:\n%s", stdout)
	}
}

func TestConfigSetRejectsUnknownKeyWithUsageExit(t *testing.T) {
	_, stderr, code := run(t, "config", "sever.port", "9000")

	// A mistyped key must fail rather than persist a setting nothing reads.
	if code != exit.UsageCode {
		t.Errorf("exit = %d, want %d for an unknown key", code, exit.UsageCode)
	}
	if !strings.Contains(stderr, "unknown config key") {
		t.Errorf("stderr does not explain the failure:\n%s", stderr)
	}
}

func TestConfigSetAllowsUnknownKeyWithFlag(t *testing.T) {
	_, _, code := run(t, "config", "future.key", "value", "--allow-unknown")
	if code != exit.Success {
		t.Errorf("exit = %d, want 0 with --allow-unknown", code)
	}
}

func TestConfigRejectsTooManyArguments(t *testing.T) {
	_, stderr, code := run(t, "config", "a", "b", "c")

	if code != exit.UsageCode {
		t.Errorf("exit = %d, want %d", code, exit.UsageCode)
	}
	if !strings.Contains(stderr, "at most 2 arguments") {
		t.Errorf("stderr does not name the problem:\n%s", stderr)
	}
}

func TestEnvironmentOverridesPersistedValue(t *testing.T) {
	t.Setenv("ORBIT_LOG_LEVEL", "error")
	stdout, _, code := run(t, "config", "log.level")

	if code != exit.Success {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(stdout, "error") {
		t.Errorf("environment value did not win:\n%s", stdout)
	}
}

func TestFlagOverridesEnvironment(t *testing.T) {
	t.Setenv("ORBIT_LOG_LEVEL", "error")
	stdout, _, code := run(t, "--log-level", "debug", "config", "log.level")

	if code != exit.Success {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(stdout, "debug") {
		t.Errorf("flag did not outrank the environment:\n%s", stdout)
	}
}

func TestUnknownFlagExitsAsUsageError(t *testing.T) {
	_, stderr, code := run(t, "--no-such-flag", "version")

	if code != exit.UsageCode {
		t.Errorf("exit = %d, want %d", code, exit.UsageCode)
	}
	if !strings.Contains(stderr, "unknown flag") {
		t.Errorf("stderr does not name the bad flag:\n%s", stderr)
	}
	// A usage error should show the user how to call the command.
	if !strings.Contains(stderr, "Usage:") {
		t.Errorf("usage error did not print usage:\n%s", stderr)
	}
}

func TestUnknownCommandExitsAsUsageError(t *testing.T) {
	_, _, code := run(t, "no-such-command")
	if code != exit.UsageCode {
		t.Errorf("exit = %d, want %d", code, exit.UsageCode)
	}
}

func TestBareInvocationPrintsHelp(t *testing.T) {
	stdout, _, code := run(t)

	if code != exit.Success {
		t.Fatalf("exit = %d, want 0", code)
	}
	// Doing nothing silently is worse than showing what is available.
	if !strings.Contains(stdout, "Available Commands") {
		t.Errorf("bare invocation did not print help:\n%s", stdout)
	}
}

func TestCanonicalEntryWrittenToLogFileOnSuccess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Chdir(t.TempDir())

	var out, errOut bytes.Buffer
	code := Execute(Inputs{
		Name:      "orbit",
		BuildInfo: build.NewInfo("v1", "c1", "d1"),
		Stdout:    &out,
		Stderr:    &errOut,
	}, "version")
	if code != exit.Success {
		t.Fatalf("exit = %d, want 0", code)
	}

	// With the default destination, a CLI run must leave stdout clean for the
	// command's own output and put the entry in the per-user log file.
	if strings.Contains(out.String(), "event.kind") {
		t.Errorf("log output leaked into stdout:\n%s", out.String())
	}

	entries := readLogEntries(t, home)
	if len(entries) != 1 {
		t.Fatalf("log file holds %d entries, want exactly 1 per invocation:\n%s",
			len(entries), strings.Join(entries, "\n"))
	}
	for _, want := range []string{
		`"event.kind":"cli_invocation"`,
		`"cli.command":"orb version"`,
		`"exit_code":0`,
		`"event.outcome":"success"`,
		`"duration"`,
	} {
		if !strings.Contains(entries[0], want) {
			t.Errorf("canonical entry missing %s:\n%s", want, entries[0])
		}
	}
}

func TestCanonicalEntryDoesNotRecordArgumentValues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Chdir(t.TempDir())

	var out, errOut bytes.Buffer
	// A tool's arguments can carry identifiers or secrets, so the entry records how
	// many there were, never what they said.
	code := Execute(Inputs{
		Name:      "orbit",
		BuildInfo: build.NewInfo("v1", "c1", "d1"),
		Stdout:    &out,
		Stderr:    &errOut,
	}, "config", "log.level", "debug")
	if code != exit.Success {
		t.Fatalf("exit = %d, want 0", code)
	}

	for _, entry := range readLogEntries(t, home) {
		if strings.Contains(entry, "debug") {
			t.Errorf("canonical entry captured an argument value:\n%s", entry)
		}
	}
}

func TestCanonicalEntryEmittedOnFailure(t *testing.T) {
	// A flag parse failure happens before PersistentPreRunE resolves configuration.
	// This is the path cobra's own Post-run hooks skip entirely, which is why it is
	// asserted explicitly: the entry still goes to the log file, not to stderr.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Chdir(t.TempDir())

	var out, errOut bytes.Buffer
	code := Execute(Inputs{
		Name:      "orbit",
		BuildInfo: build.NewInfo("v1", "c1", "d1"),
		Stdout:    &out,
		Stderr:    &errOut,
	}, "--no-such-flag", "version")

	if code != exit.UsageCode {
		t.Fatalf("exit = %d, want %d", code, exit.UsageCode)
	}
	if strings.Contains(errOut.String(), "event.outcome") {
		t.Errorf("the canonical entry leaked onto stderr:\n%s", errOut.String())
	}
	entries := readLogEntries(t, home)
	if len(entries) != 1 {
		t.Fatalf("log file holds %d entries, want 1:\n%s", len(entries), strings.Join(entries, "\n"))
	}
	for _, want := range []string{
		`"event.kind":"cli_invocation"`,
		`"event.outcome":"usage_error"`,
		`"exit_code":2`,
		`"level":"warn"`,
		`was called wrongly (exit code 2): unknown flag: --no-such-flag`,
	} {
		if !strings.Contains(entries[0], want) {
			t.Errorf("canonical entry missing %s:\n%s", want, entries[0])
		}
	}
}

func TestHelpLeavesTheTerminalToTheHelp(t *testing.T) {
	// cobra answers --help itself, before PersistentPreRunE, so this is the
	// invocation that once printed its canonical entry after the help text.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Chdir(t.TempDir())

	var out, errOut bytes.Buffer
	code := Execute(Inputs{
		Name:      "orbit",
		BuildInfo: build.NewInfo("v1", "c1", "d1"),
		Stdout:    &out,
		Stderr:    &errOut,
	}, "--help")

	if code != exit.Success {
		t.Fatalf("exit = %d, want 0", code)
	}
	if errOut.Len() > 0 {
		t.Errorf("--help wrote to stderr:\n%s", errOut.String())
	}
	if strings.Contains(out.String(), "exit_code") {
		t.Errorf("the canonical entry leaked into the help:\n%s", out.String())
	}
	entries := readLogEntries(t, home)
	if len(entries) != 1 {
		t.Fatalf("log file holds %d entries, want 1:\n%s", len(entries), strings.Join(entries, "\n"))
	}
	for _, want := range []string{`"cli.help":true`, `"message":"orb succeeded in `, `: printed its help"`} {
		if !strings.Contains(entries[0], want) {
			t.Errorf("canonical entry missing %s:\n%s", want, entries[0])
		}
	}
}

func TestAnUnwritableLogFileFallsBackToStderr(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Chdir(t.TempDir())
	// A directory where the log file goes cannot be opened, even by root.
	if err := os.MkdirAll(logging.LogFilePath(logging.Options{Service: "orbit"}), 0o700); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	Execute(Inputs{
		Name:      "orbit",
		BuildInfo: build.NewInfo("v1", "c1", "d1"),
		Stdout:    &out,
		Stderr:    &errOut,
	}, "--help")

	// Nowhere else to write it: losing the entry would be worse than the terminal.
	if strings.Count(errOut.String(), "printed its help") != 1 {
		t.Errorf("want the entry once on stderr, got:\n%s", errOut.String())
	}
}

func TestHelpTurnedOffIsNotRecordedAsHelp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Chdir(t.TempDir())

	var out, errOut bytes.Buffer
	Execute(Inputs{
		Name:      "orbit",
		BuildInfo: build.NewInfo("v1", "c1", "d1"),
		Stdout:    &out,
		Stderr:    &errOut,
	}, "version", "--help=false")

	entries := readLogEntries(t, home)
	if len(entries) != 1 || strings.Contains(entries[0], "cli.help") || strings.Contains(entries[0], "printed its help") {
		t.Errorf("version --help=false ran the command, but its entry says help:\n%s", strings.Join(entries, "\n"))
	}
}

func TestUnreadableConfigIsLoggedToTheDefaultLogFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	local := t.TempDir()
	t.Chdir(local)
	if err := os.MkdirAll(filepath.Join(local, ".orbit", "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, ".orbit", "config", "config.yaml"), []byte("log: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	code := Execute(Inputs{
		Name:      "orbit",
		BuildInfo: build.NewInfo("v1", "c1", "d1"),
		Stdout:    &out,
		Stderr:    &errOut,
	}, "version")

	if code != exit.Failure {
		t.Fatalf("exit = %d, want %d", code, exit.Failure)
	}
	// The user is told once, by reportError; the entry goes where entries go.
	if !strings.Contains(errOut.String(), "Error: load configuration") {
		t.Errorf("stderr does not report the configuration error:\n%s", errOut.String())
	}
	if strings.Contains(errOut.String(), "event.outcome") {
		t.Errorf("the canonical entry leaked onto stderr:\n%s", errOut.String())
	}
	entries := readLogEntries(t, home)
	if len(entries) != 1 || !strings.Contains(entries[0], `"event.outcome":"failure"`) {
		t.Fatalf("want one failure entry in the default log file, got:\n%s", strings.Join(entries, "\n"))
	}
}

// readLogEntries returns the lines of the orbit log file under home, if any.
func readLogEntries(t *testing.T, home string) []string {
	t.Helper()
	var lines []string
	_ = filepath.Walk(home, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() || filepath.Base(path) != "orbit.log" {
			return nil
		}
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		for _, line := range strings.Split(strings.TrimSpace(string(contents)), "\n") {
			if line != "" {
				lines = append(lines, line)
			}
		}
		return nil
	})
	return lines
}

func TestPanickingCommandStillEmitsCanonicalEntry(t *testing.T) {
	// Drives the real Execute, so the production defer is what is under test. An
	// earlier version of this test rebuilt Execute's arrangement inline and passed
	// even with that defer deleted -- it was asserting that its own scaffold worked.
	panicking := &cobra.Command{
		Use:  "panic-probe",
		RunE: func(cmd *cobra.Command, args []string) error { panic("kaboom") },
	}
	extraCommands = []*cobra.Command{panicking}
	t.Cleanup(func() { extraCommands = nil })

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Chdir(t.TempDir())

	var out, errOut bytes.Buffer
	func() {
		// A panic must keep propagating: orbit cannot continue past one, and
		// swallowing it would hide a crash behind a success code.
		defer func() {
			if recover() == nil {
				t.Error("Execute swallowed the panic")
			}
		}()
		Execute(Inputs{
			Name:      "orbit",
			BuildInfo: build.NewInfo("v1", "c1", "d1"),
			Stdout:    &out,
			Stderr:    &errOut,
		}, "panic-probe")
	}()

	entries := readLogEntries(t, home)
	if len(entries) != 1 {
		t.Fatalf("a panicking command produced %d canonical entries, want 1:\n%s",
			len(entries), strings.Join(entries, "\n"))
	}
	entry := entries[0]
	for _, want := range []string{
		`"event.outcome":"failure"`,
		`"error":"panic: kaboom"`,
		`"error.stack_trace"`,
		// The crash must be attributed to the command that panicked, not the root.
		`"cli.command":"orb panic-probe"`,
	} {
		if !strings.Contains(entry, want) {
			t.Errorf("canonical entry missing %s:\n%s", want, entry)
		}
	}
}

func TestEveryInvocationEmitsExactlyOneCanonicalEntry(t *testing.T) {
	// The guarantee documented in docs/architecture.md. The interesting cases are the
	// ones that never reach a command body: a flag parse error, and the invocations
	// cobra short-circuits (--help), for which Execute resolves configuration itself.
	for _, args := range [][]string{
		{"version"},
		{"config"},
		{"--help"},
		{"version", "--help"},
		{"--no-such-flag", "version"},
		{"no-such-command"},
		{"config", "a", "b", "c"},
	} {
		name := strings.Join(args, " ")
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			t.Chdir(t.TempDir())

			var out, errOut bytes.Buffer
			Execute(Inputs{
				Name:      "orbit",
				BuildInfo: build.NewInfo("v1", "c1", "d1"),
				Stdout:    &out,
				Stderr:    &errOut,
			}, args...)

			if total := len(readLogEntries(t, home)); total != 1 {
				t.Errorf("%q wrote %d canonical entries to the log file, want exactly 1\nstderr:\n%s",
					name, total, errOut.String())
			}
			if strings.Contains(errOut.String(), "exit_code") {
				t.Errorf("%q wrote its canonical entry to stderr:\n%s", name, errOut.String())
			}
		})
	}
}
