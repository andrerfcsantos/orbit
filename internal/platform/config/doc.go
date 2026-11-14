// Package config resolves orbit configuration from flags, environment
// variables and config files, and persists user-set values.
//
// It wraps a private viper instance rather than viper's package-level global so
// that two managers (a test's and the process's) cannot interfere, and so no
// unrelated package can mutate configuration by importing viper.
package config
