package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	appdirs "github.com/andrerfcsantos/gappdirs"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// DefaultConfigFile is the file name looked for in each config directory.
const DefaultConfigFile = "config.yaml"

// envKeyReplacer maps a dotted config key onto an environment variable name:
// "log.level" becomes LOG_LEVEL, which with the prefix is ORBIT_LOG_LEVEL.
var envKeyReplacer = strings.NewReplacer(".", "_", "-", "_")

// ErrUnknownKey is returned when a write targets a key absent from the catalog.
var ErrUnknownKey = errors.New("unknown config key")

// Manager resolves and persists configuration.
//
// Precedence, highest first:
//
//  1. command flags       (bound via BindFlags)
//  2. environment         (ORBIT_*)
//  3. local config file   (./.orbit/config.yaml)
//  4. user config file    (OS user config dir; the target of Set)
//  5. system config file  (OS system config dir)
//  6. defaults declared on registered KeyDefs
//
// Layers 3-5 are merged in ascending order of precedence so a developer's
// repo-local file overrides their user file, which overrides whatever the
// machine provides -- while the machine still supplies values neither file
// mentions.
type Manager struct {
	appName       string
	configFile    string
	envPrefix     string
	userConfigDir string
	catalog       Catalog

	// keyDefs holds every registered key; commandDefs records which command
	// registered it, so BindFlags can bind only the keys in scope for the command
	// actually running (see scopedKeyDefs).
	keyDefs     map[string]KeyDef
	commandDefs map[*cobra.Command][]KeyDef

	// boundFlags records the flag bound to each key, so Explicit can tell a flag
	// the user passed from one left at its default.
	boundFlags map[string]*pflag.Flag

	// loadedFiles records the config files actually merged, in the order they were
	// merged. `orb config` reports these so "why is this value set" is
	// answerable without guessing at the lookup path.
	loadedFiles []string

	v *viper.Viper
}

// Option customizes a Manager.
type Option func(*options)

type options struct {
	configFile    string
	envPrefix     string
	userConfigDir string
	catalog       Catalog
}

// WithConfigFile overrides the config file name looked for in each directory.
func WithConfigFile(name string) Option {
	return func(o *options) { o.configFile = name }
}

// WithEnvPrefix overrides the environment variable prefix, e.g. "ORBIT".
func WithEnvPrefix(prefix string) Option {
	return func(o *options) { o.envPrefix = prefix }
}

// WithUserConfigDir overrides the user config directory. Tests use it to keep
// writes inside a temp dir; without it, a test that persisted a value would
// write to the developer's real config.
func WithUserConfigDir(dir string) Option {
	return func(o *options) { o.userConfigDir = dir }
}

// WithCatalog overrides the set of known keys.
func WithCatalog(c Catalog) Option {
	return func(o *options) { o.catalog = c }
}

// New builds a Manager for an application name. Init must be called before
// values are read.
func New(appName string, opts ...Option) *Manager {
	o := options{
		configFile: DefaultConfigFile,
		envPrefix:  strings.ToUpper(envKeyReplacer.Replace(appName)),
		catalog:    DefaultCatalog(),
	}
	for _, opt := range opts {
		opt(&o)
	}
	if o.configFile == "" {
		o.configFile = DefaultConfigFile
	}

	return &Manager{
		appName:       appName,
		configFile:    o.configFile,
		envPrefix:     o.envPrefix,
		userConfigDir: o.userConfigDir,
		catalog:       o.catalog,
		keyDefs:       make(map[string]KeyDef),
		commandDefs:   make(map[*cobra.Command][]KeyDef),
		boundFlags:    make(map[string]*pflag.Flag),
		v:             viper.New(),
	}
}

