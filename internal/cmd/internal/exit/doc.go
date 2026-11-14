// Package exit defines orbit's process exit codes and the error type that
// carries one.
//
// It sits below internal/cmd rather than inside it because the subcommand
// packages need these codes and internal/cmd imports those subcommands; sharing
// them from here avoids an import cycle.
package exit
