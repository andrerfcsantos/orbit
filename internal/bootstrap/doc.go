// Package bootstrap turns the immutable process inputs from main into the
// per-command runtime the app layer executes against.
//
// It exists so that the ordering problem -- configuration determines how to log,
// but loading configuration can fail and that failure must be logged -- is solved
// in exactly one place. See NewRuntime.
package bootstrap