// Init builds the resolved configuration: defaults, then environment, then the
// config files in precedence order. Flags are applied separately by BindFlags,
// which must run after the command line has been parsed.
func (m *Manager) Init() error {
	m.v = viper.New()
	m.loadedFiles = nil

	for _, def := range m.keyDefs {
		if def.Default != nil {
			m.v.SetDefault(def.Key, def.Default)
		}
	}
	// Catalog defaults are applied too, so a key that exists but was not
	// registered by the running command still resolves to its default rather than
	// to nil. Without this, `orb config git.timeout` would report nothing when
	// run outside `orb git`.
	for _, def := range m.catalog.Keys() {
		if def.Default != nil {
			m.v.SetDefault(def.Key, def.Default)
		}
	}

	m.v.SetEnvPrefix(m.envPrefix)
	m.v.SetEnvKeyReplacer(envKeyReplacer)
	m.v.AutomaticEnv()

	for _, path := range m.ConfigFilePaths() {
		if _, err := os.Stat(path); err != nil {
			// A missing file is the normal case: most installs have no system or
			// local config. Anything else (a permissions problem, say) is worth
			// surfacing, because silently ignoring it would present a
			// half-configured process as fully configured.
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("stat config file %s: %w", path, err)
		}
		m.v.SetConfigFile(path)
		if err := m.v.MergeInConfig(); err != nil {
			return fmt.Errorf("read config file %s: %w", path, err)
		}
		m.loadedFiles = append(m.loadedFiles, path)
	}

	return nil
}

// ConfigFilePaths returns every path consulted for configuration, in ascending
// order of precedence: system, then user, then the local (per-checkout) file.
//
// Exactly one path per scope is taken. Each gappdirs resolver already appends the
// lower-precedence scopes as fallbacks, so combining all three resolvers wholesale
// would list the same file several times and merge it repeatedly.
func (m *Manager) ConfigFilePaths() []string {
	var paths []string

	if dirs := appdirs.NewSystemResolver(m.appName).ConfigDirs(); len(dirs) > 0 {
		paths = append(paths, filepath.Join(dirs[0], m.configFile))
	}

	paths = append(paths, m.UserConfigFile())

	// The local layer gives a per-checkout ./.orbit/config/config.yaml, so settings
	// can travel with a repository. It is skipped when the working directory cannot
	// be determined -- the other layers remain usable.
	if cwd, err := os.Getwd(); err == nil {
		local := appdirs.NewLocalResolver(m.appName, appdirs.WithLocalDir(cwd))
		if dirs := local.ConfigDirs(); len(dirs) > 0 {
			paths = append(paths, filepath.Join(dirs[0], m.configFile))
		}
	}

	return dedupePaths(paths)
}

// dedupePaths drops repeated paths, keeping the last occurrence so a path that is
// both e.g. the user and the system location is merged at its highest precedence.
func dedupePaths(paths []string) []string {
	lastIndex := make(map[string]int, len(paths))
	for i, path := range paths {
		lastIndex[path] = i
	}
	out := make([]string, 0, len(paths))
	for i, path := range paths {
		if lastIndex[path] == i {
			out = append(out, path)
		}
	}
	return out
}

// LoadedFiles returns the config files that were merged, lowest precedence first.
func (m *Manager) LoadedFiles() []string {
	return append([]string(nil), m.loadedFiles...)
}

// UserConfigFile returns the path Set writes to.
func (m *Manager) UserConfigFile() string {
	return filepath.Join(m.resolveUserConfigDir(), m.configFile)
}

// RegisterKeys records key definitions against the command that owns them.
//
// One definition may be registered on several commands, so siblings share a key
// without a persistent flag pushing it onto siblings that do not read it. Two
// different definitions of one key are refused.
//
// It panics rather than returning an error because every call site is static
// wiring executed during startup: a duplicate key or a flag bound to two keys is
// a programming mistake that cannot be recovered from at runtime, and failing at
// the first invocation is better than resolving configuration unpredictably.
func (m *Manager) RegisterKeys(cmd *cobra.Command, defs ...KeyDef) {
	if cmd == nil {
		panic("config.RegisterKeys: nil command")
	}

	for _, def := range defs {
		if strings.TrimSpace(def.Key) == "" {
			panic("config.RegisterKeys: empty config key")
		}
		if existing, exists := m.keyDefs[def.Key]; exists && existing != def {
			panic(fmt.Sprintf("config.RegisterKeys: duplicate config key %q", def.Key))
		}
		m.keyDefs[def.Key] = def
		m.commandDefs[cmd] = append(m.commandDefs[cmd], def)
	}
}

