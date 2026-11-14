// Package transport groups orbit's delivery adapters: today only the terminal,
// for the CLI. An HTTP API or a UI would be another adapter here.
//
// Adapters translate between a surface and the app services; they hold no feature
// logic of their own. Adding a surface means adding an adapter here, not moving
// behavior into it.
package transport
