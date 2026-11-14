package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	appdirs "github.com/andrerfcsantos/gappdirs"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Log encodings.
const (
	FormatJSON    = "json"
	FormatConsole = "console"
)

// Options describes the logger to build.
type Options struct {
	// Level is the minimum level emitted (debug, info, warn, error). An
	// unrecognized value falls back to info rather than failing: losing detail is
	// preferable to a process that will not start because of a log setting.
	Level string

	// Format selects the encoding, FormatJSON or FormatConsole. JSON is the
	// default because canonical entries are meant to be queried by field.
	Format string

	// Stdout sends entries to stdout instead of the per-user log file. Containers
	// need this (stdout is what gets collected); a CLI on a laptop must not have
	// it, or log entries would interleave with command output.
	Stdout bool

	// Service, Version and Commit are attached to every entry so an entry can be
	// tied back to the build that produced it.
	Service string
	Version string
	Commit  string

	// LogDir and LogFile override the log file location. Empty means the OS
	// per-user log directory.
	LogDir  string
	LogFile string

	// Rotation limits for the log file.
	MaxSizeMB  int
	MaxBackups int
	MaxAgeDays int
	Compress   bool
}

// Logger is a zap logger paired with the cleanup for its destination.
type Logger struct {
	*zap.Logger

	// closers releases the underlying writers. Sync alone does not close the log
	// file.
	closers []io.Closer
}

// Close flushes and releases the logger's destinations. Safe on a nil Logger so
// callers can defer it immediately after construction.
func (l *Logger) Close() {
	if l == nil {
		return
	}
	if l.Logger != nil {
		// Sync on a terminal or an already-closed handle returns an error that says
		// nothing about whether entries were written, and there is nowhere useful to
		// report it from a logging teardown, so it is dropped deliberately.
		_ = l.Logger.Sync()
	}
	for _, closer := range l.closers {
		_ = closer.Close()
	}
	l.closers = nil
}

// New builds a logger from opts.
//
// It returns an error only when a requested destination cannot be opened, since
// that means entries would be silently lost. Everything else (an unknown level or
// format) degrades to a default.
func New(opts Options) (*Logger, error) {
	encoder := buildEncoder(opts.Format)
	level := parseLevel(opts.Level)

	var (
		writer  zapcore.WriteSyncer
		closers []io.Closer
	)
	if opts.Stdout {
		// Lock stdout: concurrent handlers write entries through the same core, and
		// unsynchronized writes would interleave into corrupt lines.
		writer = zapcore.Lock(os.Stdout)
	} else {
		file, err := openLogFile(opts)
		if err != nil {
			return nil, err
		}
		writer = zapcore.AddSync(file)
		closers = append(closers, file)
	}

	// No AddStacktrace: a canonical entry is emitted at error level for any failed
	// unit of work, including ordinary user mistakes like a mistyped config value.
	// Attaching a stack to those buries the message under frames that describe the
	// logging call site rather than the fault. Where a stack genuinely helps -- a
	// panic -- the handler captures a bounded one into an explicit field.
	logger := zap.New(
		zapcore.NewCore(encoder, writer, level),
		zap.ErrorOutput(zapcore.Lock(os.Stderr)),
	)

	base := make([]zap.Field, 0, 3)
	if opts.Service != "" {
		base = append(base, zap.String("service", opts.Service))
	}
	if opts.Version != "" {
		base = append(base, zap.String("version", opts.Version))
	}
	if opts.Commit != "" {
		base = append(base, zap.String("commit", opts.Commit))
	}
	if len(base) > 0 {
		logger = logger.With(base...)
	}

	return &Logger{Logger: logger, closers: closers}, nil
}

// Bootstrap returns the logger used before configuration has been resolved.
//
// It writes to w in the console encoding: at that point the configured level,
// format and destination are unknown, but a failure to load configuration -- or an
// invocation cobra short-circuits before configuration is read, such as --help --
// still has to produce its canonical entry.
//
// The level is info rather than warn so that those short-circuited invocations are
// recorded rather than silently dropped by the level check inside Emit. Nothing
// else logs through this logger, so this costs one line on an invocation that never
// reached a command.
//
// The destination is a parameter rather than os.Stderr so that a caller which was
// handed its streams -- a test, or an embedding process -- gets these entries too
// instead of having them escape to the real stderr.
func Bootstrap(service string, w io.Writer) *Logger {
	if w == nil {
		w = os.Stderr
	}
	encoder := buildEncoder(FormatConsole)
	logger := zap.New(zapcore.NewCore(encoder, zapcore.Lock(zapcore.AddSync(w)), zapcore.InfoLevel))
	if service != "" {
		logger = logger.With(zap.String("service", service))
	}
	return &Logger{Logger: logger}
}

// Nop returns a logger that discards everything. It is the default used by tests
// and by FromContext when no logger is attached.
func Nop() *zap.Logger { return zap.NewNop() }

// Discard returns a Logger that discards everything, for tests.
func Discard() *Logger { return &Logger{Logger: Nop()} }

// LogFilePath returns the log file a non-stdout logger would write to, so a
// command can tell the user where its logs went.
func LogFilePath(opts Options) string {
	dir := opts.LogDir
	if dir == "" {
		dir = appdirs.NewUserResolver(opts.Service).LogDir()
	}
	name := opts.LogFile
	if name == "" {
		name = opts.Service + ".log"
	}
	return filepath.Join(dir, name)
}

// openLogFile opens the rotating log file, creating its directory.
func openLogFile(opts Options) (io.WriteCloser, error) {
	path := LogFilePath(opts)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create log directory %s: %w", filepath.Dir(path), err)
	}
	// lumberjack opens the file on its first write, by when an entry that cannot be
	// written is lost; opening it here makes an unwritable file an error now, while
	// the caller can still log elsewhere.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log file %s: %w", path, err)
	}
	_ = file.Close()

	maxSize := opts.MaxSizeMB
	if maxSize <= 0 {
		maxSize = 10
	}
	maxBackups := opts.MaxBackups
	if maxBackups <= 0 {
		maxBackups = 3
	}
	maxAge := opts.MaxAgeDays
	if maxAge <= 0 {
		maxAge = 28
	}

	// Rotation matters more for a local CLI than for a container: nothing else
	// prunes this file, and it is appended to by every invocation.
	return &lumberjack.Logger{
		Filename:   path,
		MaxSize:    maxSize,
		MaxBackups: maxBackups,
		MaxAge:     maxAge,
		Compress:   opts.Compress,
	}, nil
}

func buildEncoder(format string) zapcore.Encoder {
	cfg := zapcore.EncoderConfig{
		TimeKey:        "ts",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      zapcore.OmitKey,
		MessageKey:     "message",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.MillisDurationEncoder,
	}

	if strings.EqualFold(strings.TrimSpace(format), FormatConsole) {
		cfg.EncodeLevel = zapcore.CapitalColorLevelEncoder
		return zapcore.NewConsoleEncoder(cfg)
	}
	return zapcore.NewJSONEncoder(cfg)
}

func parseLevel(level string) zapcore.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return zapcore.DebugLevel
	case "warn", "warning":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	case "fatal":
		return zapcore.FatalLevel
	default:
		return zapcore.InfoLevel
	}
}
