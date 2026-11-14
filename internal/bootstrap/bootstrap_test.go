package bootstrap

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrerfcsantos/orbit/internal/platform/build"
	"github.com/andrerfcsantos/orbit/internal/platform/config"
	"github.com/spf13/cobra"
	"go.uber.org/zap/zapcore"
)

func testInputs() Inputs {
	return Inputs{
		Name:      AppName,
		BuildInfo: build.NewInfo("v1.0.0", "abc123", "2026-01-01T00:00:00Z"),
		Stdout:    io.Discard,
		Stderr:    io.Discard,
	}
}

func TestNewRuntimeResolvesConfigAndLogger(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())

	manager := NewConfigManager()
	cmd := &cobra.Command{Use: "orbit"}
	manager.RegisterPersistentStringFlag(cmd, config.LogLevelKey)

	runtime, logger, err := NewRuntime(testInputs(), manager, cmd)
	if err != nil {
		t.Fatalf("NewRuntime() = %v", err)
	}
	defer logger.Close()

	if runtime.Config == nil || runtime.Logger == nil {
		t.Fatal("runtime is missing its config or logger")
	}
	if runtime.BuildInfo.Version != "v1.0.0" {
		t.Errorf("BuildInfo.Version = %q, want v1.0.0", runtime.BuildInfo.Version)
	}
}

func TestNewRuntimeRejectsNilManager(t *testing.T) {
	if _, _, err := NewRuntime(testInputs(), nil, &cobra.Command{Use: "orbit"}); err == nil {
		t.Error("NewRuntime(nil manager) = nil, want an error")
	}
}

func TestNewRuntimeReportsUnreadableConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	// A malformed config file must fail startup loudly. Silently ignoring it would
	// run the process with defaults while the operator believes their file applied.
	manager := config.New(AppName,
		config.WithEnvPrefix(EnvPrefix),
		config.WithUserConfigDir(home),
	)
	path := filepath.Join(home, config.DefaultConfigFile)
	if err := os.WriteFile(path, []byte("log:\n  level: [unclosed\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, _, err := NewRuntime(testInputs(), manager, &cobra.Command{Use: "orbit"})
	if err == nil {
		t.Fatal("NewRuntime() = nil, want an error for a malformed config file")
	}
	if !strings.Contains(err.Error(), "load configuration") {
		t.Errorf("error does not identify the stage: %v", err)
	}
}

func TestNewRuntimeConfigErrorDoesNotEchoFileContents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	manager := config.New(AppName,
		config.WithEnvPrefix(EnvPrefix),
		config.WithUserConfigDir(home),
	)
	// The config file is the designated home for secrets, so a parse failure must not
	// quote the offending line back into an error that reaches stderr and the log.
	const secret = "SUPER-SECRET-TOKEN"
	body := "log:\n  level: " + secret + "\n  bad: [unclosed\n"
	if err := os.WriteFile(filepath.Join(home, config.DefaultConfigFile), []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, _, err := NewRuntime(testInputs(), manager, &cobra.Command{Use: "orbit"})
	if err == nil {
		t.Fatal("NewRuntime() = nil, want a parse error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("config parse error echoes file contents: %v", err)
	}
}

func TestNewRuntimeHonoursConfiguredLogLevel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	// The logger must be built from resolved configuration, which is the whole reason
	// it cannot be constructed before cobra parses.
	t.Setenv(EnvPrefix+"_LOG_LEVEL", "error")

	manager := NewConfigManager()
	runtime, logger, err := NewRuntime(testInputs(), manager, &cobra.Command{Use: "orbit"})
	if err != nil {
		t.Fatalf("NewRuntime() = %v", err)
	}
	defer logger.Close()

	if runtime.Config.GetString(config.KeyLogLevel) != "error" {
		t.Errorf("log level = %q, want error", runtime.Config.GetString(config.KeyLogLevel))
	}
	if logger.Core().Enabled(zapInfo) {
		t.Error("logger emits info although the configured level is error")
	}
}

// zapInfo is spelled out here to keep the zapcore import local to this use.
const zapInfo = zapcore.InfoLevel