// BindFlags binds the flags of cmd (and its ancestors) to their config keys, so
// an explicitly-set flag takes precedence over everything else.
//
// Only keys in the running command's scope are bound. Viper consults a flag's
// Changed state, so an unset flag does not override a file or environment value --
// the scoping is not what makes precedence correct. What it prevents is a command's
// resolved configuration advertising keys belonging to unrelated commands: binding
// every registered key would add them all to AllKeys(), so `orb config` would
// list settings that have no bearing on the command being run.
func (m *Manager) BindFlags(cmd *cobra.Command) error {
	if cmd == nil {
		return errors.New("config.BindFlags: nil command")
	}

	// Conflicts are detected here rather than in RegisterKeys because this is the
	// first point at which the command tree is complete. Commands register their keys
	// inside their own constructors, before AddCommand attaches them to a parent, so
	// at registration time a subcommand has no ancestors and a shared-scope conflict
	// is invisible.
	boundBy := make(map[string]string, len(m.keyDefs))

	for _, def := range m.scopedKeyDefs(cmd) {
		if def.FlagName == "" {
			continue
		}
		flag := lookupFlag(cmd, def.FlagName)
		if flag == nil {
			continue
		}
		if existing, taken := boundBy[def.FlagName]; taken && existing != def.Key {
			// Two keys in scope for one command claim the same flag, so the flag's value
			// would land in both. Which of them a reader gets is an accident of
			// registration order, so this is a wiring bug to fix rather than a state to
			// resolve.
			return fmt.Errorf("flag --%s is bound to both %q and %q for %s",
				def.FlagName, existing, def.Key, cmd.CommandPath())
		}
		boundBy[def.FlagName] = def.Key

		if err := m.v.BindPFlag(def.Key, flag); err != nil {
			return fmt.Errorf("bind --%s to %s: %w", def.FlagName, def.Key, err)
		}
		m.boundFlags[def.Key] = flag
	}
	return nil
}

// Get returns a value and whether it is set at all.
func (m *Manager) Get(key string) (any, bool) {
	if !m.v.IsSet(key) {
		return nil, false
	}
	return m.v.Get(key), true
}

// Explicit reports whether something other than a default supplies key: a flag
// that was passed, its environment variable, or a config file.
//
// It exists for settings that override another source only when someone asked
// them to. A value read through Get cannot say that -- an unset flag reads as its
// default, indistinguishable from the same value typed out -- so applying every
// key on read would have the defaults silently replace whatever the other source
// said.
func (m *Manager) Explicit(key string) bool {
	if flag, bound := m.boundFlags[key]; bound && flag.Changed {
		return true
	}
	if _, set := os.LookupEnv(m.EnvVarName(key)); set {
		return true
	}
	return m.v.InConfig(key)
}

// UnmarshalKey decodes a structured value, such as a list, into target.
func (m *Manager) UnmarshalKey(key string, target any) error {
	return m.v.UnmarshalKey(key, target)
}

// GetString returns a string value, or "" when unset.
func (m *Manager) GetString(key string) string { return m.v.GetString(key) }

// GetInt returns an int value, or 0 when unset.
func (m *Manager) GetInt(key string) int { return m.v.GetInt(key) }

// GetBool returns a bool value, or false when unset.
func (m *Manager) GetBool(key string) bool { return m.v.GetBool(key) }

