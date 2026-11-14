// Package git runs the git binary for orbit, and finds the repositories beneath a
// directory.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// Runner runs git in a repository. Nothing it runs can prompt: there is no
// terminal for a credential or a passphrase to be asked at, so a remote needing
// one fails rather than hangs, and many runs at once never fight over the screen.
type Runner struct {
	// Binary is the git to run, "git" from PATH when empty.
	Binary string
	// Grace is how long a git stopped has to finish before it, and what it
	// started, is killed: 10 seconds when zero.
	Grace time.Duration
}

// Error is a git that ran and failed.
type Error struct {
	// Command is the git subcommand, such as "fetch".
	Command  string
	ExitCode int
	// Stderr is what git said, trimmed.
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	if message := e.Message(); message != "" {
		return "git " + e.Command + ": " + message
	}
	return "git " + e.Command + ": " + e.Err.Error()
}

func (e *Error) Unwrap() error { return e.Err }

// Message is why git failed: its first error, with the paths it lists beneath
// ("The following untracked working tree files would be overwritten by merge:
// .env"), or else the last thing it said.
func (e *Error) Message() string {
	lines := strings.Split(e.Stderr, "\n")
	for i, line := range lines {
		message, ok := strings.CutPrefix(line, "fatal: ")
		if !ok {
			message, ok = strings.CutPrefix(line, "error: ")
		}
		if !ok {
			continue
		}
		var listed []string
		for _, next := range lines[i+1:] {
			if !strings.HasPrefix(next, "\t") {
				break
			}
			listed = append(listed, strings.TrimSpace(next))
		}
		if len(listed) > 0 {
			message += " " + strings.Join(listed, ", ")
		}
		return message
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" && !strings.HasPrefix(line, "hint:") {
			return line
		}
	}
	return ""
}

// environment is added to orbit's own for every git it runs.
var environment = []string{
	"GIT_TERMINAL_PROMPT=0",
	// Set and empty, git asks no askpass program at all, nor falls back to
	// core.askPass or SSH_ASKPASS, any of which could open a window and wait.
	"GIT_ASKPASS=",
	"SSH_ASKPASS_REQUIRE=never",
	"GCM_INTERACTIVE=never",
	// Messages are read, so they are read in one language.
	"LC_ALL=C",
	// git status otherwise refreshes the index, taking its lock from under an
	// editor or another git working in the repository.
	"GIT_OPTIONAL_LOCKS=0",
	"GIT_MERGE_AUTOEDIT=no",
}

// repositoryVariables are those git rev-parse --local-env-vars lists: each
// points git at a repository other than the one it runs in, as a hook's
// environment or a shell exporting GIT_DIR would, so none is passed on.
var repositoryVariables = []string{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT",
	"GIT_OBJECT_DIRECTORY", "GIT_DIR", "GIT_WORK_TREE", "GIT_IMPLICIT_WORK_TREE", "GIT_GRAFT_FILE",
	"GIT_INDEX_FILE", "GIT_NO_REPLACE_OBJECTS", "GIT_REPLACE_REF_BASE", "GIT_PREFIX",
	"GIT_INTERNAL_SUPER_PREFIX", "GIT_SHALLOW_FILE", "GIT_COMMON_DIR",
	// These change what a pathspec matches, :(literal) ones too: with
	// GIT_ICASE_PATHSPECS, cleaning cache would clean CACHE beside it.
	"GIT_LITERAL_PATHSPECS", "GIT_GLOB_PATHSPECS", "GIT_NOGLOB_PATHSPECS", "GIT_ICASE_PATHSPECS",
}

// Run runs git with args in dir, a repository's top directory, and returns what
// it printed. A context ending interrupts git, as Ctrl-C would, so it removes its
// lock files before it goes.
//
// git never looks above dir: a .git it cannot read, or one removed since the
// repository was found, fails rather than leading git to a repository dir is
// inside, whose files it would then work on.
func (r Runner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	return r.run(ctx, dir, true, args)
}

// Climb runs git in dir as Run does, but lets git look for the repository dir is
// inside, as git run in a subdirectory does.
func (r Runner) Climb(ctx context.Context, dir string, args ...string) (string, error) {
	return r.run(ctx, dir, false, args)
}

func (r Runner) run(ctx context.Context, dir string, fence bool, args []string) (string, error) {
	binary := r.Binary
	if binary == "" {
		binary = "git"
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = dir
	cmd.Env = append(passedOn(os.Environ()), environment...)
	if fence {
		// The ceiling is a list: a parent named with the list separator in it,
		// such as /code/team:archive, would be several ceilings, none of them it.
		parent := filepath.Dir(dir)
		if strings.ContainsRune(parent, os.PathListSeparator) {
			return "", &Error{Command: firstOf(args), ExitCode: -1,
				Err: fmt.Errorf("git cannot be kept to a repository whose parent directory's name holds %q", os.PathListSeparator)}
		}
		cmd.Env = append(cmd.Env, "GIT_CEILING_DIRECTORIES="+parent)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	detach(cmd)
	cmd.WaitDelay = r.Grace
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = 10 * time.Second
	}

	release, err := start(cmd)
	if err == nil {
		err = cmd.Wait()
	}
	release(err != nil)
	if err == nil {
		return stdout.String(), nil
	}
	command := firstOf(args)
	// Stopped, failed, or done while something it started still holds its output
	// open past the grace -- which a failure's exit error hides -- nothing it
	// started outlives the run.
	reap(cmd)
	if ctx.Err() != nil {
		return stdout.String(), &Error{Command: command, Err: ctx.Err()}
	}
	failure := &Error{Command: command, ExitCode: -1, Stderr: strings.TrimSpace(stderr.String()), Err: err}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		failure.ExitCode = exitErr.ExitCode()
	}
	return stdout.String(), failure
}

// firstOf is the git subcommand args run, which errors name.
func firstOf(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// passedOn is the environment less repositoryVariables, matched as the
// platform does: on Windows, git_dir is GIT_DIR.
func passedOn(environ []string) []string {
	kept := make([]string, 0, len(environ))
	for _, variable := range environ {
		name, _, _ := strings.Cut(variable, "=")
		if !slices.ContainsFunc(repositoryVariables, func(stripped string) bool { return sameVariable(name, stripped) }) {
			kept = append(kept, variable)
		}
	}
	return kept
}

func sameVariable(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
