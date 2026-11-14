package bootstrap

import (
	"fmt"
	"io"

	"github.com/andrerfcsantos/orbit/internal/app"
	"github.com/andrerfcsantos/orbit/internal/platform/build"
	"github.com/andrerfcsantos/orbit/internal/platform/config"
	"github.com/andrerfcsantos/orbit/internal/platform/logging"
	"github.com/spf13/cobra"
)

// AppName is the application name used for config paths, log paths and the
// environment variable prefix.
const AppName = "orbit"

// CommandName is the name of the binary, and so of the root command: orbit is the
// project, orb is what you type.
const CommandName = "orb"

// EnvPrefix is the prefix for configuration environment variables, e.g.
// ORBIT_LOG_LEVEL sets log.level.
const EnvPrefix = "ORBIT"

// Inputs are the process-level values main supplies, fixed for the lifetime of
// the process. They are passed explicitly rather than read from globals so a test
// can run the whole command tree against its own streams.
type Inputs struct {
	Name      string
	BuildInfo *build.Info
	Stdout    io.Writer
	Stderr    io.Writer
}

// NewConfigManager builds the config manager for the process.
func NewConfigManager() *config.Manager {
	return config.New(
		AppName,
		config.WithEnvPrefix(EnvPrefix),
		config.WithCatalog(config.DefaultCatalog()),
	)
}

// NewRuntime resolves configuration and builds the runtime for the command about
// to run.
//
// The order is deliberate. Configuration is resolved first (files and
// environment), then the command's flags are bound over the top so a flag wins,
// and only then is the logger built -- because the level, format and destination
// are themselves configuration. A failure before that point leaves the caller to
// record it with DefaultLogger, or the bootstrap logger it already holds.
//
// The returned Logger owns its destination and must be closed by the caller.
func NewRuntime(inputs Inputs, manager *config.Manager, cmd *cobra.Command) (*app.Runtime, *logging.Logger, error) {
	logger, err := NewLogger(inputs, manager, cmd)
	if err != nil {
		return nil, nil, err
	}
	return &app.Runtime{
		Name:      inputs.Name,
		BuildInfo: buildInfo(inputs),
		Config:    manager,
		Logger:    logger.Logger,
		Stdout:    inputs.Stdout,
		Stderr:    inputs.Stderr,
	}, logger, nil
}

// NewLogger resolves configuration for cmd and builds the logger it describes,
// without the rest of the runtime. Execute uses it for an invocation cobra answers
// itself, such as --help, so that its entry goes where every other entry goes.
//
// The returned Logger owns its destination and must be closed by the caller.
func NewLogger(inputs Inputs, manager *config.Manager, cmd *cobra.Command) (*logging.Logger, error) {
	if manager == nil {
		return nil, fmt.Errorf("bootstrap: nil config manager")
	}
	if err := manager.Init(); err != nil {
		return nil, fmt.Errorf("load configuration: %w", err)
	}
	if err := manager.BindFlags(cmd); err != nil {
		return nil, fmt.Errorf("bind flags: %w", err)
	}
	logger, err := logging.New(loggerOptions(inputs, logging.Options{
		Level:  manager.GetString(config.KeyLogLevel),
		Format: manager.GetString(config.KeyLogFormat),
		Stdout: manager.GetBool(config.KeyLogStdout),
	}))
	if err != nil {
		return nil, fmt.Errorf("initialize logging: %w", err)
	}
	return logger, nil
}

// DefaultLogger builds the logger the defaults describe, the per-user log file,
// for when configuration itself cannot be read.
//
// The returned Logger owns its destination and must be closed by the caller.
func DefaultLogger(inputs Inputs) (*logging.Logger, error) {
	return logging.New(loggerOptions(inputs, logging.Options{}))
}

func loggerOptions(inputs Inputs, opts logging.Options) logging.Options {
	info := buildInfo(inputs)
	opts.Service = AppName
	opts.Version = info.Version
	opts.Commit = info.Commit
	return opts
}

func buildInfo(inputs Inputs) *build.Info {
	if inputs.BuildInfo == nil {
		return build.NewInfo("", "", "")
	}
	return inputs.BuildInfo
}
