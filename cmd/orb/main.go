// Command orb is the CLI of orbit, a collection of personal tools and
// automations.
package main

import (
	"os"

	"github.com/andrerfcsantos/orbit/internal/bootstrap"
	"github.com/andrerfcsantos/orbit/internal/cmd"
	"github.com/andrerfcsantos/orbit/internal/platform/build"
)

// Build metadata, set at link time with -ldflags by scripts/build.sh and
// GoReleaser. The
// placeholder values are what a plain `go build` or `go run` reports.
//
//	-X main.version=... -X main.commit=... -X main.date=...
var (
	version = build.UnknownVersion
	commit  = build.UnknownCommit
	date    = build.UnknownDate
)

func main() {
	// Exiting here, rather than anywhere deeper, means every deferred cleanup in
	// the command tree has already run -- in particular the log file flush, without
	// which the canonical entry for this invocation could be lost.
	os.Exit(cmd.Execute(bootstrap.Inputs{
		Name:      bootstrap.AppName,
		BuildInfo: build.NewInfo(version, commit, date),
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
	}, os.Args[1:]...))
}
