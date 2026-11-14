package cmd

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	"github.com/andrerfcsantos/orbit/internal/app"
	"github.com/andrerfcsantos/orbit/internal/bootstrap"
	cmdconfig "github.com/andrerfcsantos/orbit/internal/cmd/config"
	cmdgit "github.com/andrerfcsantos/orbit/internal/cmd/git"
	"github.com/andrerfcsantos/orbit/internal/cmd/internal/exit"
	cmdversion "github.com/andrerfcsantos/orbit/internal/cmd/version"
	platformconfig "github.com/andrerfcsantos/orbit/internal/platform/config"
	"github.com/andrerfcsantos/orbit/internal/platform/logging"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Execute runs the command tree and returns the process exit code.
//
// It returns a code rather than calling os.Exit so that deferred cleanup -- most
// importantly flushing and closing the log file -- actually runs.
//
// args are the command-line arguments excluding the program name. Passing them in,
// rather than letting cobra read os.Args, is what lets a test drive the whole tree
// in-process against its own streams.
func Execute(inputs Inputs, args ...string) int {
	manager := bootstrap.NewConfigManager()

	// The runtime does not exist until configuration is resolved, which cannot
	// happen until cobra has parsed the arguments that configure it. Commands
	// therefore close over this accessor and call it while running.
	var runtime *app.Runtime
	provider := app.RuntimeProvider(func() *app.Runtime { return runtime })

	// Until the configured logger exists, the entry is held against a minimal stderr
	// logger. It is retargeted before it is written, and only an invocation that can
	// open no log file at all ever writes to it.
	bootstrapLogger := logging.Bootstrap(bootstrap.AppName, inputs.Stderr)
	activeLogger := bootstrapLogger
	adopted := false
	// Deferred through a closure, not `defer activeLogger.Close()`: the latter would
	// bind the receiver now and close the bootstrap logger, leaving the configured
	// logger's file handle open and its buffered entry unflushed. The closure reads
	// the variable at return, after the retarget below has reassigned it.
	//
	// This runs after emitInvocation, so the canonical entry is written before its
	// destination is flushed and closed.
	defer func() { activeLogger.Close() }()

	// The canonical entry is created before cobra runs and emitted from a defer, so
	// every invocation produces exactly one -- including one that fails while parsing
	// flags, and one that cobra short-circuits (--help, --version), neither of which
	// reaches a command body.
	ctx, canonical := logging.NewCanonical(context.Background(), bootstrapLogger.Logger)
	canonical.Add(zap.String("event.kind", "cli_invocation"))
	started := time.Now()

	// Recorded by PersistentPreRunE, so the deferred emit below can name the command
	// that crashed. On a panic, ExecuteContextC never returns and its `executed`
	// result is unavailable, which would otherwise attribute every crash to the root.
	runningCommand := ""
	observe := func(path string) { runningCommand = path }

	// adopt retargets the entry at logger, keeping the fields already gathered, and
	// takes over responsibility for closing it.
	adopt := func(logger *logging.Logger) {
		adopted = true
		canonical.SetLogger(logger.Logger)
		activeLogger.Close()
		activeLogger = logger
	}
	// settle adopts a logger for an invocation that never reached PersistentPreRunE:
	// one cobra answers itself (--help) or rejects (a mistyped flag or command).
	// Configuration is resolved for it here, so its entry still goes to the log file,
	// or to stdout with --log-stdout, rather than onto the terminal after the help.
	// When configuration cannot be read, the defaults' log file holds the entry; the
	// error itself is printed to stderr by reportError.
	settle := func(cmd *cobra.Command) {
		if adopted {
			return
		}
		if logger, err := bootstrap.NewLogger(inputs, manager, cmd); err == nil {
			adopt(logger)
		} else if logger, err := bootstrap.DefaultLogger(inputs); err == nil {
			adopt(logger)
		}
	}

	root := newRootCommand(inputs, manager, provider, observe, func(next *app.Runtime, logger *logging.Logger) {
		runtime = next
		adopt(logger)
	})

	root.SetArgs(args)

	// The entry is emitted from a defer so a panic inside a command still produces
	// one, with the panic recorded, before the panic continues unwinding. Emit is
	// idempotent, so the normal path below emits once and this is then a no-op.
	//
	// The panic is deliberately not recovered: orbit does not know how to continue
	// past one, and swallowing it here would hide a crash and return a success code.
	var (
		command = root.CommandPath()
		code    = exit.Failure
	)
	defer func() {
		if recovered := recover(); recovered != nil {
			crashed := command
			if runningCommand != "" {
				crashed = runningCommand
			}
			canonical.Add(
				zap.String("error", fmt.Sprint(recovered)),
				zap.String("error.stack_trace", boundedStack()),
			)
			settle(root)
			emitInvocation(canonical, crashed, exit.Failure, time.Since(started),
				fmt.Errorf("panic: %v", recovered))
			// activeLogger.Close() (deferred above, so it runs after this) flushes the
			// entry before the process dies.
			panic(recovered)
		}
	}()

	executed, err := root.ExecuteContextC(ctx)
	if executed != nil {
		command = executed.CommandPath()
		settle(executed)
		if help, _ := executed.Flags().GetBool("help"); help && err == nil {
			canonical.Add(zap.Bool("cli.help", true))
			logging.Summarize(ctx, "printed its help")
		}
	} else {
		settle(root)
	}
	code = reportError(root, executed, err)
	emitInvocation(canonical, command, code, time.Since(started), err)
	return code
}

