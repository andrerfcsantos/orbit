package config

import "sort"

// Known configuration keys. Referencing a key through these constants rather
// than a bare string means a typo is a compile error instead of a setting that
// silently never takes effect.
const (
	KeyLogLevel  = "log.level"
	KeyLogFormat = "log.format"
	KeyLogStdout = "log.stdout"
)

// Log format values accepted by KeyLogFormat.
const (
	LogFormatJSON    = "json"
	LogFormatConsole = "console"
)

// The definitions of the known keys. Each is exported so the command that owns a
// key can register its flag from the same definition the catalog validates
// against.
var (
	LogLevelKey = KeyDef{
		Key:         KeyLogLevel,
		FlagName:    "log-level",
		Description: "Minimum log level to emit (debug, info, warn, error)",
		Default:     "info",
		Owner:       "platform.logging",
	}
	LogFormatKey = KeyDef{
		Key:         KeyLogFormat,
		FlagName:    "log-format",
		Description: "Log encoding (json, console)",
		Default:     LogFormatJSON,
		Owner:       "platform.logging",
	}
	// LogStdoutKey switches logs from the per-user log file to stdout. Running on
	// infrastructure, logs must go to stdout to be collected; running as a CLI on
	// a laptop, they must not, or they would interleave with the command's actual
	// output. Hence a setting rather than a compile-time choice.
	LogStdoutKey = KeyDef{
		Key:         KeyLogStdout,
		FlagName:    "log-stdout",
		Description: "Write logs to stdout instead of the per-user log file (use in containers)",
		Default:     false,
		Owner:       "platform.logging",
	}
)

// Catalog is the set of configuration keys orbit knows about. It backs two
// behaviors: `orb config` can describe a key without it having been
// registered by the current command, and writes to unknown keys are refused so a
// typo in `orb config sever.port 9000` fails loudly instead of persisting a
// setting nothing reads.
type Catalog struct {
	defs map[string]KeyDef
}

// NewCatalog builds a catalog from the given definitions.
func NewCatalog(defs ...KeyDef) Catalog {
	c := Catalog{defs: make(map[string]KeyDef, len(defs))}
	for _, def := range defs {
		c.defs[def.Key] = def
	}
	return c
}

// DefaultCatalog returns the catalog of keys orbit ships with.
func DefaultCatalog() Catalog {
	defs := []KeyDef{
		LogLevelKey,
		LogFormatKey,
		LogStdoutKey,
	}
	defs = append(defs, gitKeys()...)
	return NewCatalog(defs...)
}

// Lookup returns the definition of a known key.
func (c Catalog) Lookup(key string) (KeyDef, bool) {
	def, ok := c.defs[key]
	return def, ok
}

// Keys returns every known definition, ordered by key so output is stable.
func (c Catalog) Keys() []KeyDef {
	defs := make([]KeyDef, 0, len(c.defs))
	for _, def := range c.defs {
		defs = append(defs, def)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Key < defs[j].Key })
	return defs
}