// Duration returns a duration value, or 0 when unset.
//
// A bare number is rejected rather than read as a nanosecond count, which is what
// viper's own coercion does: ORBIT_JOBS_PAYLOAD_SAVER_TIMEOUT=300 means five minutes to
// everyone who writes it, and 300ns is positive, so it passes a "must be positive"
// check and then times out every request. `orb config set` has always required
// the unit -- this applies the same rule to the environment and config files, which
// is where the value usually comes from.
func (m *Manager) Duration(key string) (time.Duration, error) {
	switch raw := m.v.Get(key).(type) {
	case nil:
		return 0, nil
	case time.Duration:
		return raw, nil
	case string:
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return 0, durationError(key)
		}
		return parsed, nil
	default:
		// A YAML scalar: viper hands back int or float64 here. Zero is the one number
		// that needs no unit -- it is the documented way to turn a floor or a bound
		// off, it is what time.ParseDuration itself accepts bare, and it already
		// worked from the environment and as a quoted string. Rejecting it only in
		// YAML made the spelling the file uses the one that does not work.
		if numericZero(raw) {
			return 0, nil
		}
		return 0, durationError(key)
	}
}

// numericZero reports whether a YAML scalar is zero, whichever numeric type the
// decoder chose for it.
func numericZero(raw any) bool {
	switch value := raw.(type) {
	case int:
		return value == 0
	case int32:
		return value == 0
	case int64:
		return value == 0
	case uint:
		return value == 0
	case uint64:
		return value == 0
	case float32:
		return value == 0
	case float64:
		return value == 0
	}
	return false
}

// durationError names the key and the expected form but never the value, for the
// reason parseValue gives.
func durationError(key string) error {
	return fmt.Errorf("config %s expects a duration with a unit, such as 30s, 5m or 1h", key)
}

// List returns every resolved key-value pair, sorted by key.
//
// Values may include secrets supplied through the environment or a config file,
// which is why this is reachable only from the CLI. See docs/architecture.md.
func (m *Manager) List() []Entry {
	keys := m.v.AllKeys()
	entries := make([]Entry, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, Entry{Key: key, Value: m.v.Get(key)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	return entries
}

// Keys returns the names of every resolved key, sorted. Unlike List it carries no
// values, so it is safe to log or expose: it describes the shape of the
// configuration without disclosing any of it.
func (m *Manager) Keys() []string {
	keys := m.v.AllKeys()
	sort.Strings(keys)
	return keys
}

// FindKeyDef returns the definition of a key, whether registered by a command or
// merely known to the catalog.
func (m *Manager) FindKeyDef(key string) (KeyDef, bool) {
	if def, ok := m.keyDefs[key]; ok {
		return def, true
	}
	return m.catalog.Lookup(key)
}

// CatalogEntries returns every known key definition.
func (m *Manager) CatalogEntries() []KeyDef { return m.catalog.Keys() }

// EnvVarName returns the environment variable that sets a key, for display.
func (m *Manager) EnvVarName(key string) string {
	name := strings.ToUpper(envKeyReplacer.Replace(key))
	prefix := strings.ToUpper(strings.TrimSpace(m.envPrefix))
	if prefix == "" {
		return name
	}
	return prefix + "_" + name
}

// Set parses value according to the key's declared type and persists it to the
// user config file, creating the file and its directory when absent.
//
// Unknown keys are refused unless allowUnknown is set: a mistyped key would
// otherwise be written happily and silently never read. allowUnknown exists
// because a key can be legitimately ahead of the catalog (a config file shared
// with a newer build), but it has to be asked for.
func (m *Manager) Set(key, value string, allowUnknown bool) error {
	parsed, err := m.parseValue(key, value, allowUnknown)
	if err != nil {
		return err
	}

	dir := m.resolveUserConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config directory %s: %w", dir, err)
	}
	path := filepath.Join(dir, m.configFile)

	// Write through a separate viper holding only the user file's own contents.
	// m.v carries the merged result of every layer plus defaults, so writing it
	// out would bake this machine's environment variables and the system file's
	// values into the user's file as if they had been set there by hand.
	out := viper.New()
	out.SetConfigFile(path)
	if _, statErr := os.Stat(path); statErr == nil {
		if err := out.ReadInConfig(); err != nil {
			return fmt.Errorf("read existing config %s: %w", path, err)
		}
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("stat config file %s: %w", path, statErr)
	}
	out.Set(key, parsed)
	if err := out.WriteConfigAs(path); err != nil {
		return fmt.Errorf("write config to %s: %w", path, err)
	}
	// Restrict the file: it is the designated home for tokens and similar
	// secrets, and viper creates it world-readable by default.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("restrict permissions on %s: %w", path, err)
	}

	// Reflect the write in the live instance so a command that sets and then reads a
	// value within one invocation sees it.
	//
	// Note this lands in viper's override layer, which outranks even a bound flag for
	// the rest of this process. That is acceptable because `orb config <k> <v>` is
	// terminal -- it writes, reports, and exits -- but a future caller that set a value
	// and then kept running would not see its own --flag honoured afterwards.
	m.v.Set(key, parsed)
	return nil
}