// maxPanicStackBytes bounds the stack recorded on a panic: enough frames to
// identify the fault, short of putting a goroutine dump in a log entry.
const maxPanicStackBytes = 8 << 10

func boundedStack() string {
	stack := debug.Stack()
	if len(stack) > maxPanicStackBytes {
		return string(stack[:maxPanicStackBytes]) + "\n... stack truncated"
	}
	return string(stack)
}

// Inputs aliases bootstrap.Inputs so main depends on one package.
type Inputs = bootstrap.Inputs

// extraCommands is a seam for tests that need a command with behavior the real tree
// has none of -- notably one that panics, so the panic path in Execute can be
// exercised as it actually runs rather than through a re-creation of it. It is
// package-private and empty in production.
var extraCommands []*cobra.Command

// runtimeSetter is called once the runtime has been built, so Execute can adopt
// the configured logger.
type runtimeSetter func(*app.Runtime, *logging.Logger)

// NewRootCommand builds the command tree. Exported so tests can drive it directly.
func NewRootCommand(
	inputs Inputs,
	manager *platformconfig.Manager,
	provider app.RuntimeProvider,
	setRuntime runtimeSetter,
) *cobra.Command {
	return newRootCommand(inputs, manager, provider, nil, setRuntime)
}

// newRootCommand additionally reports the command path once it is known, which
// Execute needs so a panic is attributed to the command that crashed.
func newRootCommand(
	inputs Inputs,
	manager *platformconfig.Manager,
	provider app.RuntimeProvider,
	observe func(string),
	setRuntime runtimeSetter,
) *cobra.Command {
	root := &cobra.Command{
		Use:   bootstrap.CommandName,
		Short: "orbit: personal tools and automations",
		Long: `orb is the command of orbit, a collection of personal tools and automations.

Every setting can come from a flag, an environment variable (ORBIT_*), or a
config file, in that order of precedence. Run "orb config" to see the
settings in effect and where they can be set from.`,

		// Errors and usage are printed by reportError, which decides based on the
		// kind of failure: a usage error should show usage, a runtime failure should
		// not bury the message under a wall of help text.
		SilenceUsage:  true,
		SilenceErrors: true,

		// Without a subcommand, print help rather than doing nothing silently.
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return exit.Usage(fmt.Errorf("unknown command %q", args[0]))
			}
			return cmd.Help()
		},

		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			runtime, logger, err := bootstrap.NewRuntime(inputs, manager, cmd)
			if err != nil {
				return err
			}
			if setRuntime == nil {
				// Nothing will adopt the logger, so close it here rather than leaking its
				// file handle. Reachable only through NewRootCommand, which is exported
				// for tests; Execute always supplies a setter.
				logger.Close()
				return errors.New("no runtime setter was provided")
			}
			setRuntime(runtime, logger)

			if observe != nil {
				observe(cmd.CommandPath())
			}

			// Annotate the canonical entry as soon as the command is known, so even a
			// command that fails immediately is attributable.
			logging.AddFields(cmd.Context(),
				zap.String("cli.command", cmd.CommandPath()),
				// Argument count, not the arguments: a tool's arguments can carry
				// identifiers or secrets, and the count is enough to tell invocations
				// apart. Individual commands add the specific fields worth recording.
				zap.Int("cli.args", len(args)),
			)
			return nil
		},
	}

	root.SetOut(inputs.Stdout)
	root.SetErr(inputs.Stderr)
	root.SilenceUsage = true

	// Logging settings are persistent: they apply to every command, and being able
	// to raise the log level on the command that is misbehaving is the point.
	manager.RegisterPersistentStringFlag(root, platformconfig.LogLevelKey)
	manager.RegisterPersistentStringFlag(root, platformconfig.LogFormatKey)
	manager.RegisterPersistentBoolFlag(root, platformconfig.LogStdoutKey)

	root.AddCommand(
		cmdversion.New(provider),
		cmdconfig.New(provider),
		cmdgit.New(provider, manager),
	)
	root.AddCommand(extraCommands...)

	return root
}

