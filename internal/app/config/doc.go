// Package config exposes configuration inspection and persistence as a use case.
//
// Resolved configuration can contain secrets, so this use case is meant for the
// CLI only: should orbit grow an API, it must not be wired into it. See
// docs/architecture.md.
package config
