package app

import (
	"io"

	"github.com/andrerfcsantos/orbit/internal/platform/build"
	"github.com/andrerfcsantos/orbit/internal/platform/config"
	"go.uber.org/zap"
)

// Runtime holds the dependencies resolved for the command being run: the
// configuration in effect, the configured logger, and the process's streams.
type Runtime struct {
	// Name is the application name used for config and log paths.
	Name string

	// BuildInfo describes the running build.
	BuildInfo *build.Info

	// Config is the resolved configuration.
	//
	// Values may include secrets from the environment or a config file, so this is
	// reachable from the CLI only. A future API would receive individual services
	// with narrow surfaces instead, never a Runtime.
	Config *config.Manager

	// Logger is the configured logger. Units of work should annotate the canonical
	// entry in the context rather than logging separately.
	Logger *zap.Logger

	// Stdout and Stderr are the process streams, passed explicitly so commands are
	// testable without capturing the globals.
	Stdout io.Writer
	Stderr io.Writer
}

// RuntimeProvider returns the Runtime for the command currently executing.
//
// Commands are constructed before any configuration exists -- cobra needs the
// command tree to parse the arguments that determine the configuration -- so they
// receive this accessor rather than a Runtime, and call it once they are running.
type RuntimeProvider func() *Runtime
