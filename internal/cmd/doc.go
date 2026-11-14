// Package cmd defines orbit's Cobra command tree.
//
// Commands here are thin: they declare flags and arguments, call an app service,
// and render the result. Behavior belongs to the service, so the same
// functionality can be reached from another surface later without being reimplemented.
package cmd
