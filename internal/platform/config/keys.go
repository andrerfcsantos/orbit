package config

// KeyDef describes one configuration key: its dotted name plus everything the
// other surfaces need to present and validate it.
//
// A key is declared once, here, and the flag, environment variable, default and
// help text are all derived from that single declaration (see flags.go). The
// alternative -- declaring a cobra flag in one place and a viper default in
// another -- lets the two drift silently, so that `--help` documents a default
// the code no longer uses.
type KeyDef struct {
	// Key is the dotted config key, e.g. "server.port". It is also the path
	// written into the YAML config file.
	Key string

	// FlagName is the cobra flag that overrides this key, without the leading
	// dashes, e.g. "port". Empty means the key has no flag and can only be set
	// via the environment or a config file.
	FlagName string

	// Shorthand is an optional one-letter alias for the flag, e.g. "r".
	Shorthand string

	// Description is used as the flag's usage string and shown by `orb config
	// <key>`, so it is written for both audiences.
	Description string

	// Default is the value used when nothing else supplies one. Its dynamic type
	// also determines how a string from the command line or a config file is
	// parsed (see Manager.parseValue), so a key intended to hold a number must
	// declare a numeric default rather than "0".
	Default any

	// Owner names the subsystem responsible for the key, e.g. "platform.logging".
	// It is descriptive: `orb config <key>` prints it so a reader can tell who
	// to ask about a setting.
	Owner string

	// Secret marks a key whose value must not be printed. `orb config` reports
	// whether such a key is set, never what it holds -- the command is routinely
	// run over a shared screen, and a provider credential scrolling past is not
	// recoverable.
	Secret bool
}

// Entry is one resolved key-value pair, as reported by Manager.List.
type Entry struct {
	Key   string
	Value any
}