// reportError prints err and maps it to an exit code.
func reportError(root, executed *cobra.Command, err error) int {
	if err == nil {
		return exit.Success
	}

	var exitErr *exit.Error
	if errors.As(err, &exitErr) {
		if exitErr.Err != nil {
			fmt.Fprintln(root.ErrOrStderr(), "Error:", exitErr.Err)
		}
		// A usage error means the user does not know how to call the command, so show
		// them; a runtime failure means they called it correctly and it failed, where
		// usage text is noise.
		if exitErr.Code == exit.UsageCode {
			fmt.Fprintln(root.ErrOrStderr())
			printUsage(root, executed)
		}
		return exitErr.Code
	}

	// A flag the user mistyped is a usage problem, and cobra reports it as a plain
	// error. Classifying it here -- rather than at each call site -- keeps the exit
	// code meaningful: 2 means "you called this wrongly", 1 means "it ran and
	// failed".
	if isUsageError(err) {
		fmt.Fprintln(root.ErrOrStderr(), "Error:", err)
		fmt.Fprintln(root.ErrOrStderr())
		printUsage(root, executed)
		return exit.UsageCode
	}

	fmt.Fprintln(root.ErrOrStderr(), "Error:", err)
	return exit.Failure
}

// usageErrorPrefixes are the messages cobra and pflag produce for a malformed
// command line. They offer no typed error to match on, so the text is the only
// signal available.
var usageErrorPrefixes = []string{
	"unknown flag",
	"unknown shorthand flag",
	"unknown command",
	"flag needs an argument",
	"invalid argument",
	"bad flag syntax",
	"accepts ",
	"requires at least",
	"unknown help topic",
}

func isUsageError(err error) bool {
	message := err.Error()
	for _, prefix := range usageErrorPrefixes {
		if strings.HasPrefix(message, prefix) {
			return true
		}
	}
	return false
}

// emitInvocation writes the single canonical entry for this invocation.
//
// It runs from Execute rather than a cobra PostRun hook because those are skipped
// when a command returns an error -- which is exactly the invocation most worth
// having a log entry for.
func emitInvocation(canonical *logging.Canonical, command string, code int, elapsed time.Duration, err error) {
	took := logging.RoundDuration(elapsed)
	outcome := "success"
	level := zapcore.InfoLevel
	message := fmt.Sprintf("%s succeeded in %s", command, took)
	if summary := canonical.Summary(); summary != "" {
		message += ": " + summary
	}
	if err != nil {
		outcome = "failure"
		level = zapcore.ErrorLevel
		// The error text is recorded, so every error surfaced to a user must be free
		// of the values it was given -- see parseValue in platform/config, which
		// deliberately does not echo a rejected value.
		canonical.Add(zap.String("error", err.Error()))
		reason := err.Error()
		// An exit code with nothing to say, as when a lookup finds nothing, has said
		// it on stdout already; the summary says it here.
		if exitErr := (*exit.Error)(nil); errors.As(err, &exitErr) && exitErr.Err == nil {
			reason = canonical.Summary()
		}
		message = fmt.Sprintf("%s failed after %s with exit code %d", command, took, code)
		if code == exit.UsageCode {
			// A mistyped flag is the user's to fix, not orbit's failure.
			outcome = "usage_error"
			level = zapcore.WarnLevel
			message = fmt.Sprintf("%s was called wrongly (exit code %d)", command, code)
		}
		if reason != "" {
			message += ": " + reason
		}
	}

	canonical.Add(
		zap.String("cli.command", command),
		zap.Int("exit_code", code),
		zap.Duration("duration", elapsed),
		zap.String("event.outcome", outcome),
	)
	canonical.Emit(level, message)
}

// printUsage writes usage for the command that failed to stderr.
//
// cobra's Usage() goes to the command's output stream, which is stdout: usage
// printed as part of an error belongs on stderr, so that a caller redirecting
// stdout still gets a clean result and still sees the diagnostic.
func printUsage(root, executed *cobra.Command) {
	target := executed
	if target == nil {
		target = root
	}
	// Usage for the specific subcommand is what the user needs; the root's usage
	// would describe the whole tool when they mistyped one flag.
	fmt.Fprint(root.ErrOrStderr(), target.UsageString())
}
