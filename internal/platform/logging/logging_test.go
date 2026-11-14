package logging

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestNewWritesToLogFile(t *testing.T) {
	dir := t.TempDir()
	logger, err := New(Options{
		Level:   "info",
		Service: "orbit-test",
		Version: "v1",
		Commit:  "abc",
		LogDir:  dir,
		LogFile: "test.log",
	})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	logger.Info("hello", zap.String("k", "v"))
	logger.Close()

	contents, err := os.ReadFile(filepath.Join(dir, "test.log"))
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	line := string(contents)
	// Base fields identify the build that produced an entry.
	for _, want := range []string{`"message":"hello"`, `"service":"orbit-test"`, `"version":"v1"`, `"commit":"abc"`} {
		if !strings.Contains(line, want) {
			t.Errorf("log line missing %s:\n%s", want, line)
		}
	}
}

func TestNewLevelFiltersEntries(t *testing.T) {
	dir := t.TempDir()
	logger, err := New(Options{Level: "warn", Service: "orbit-test", LogDir: dir, LogFile: "level.log"})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	defer logger.Close()

	if logger.Core().Enabled(zapcore.InfoLevel) {
		t.Error("info is enabled at level warn")
	}
	if !logger.Core().Enabled(zapcore.WarnLevel) {
		t.Error("warn is not enabled at level warn")
	}
}

func TestNewUnknownLevelFallsBackToInfo(t *testing.T) {
	dir := t.TempDir()
	// A bad log level must not stop the process: losing detail beats not starting.
	logger, err := New(Options{Level: "not-a-level", Service: "orbit-test", LogDir: dir, LogFile: "fallback.log"})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	defer logger.Close()

	if !logger.Core().Enabled(zapcore.InfoLevel) {
		t.Error("info is not enabled after falling back")
	}
	if logger.Core().Enabled(zapcore.DebugLevel) {
		t.Error("debug is enabled; the fallback should be info")
	}
}

func TestLogFilePathUsesServiceNameByDefault(t *testing.T) {
	got := LogFilePath(Options{Service: "orbit", LogDir: "/logs"})
	if want := filepath.Join("/logs", "orbit.log"); got != want {
		t.Errorf("LogFilePath() = %q, want %q", got, want)
	}
}

func TestCloseIsSafeOnNilAndRepeated(t *testing.T) {
	var logger *Logger
	logger.Close()

	real, err := New(Options{Service: "orbit-test", LogDir: t.TempDir(), LogFile: "close.log"})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	real.Close()
	// Execute closes the bootstrap logger and then the configured one; a repeated
	// close must not panic.
	real.Close()
}

func TestBootstrapLoggerRecordsInfoEntries(t *testing.T) {
	var out bytes.Buffer
	logger := Bootstrap("orbit-test", &out)
	defer logger.Close()

	// An invocation cobra short-circuits (--help) never resolves configuration, so
	// its canonical entry is emitted at info against this logger. A warn-level
	// bootstrap logger would drop it in Emit's level check and the invocation would
	// go unrecorded -- which it previously did.
	if !logger.Core().Enabled(zapcore.InfoLevel) {
		t.Error("bootstrap logger drops info entries; short-circuited invocations would go unlogged")
	}
	if logger.Core().Enabled(zapcore.DebugLevel) {
		t.Error("bootstrap logger emits debug; it should not be verbose before config is read")
	}

	logger.Info("bootstrap entry")
	if !strings.Contains(out.String(), "bootstrap entry") {
		t.Errorf("entry did not reach the supplied writer:\n%s", out.String())
	}
}

func TestConsoleFormatIsNotJSON(t *testing.T) {
	dir := t.TempDir()
	logger, err := New(Options{
		Format:  FormatConsole,
		Service: "orbit-test",
		LogDir:  dir,
		LogFile: "console.log",
	})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	logger.Info("plain output")
	logger.Close()

	contents, err := os.ReadFile(filepath.Join(dir, "console.log"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if strings.HasPrefix(strings.TrimSpace(string(contents)), "{") {
		t.Errorf("console format produced JSON:\n%s", contents)
	}
}

func TestNewFailsWhenTheLogFileCannotBeWritten(t *testing.T) {
	// The file must fail to open in New, not on the first entry, which would be
	// lost. A directory where the file goes cannot be opened even by root, whom
	// permissions would not stop.
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "orbit-test.log"), 0o700); err != nil {
		t.Fatal(err)
	}

	if logger, err := New(Options{Service: "orbit-test", LogDir: dir}); err == nil {
		logger.Close()
		t.Fatal("New() = nil error for a log file that cannot be written")
	}
}
