// Package app holds orbit's application layer: the use cases that each tool
// exposes, plus the per-command Runtime they execute against.
//
// Packages under app are the single implementation of a tool's behavior, shared
// by every surface: the CLI today, an API or a UI should orbit grow one. They must
// not import cobra, net/http, or anything under internal/transport: a use case
// that reached for one of those could only be driven from that surface. The
// constraint is enforced by architecture_test.go rather than left to convention.
package app