// parseValue converts a command-line string to the type the key's default
// declares, so `orb config git.concurrency 8` stores the number 8 rather
// than the string "8" (which would then fail to unmarshal as an int).
func (m *Manager) parseValue(key, value string, allowUnknown bool) (any, error) {
	def, known := m.FindKeyDef(key)
	if !known {
		if !allowUnknown {
			return nil, fmt.Errorf("%w: %s (pass --allow-unknown to set it anyway)", ErrUnknownKey, key)
		}
		// An unknown key has no declared type, so the value is stored verbatim.
		return value, nil
	}

	// The rejection messages below deliberately do NOT wrap the strconv error.
	// strconv quotes the input it could not parse, so wrapping it would put the
	// value in the error string -- and that string is printed to stderr and recorded
	// on the canonical log entry. Since the value being set is exactly where a
	// secret appears (`orb config <key> <token>`), a mistyped or wrong-typed
	// secret would be logged verbatim. The key and the expected type are what the
	// user needs to fix the command; the value is something they already have.
	switch def.Default.(type) {
	case int:
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return nil, fmt.Errorf("config %s expects an integer", key)
		}
		return parsed, nil
	case bool:
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("config %s expects a boolean (true or false)", key)
		}
		return parsed, nil
	case float64:
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, fmt.Errorf("config %s expects a number", key)
		}
		return parsed, nil
	case time.Duration:
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return nil, fmt.Errorf("config %s expects a duration such as 30s, 5m or 1h", key)
		}
		// Stored as the string form: viper round-trips that through Duration, while
		// a time.Duration would be written to YAML as a bare nanosecond count.
		return parsed.String(), nil
	default:
		return value, nil
	}
}

func (m *Manager) resolveUserConfigDir() string {
	if m.userConfigDir != "" {
		return m.userConfigDir
	}
	return appdirs.NewUserResolver(m.appName).ConfigDir()
}

// scopedKeyDefs returns the keys registered on cmd and on each of its ancestors,
// ordered root-first so a child command's registration wins on conflict.
func (m *Manager) scopedKeyDefs(cmd *cobra.Command) []KeyDef {
	var chain []*cobra.Command
	for cur := cmd; cur != nil; cur = cur.Parent() {
		chain = append(chain, cur)
	}

	var defs []KeyDef
	for i := len(chain) - 1; i >= 0; i-- {
		defs = append(defs, m.commandDefs[chain[i]]...)
	}
	return defs
}

// lookupFlag finds a flag on the command, among its inherited persistent flags,
// or among its own persistent flags.
func lookupFlag(cmd *cobra.Command, name string) *pflag.Flag {
	if f := cmd.Flags().Lookup(name); f != nil {
		return f
	}
	if f := cmd.InheritedFlags().Lookup(name); f != nil {
		return f
	}
	return cmd.PersistentFlags().Lookup(name)
}
