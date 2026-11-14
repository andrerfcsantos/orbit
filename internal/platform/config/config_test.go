package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// newTestManager builds a manager whose user config directory is a temp dir, so a
// test that persists a value cannot write to the developer's real config.
func newTestManager(t *testing.T, envPrefix string) *Manager {
	t.Helper()
	return New(
		"orbit-test",
		WithEnvPrefix(envPrefix),
		WithUserConfigDir(t.TempDir()),
		WithCatalog(testCatalog()),
	)
}

// A command with keys of its own, as a server would have: a string and an int,
// declared only for the tests so they exercise each type the catalog parses.
var (
	testHostKey = KeyDef{Key: "server.host", FlagName: "host", Description: "Host to listen on", Default: "127.0.0.1", Owner: "test"}
	testPortKey = KeyDef{Key: "server.port", FlagName: "port", Description: "Port to listen on", Default: 8888, Owner: "test"}
)

func testCatalog() Catalog {
	return NewCatalog(append(DefaultCatalog().Keys(), testHostKey, testPortKey)...)
}

// registerCommands builds a root/serve command pair with the keys each owns, the
// shape the real tree has.
func registerCommands(m *Manager) (root, serve *cobra.Command) {
	root = &cobra.Command{Use: "orbit"}
	m.RegisterPersistentStringFlag(root, LogLevelKey)

	serve = &cobra.Command{Use: "serve"}
	root.AddCommand(serve)
	m.RegisterIntFlag(serve, testPortKey)
	return root, serve
}

func TestDefaultsComeFromCatalog(t *testing.T) {
	m := newTestManager(t, "ORBIT_TEST_DEFAULTS")
	if err := m.Init(); err != nil {
		t.Fatalf("Init() = %v", err)
	}

	// The default must resolve even though no command registered the key: `orbit
	// config server.port` has to work outside `serve`.
	if got := m.GetInt(testPortKey.Key); got != testPortKey.Default {
		t.Errorf("%s = %d, want %v", testPortKey.Key, got, testPortKey.Default)
	}
	if got := m.GetString(KeyLogLevel); got != "info" {
		t.Errorf("%s = %q, want info", KeyLogLevel, got)
	}
}

func TestEnvOverridesDefault(t *testing.T) {
	prefix := "ORBIT_TEST_ENV"
	t.Setenv(prefix+"_LOG_LEVEL", "warn")

	m := newTestManager(t, prefix)
	if err := m.Init(); err != nil {
		t.Fatalf("Init() = %v", err)
	}

	if got := m.GetString(KeyLogLevel); got != "warn" {
		t.Errorf("%s = %q, want warn from the environment", KeyLogLevel, got)
	}
}

func TestFlagOverridesEnv(t *testing.T) {
	prefix := "ORBIT_TEST_FLAG"
	t.Setenv(prefix+"_LOG_LEVEL", "warn")

	m := newTestManager(t, prefix)
	root, _ := registerCommands(m)
	if err := m.Init(); err != nil {
		t.Fatalf("Init() = %v", err)
	}
	if err := root.PersistentFlags().Set("log-level", "error"); err != nil {
		t.Fatalf("set --log-level: %v", err)
	}
	if err := m.BindFlags(root); err != nil {
		t.Fatalf("BindFlags() = %v", err)
	}

	// The documented precedence is flags over environment; this is the assertion
	// that pins it.
	if got := m.GetString(KeyLogLevel); got != "error" {
		t.Errorf("%s = %q, want error from the flag", KeyLogLevel, got)
	}
}

func TestFileOverriddenByEnv(t *testing.T) {
	prefix := "ORBIT_TEST_FILE"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DefaultConfigFile), []byte("log:\n  level: debug\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv(prefix+"_LOG_LEVEL", "error")

	m := New("orbit-test", WithEnvPrefix(prefix), WithUserConfigDir(dir))
	if err := m.Init(); err != nil {
		t.Fatalf("Init() = %v", err)
	}

	if got := m.GetString(KeyLogLevel); got != "error" {
		t.Errorf("%s = %q, want error: the environment outranks the config file", KeyLogLevel, got)
	}
}

func TestFileOverridesDefault(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DefaultConfigFile), []byte("server:\n  port: 9191\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	m := New("orbit-test", WithEnvPrefix("ORBIT_TEST_FILEDEF"), WithUserConfigDir(dir))
	if err := m.Init(); err != nil {
		t.Fatalf("Init() = %v", err)
	}

	if got := m.GetInt(testPortKey.Key); got != 9191 {
		t.Errorf("%s = %d, want 9191 from the config file", testPortKey.Key, got)
	}
	if files := m.LoadedFiles(); len(files) != 1 {
		t.Errorf("LoadedFiles() = %v, want exactly the user config file", files)
	}
}

func TestBindFlagsIgnoresUnrelatedCommands(t *testing.T) {
	m := newTestManager(t, "ORBIT_TEST_SCOPE")
	root, serve := registerCommands(m)
	if err := m.Init(); err != nil {
		t.Fatalf("Init() = %v", err)
	}

	// A flag on a command that is not running must not be bound: unset, it holds
	// its zero value, which would silently override a configured value.
	if err := serve.Flags().Set("port", "1234"); err != nil {
		t.Fatalf("set --port: %v", err)
	}
	if err := m.BindFlags(root); err != nil {
		t.Fatalf("BindFlags(root) = %v", err)
	}

	if got := m.GetInt(testPortKey.Key); got != testPortKey.Default {
		t.Errorf("%s = %d, want the default: serve's flag is out of scope for root", testPortKey.Key, got)
	}
}

func TestBindFlagsIncludesInheritedFlags(t *testing.T) {
	m := newTestManager(t, "ORBIT_TEST_INHERIT")
	root, serve := registerCommands(m)
	if err := m.Init(); err != nil {
		t.Fatalf("Init() = %v", err)
	}

	if err := root.PersistentFlags().Set("log-level", "debug"); err != nil {
		t.Fatalf("set --log-level: %v", err)
	}
	if err := serve.Flags().Set("port", "1234"); err != nil {
		t.Fatalf("set --port: %v", err)
	}
	if err := m.BindFlags(serve); err != nil {
		t.Fatalf("BindFlags(serve) = %v", err)
	}

	if got := m.GetInt(testPortKey.Key); got != 1234 {
		t.Errorf("%s = %d, want 1234 from serve's own flag", testPortKey.Key, got)
	}
	// A persistent flag declared on the root applies to the running subcommand.
	if got := m.GetString(KeyLogLevel); got != "debug" {
		t.Errorf("%s = %q, want debug from the inherited persistent flag", KeyLogLevel, got)
	}
}

func TestSetPersistsAndReloads(t *testing.T) {
	dir := t.TempDir()
	m := New("orbit-test", WithEnvPrefix("ORBIT_TEST_PERSIST"), WithUserConfigDir(dir))
	if err := m.Init(); err != nil {
		t.Fatalf("Init() = %v", err)
	}
	if err := m.Set(KeyLogLevel, "debug", false); err != nil {
		t.Fatalf("Set() = %v", err)
	}

	// A fresh manager proves the value was written to the file, not merely held in
	// memory -- which is what "persist" has to mean for the next invocation.
	reloaded := New("orbit-test", WithEnvPrefix("ORBIT_TEST_PERSIST"), WithUserConfigDir(dir))
	if err := reloaded.Init(); err != nil {
		t.Fatalf("Init() on reload = %v", err)
	}
	if got := reloaded.GetString(KeyLogLevel); got != "debug" {
		t.Errorf("reloaded %s = %q, want debug", KeyLogLevel, got)
	}
}

func TestSetWritesRestrictedPermissions(t *testing.T) {
	dir := t.TempDir()
	m := New("orbit-test", WithEnvPrefix("ORBIT_TEST_PERMS"), WithUserConfigDir(dir))
	if err := m.Init(); err != nil {
		t.Fatalf("Init() = %v", err)
	}
	if err := m.Set(KeyLogLevel, "debug", false); err != nil {
		t.Fatalf("Set() = %v", err)
	}

	info, err := os.Stat(m.UserConfigFile())
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	// This file is the designated home for secrets, so it must not be readable by
	// other users on a shared machine.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config file mode = %#o, want 0600", perm)
	}
}

func TestSetDoesNotPersistEnvironmentValues(t *testing.T) {
	prefix := "ORBIT_TEST_NOLEAK"
	// A secret arriving via the environment must not be captured into the config
	// file as a side effect of setting an unrelated key -- that would turn a
	// process-scoped credential into one written to disk.
	t.Setenv(prefix+"_SERVER_HOST", "secret.internal")

	dir := t.TempDir()
	m := New("orbit-test", WithEnvPrefix(prefix), WithUserConfigDir(dir))
	if err := m.Init(); err != nil {
		t.Fatalf("Init() = %v", err)
	}
	if err := m.Set(KeyLogLevel, "debug", false); err != nil {
		t.Fatalf("Set() = %v", err)
	}

	contents, err := os.ReadFile(m.UserConfigFile())
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if got := string(contents); strings.Contains(got, "secret.internal") {
		t.Errorf("config file captured an environment value:\n%s", got)
	}
}

func TestSetParsesTypedValues(t *testing.T) {
	m := newTestManager(t, "ORBIT_TEST_TYPES")
	if err := m.Init(); err != nil {
		t.Fatalf("Init() = %v", err)
	}

	if err := m.Set(testPortKey.Key, "9000", false); err != nil {
		t.Fatalf("Set(port) = %v", err)
	}
	// Stored as an int, not "9000": a string would fail to unmarshal where a port
	// is expected.
	if got := m.GetInt(testPortKey.Key); got != 9000 {
		t.Errorf("%s = %d, want 9000", testPortKey.Key, got)
	}

	if err := m.Set(KeyLogStdout, "true", false); err != nil {
		t.Fatalf("Set(stdout) = %v", err)
	}
	if !m.GetBool(KeyLogStdout) {
		t.Errorf("%s = false, want true", KeyLogStdout)
	}

	if err := m.Set(testPortKey.Key, "not-a-number", false); err == nil {
		t.Error("Set(port, \"not-a-number\") = nil, want a parse error")
	}
}

func TestSetRejectsUnknownKey(t *testing.T) {
	m := newTestManager(t, "ORBIT_TEST_UNKNOWN")
	if err := m.Init(); err != nil {
		t.Fatalf("Init() = %v", err)
	}

	// A typo must fail rather than persist a setting nothing ever reads.
	err := m.Set("sever.port", "9000", false)
	if !errors.Is(err, ErrUnknownKey) {
		t.Errorf("Set(unknown) = %v, want ErrUnknownKey", err)
	}

	if err := m.Set("future.key", "value", true); err != nil {
		t.Errorf("Set(unknown, allowUnknown=true) = %v, want nil", err)
	}
}

func TestConfigFilePathsAreOrderedAndUnique(t *testing.T) {
	m := newTestManager(t, "ORBIT_TEST_PATHS")
	paths := m.ConfigFilePaths()

	if len(paths) == 0 {
		t.Fatal("ConfigFilePaths() is empty")
	}
	// Each gappdirs resolver appends lower-precedence fallbacks, so a naive
	// combination repeats paths and merges the same file twice.
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		if seen[path] {
			t.Errorf("ConfigFilePaths() repeats %s:\n%v", path, paths)
		}
		seen[path] = true
	}
	if !seen[m.UserConfigFile()] {
		t.Errorf("ConfigFilePaths() omits the user config file %s:\n%v", m.UserConfigFile(), paths)
	}
}

func TestEnvVarName(t *testing.T) {
	m := New("orbit", WithEnvPrefix("ORBIT"))
	for key, want := range map[string]string{
		"log.level":                "ORBIT_LOG_LEVEL",
		"server.port":              "ORBIT_SERVER_PORT",
		"download.concurrency.max": "ORBIT_DOWNLOAD_CONCURRENCY_MAX",
	} {
		if got := m.EnvVarName(key); got != want {
			t.Errorf("EnvVarName(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestKeysCarryNoValues(t *testing.T) {
	m := newTestManager(t, "ORBIT_TEST_KEYS")
	if err := m.Init(); err != nil {
		t.Fatalf("Init() = %v", err)
	}

	// Keys is the accessor safe to log or expose, so it must stay value-free even
	// as List grows.
	keys := m.Keys()
	if len(keys) == 0 {
		t.Fatal("Keys() is empty")
	}
	for i := 1; i < len(keys); i++ {
		if keys[i-1] > keys[i] {
			t.Errorf("Keys() is not sorted: %q before %q", keys[i-1], keys[i])
		}
	}
}

func TestRegisterKeysPanicsOnDuplicateKey(t *testing.T) {
	m := New("orbit")
	cmd := &cobra.Command{Use: "x"}
	m.RegisterKeys(cmd, KeyDef{Key: "a.b", FlagName: "one"})

	defer func() {
		if recover() == nil {
			t.Error("RegisterKeys did not panic on a duplicate key")
		}
	}()
	m.RegisterKeys(cmd, KeyDef{Key: "a.b", FlagName: "two"})
}

// One key can be every sibling's flag -- each job command's --json -- without
// being inherited by the siblings that have no use for it.
func TestAKeyCanBeRegisteredOnSeveralCommandsWithOneDefinition(t *testing.T) {
	m := newTestManager(t, "ORBIT_TEST_SHARED")
	def := KeyDef{Key: "jobs.json", FlagName: "json", Default: false}
	root := &cobra.Command{Use: "jobs"}
	show := &cobra.Command{Use: "show", Run: func(*cobra.Command, []string) {}}
	replay := &cobra.Command{Use: "replay", Run: func(*cobra.Command, []string) {}}
	root.AddCommand(show, replay)
	m.RegisterBoolFlag(root, def)
	m.RegisterBoolFlag(show, def)

	if err := show.ParseFlags([]string{"--json"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Init(); err != nil {
		t.Fatal(err)
	}
	if err := m.BindFlags(show); err != nil {
		t.Fatal(err)
	}
	if !m.GetBool("jobs.json") {
		t.Error("show's --json did not set jobs.json")
	}
	if replay.Flags().Lookup("json") != nil {
		t.Error("replay inherited --json")
	}

	defer func() {
		if recover() == nil {
			t.Error("RegisterKeys accepted a second, different definition of jobs.json")
		}
	}()
	m.RegisterBoolFlag(replay, KeyDef{Key: "jobs.json", FlagName: "json", Default: true})
}

func TestBindFlagsRejectsConflictingFlagInProductionWiringOrder(t *testing.T) {
	m := newTestManager(t, "ORBIT_TEST_CONFLICT")

	root := &cobra.Command{Use: "orbit"}
	m.RegisterPersistentStringFlag(root, KeyDef{
		Key: "log.level", FlagName: "log-level", Default: "info",
	})

	// Production wiring order: a command registers its keys inside its own
	// constructor, and only then is attached to the tree. Detection therefore cannot
	// live in RegisterKeys -- at that moment the subcommand has no parent, so a
	// shared-scope conflict is invisible. An earlier version checked there and missed
	// this entirely, silently binding one flag to two keys.
	sub := &cobra.Command{Use: "tool"}
	m.RegisterStringFlag(sub, KeyDef{Key: "tool.level", FlagName: "log-level", Default: "x"})
	root.AddCommand(sub)

	if err := m.Init(); err != nil {
		t.Fatalf("Init() = %v", err)
	}
	err := m.BindFlags(sub)
	if err == nil {
		t.Fatal("BindFlags() = nil, want an error for a flag bound to two keys in scope")
	}
	if !strings.Contains(err.Error(), "log-level") {
		t.Errorf("error does not name the conflicting flag: %v", err)
	}
}

func TestBindFlagsAllowsSameFlagNameOnUnrelatedCommands(t *testing.T) {
	m := newTestManager(t, "ORBIT_TEST_SIBLING")

	root := &cobra.Command{Use: "orbit"}
	serve := &cobra.Command{Use: "serve"}
	proxy := &cobra.Command{Use: "proxy"}
	root.AddCommand(serve, proxy)

	// Two sibling tools may each have a --port; only a shared scope is ambiguous.
	m.RegisterIntFlag(serve, KeyDef{Key: "server.port", FlagName: "port", Default: 8888})
	m.RegisterIntFlag(proxy, KeyDef{Key: "proxy.port", FlagName: "port", Default: 9000})

	if err := m.Init(); err != nil {
		t.Fatalf("Init() = %v", err)
	}
	if err := m.BindFlags(serve); err != nil {
		t.Errorf("BindFlags(serve) = %v, want nil: a sibling's --port is not a conflict", err)
	}
	if err := m.BindFlags(proxy); err != nil {
		t.Errorf("BindFlags(proxy) = %v, want nil", err)
	}
}

func TestBindFlagsAcceptsTheRealCommandTree(t *testing.T) {
	// A regression guard on the wiring itself: if two catalog keys were ever given
	// the same flag name in a shared scope, every invocation of that command would
	// fail. Checking the real tree here means that surfaces as a test failure rather
	// than at a user's first run.
	m := New("orbit-tree-test", WithEnvPrefix("ORBIT_TREE"), WithUserConfigDir(t.TempDir()))

	root := &cobra.Command{Use: "orbit"}
	m.RegisterPersistentStringFlag(root, LogLevelKey)
	m.RegisterPersistentStringFlag(root, LogFormatKey)
	m.RegisterPersistentBoolFlag(root, LogStdoutKey)

	serve := &cobra.Command{Use: "serve"}
	root.AddCommand(serve)
	m.RegisterStringFlag(serve, testHostKey)
	m.RegisterIntFlag(serve, testPortKey)

	if err := m.Init(); err != nil {
		t.Fatalf("Init() = %v", err)
	}
	for _, cmd := range []*cobra.Command{root, serve} {
		if err := m.BindFlags(cmd); err != nil {
			t.Errorf("BindFlags(%s) = %v, want nil", cmd.Name(), err)
		}
	}
}

// A duration is stored in its string form so viper round-trips it. Written as a
// time.Duration it would land in the YAML as a bare nanosecond count, which reads
// back as a number of nanoseconds only by accident of viper's coercion.
func TestSetRoundTripsADuration(t *testing.T) {
	durationKey := KeyDef{Key: "tool.timeout", Default: 60 * time.Second, Owner: "test"}
	manager := New("orbit-duration-test",
		WithUserConfigDir(t.TempDir()),
		WithCatalog(NewCatalog(durationKey)),
	)
	if err := manager.Init(); err != nil {
		t.Fatalf("Init() = %v", err)
	}

	if got, err := manager.Duration("tool.timeout"); err != nil || got != 60*time.Second {
		t.Errorf("default = %s (err %v), want 60s", got, err)
	}
	if err := manager.Set("tool.timeout", "90s", false); err != nil {
		t.Fatalf("Set() = %v", err)
	}
	if got, err := manager.Duration("tool.timeout"); err != nil || got != 90*time.Second {
		t.Errorf("after Set, duration = %s (err %v), want 90s", got, err)
	}

	// Re-read from the file, which is what the next invocation does.
	reloaded := New("orbit-duration-test",
		WithUserConfigDir(filepath.Dir(manager.UserConfigFile())),
		WithCatalog(NewCatalog(durationKey)),
	)
	if err := reloaded.Init(); err != nil {
		t.Fatalf("reloaded Init() = %v", err)
	}
	if got, err := reloaded.Duration("tool.timeout"); err != nil || got != 90*time.Second {
		t.Errorf("reloaded duration = %s (err %v), want 90s", got, err)
	}
}

func TestSetRejectsAMalformedDuration(t *testing.T) {
	manager := New("orbit-duration-test",
		WithUserConfigDir(t.TempDir()),
		WithCatalog(NewCatalog(KeyDef{Key: "tool.timeout", Default: time.Second, Owner: "test"})),
	)
	if err := manager.Init(); err != nil {
		t.Fatalf("Init() = %v", err)
	}

	err := manager.Set("tool.timeout", "soon", false)
	if err == nil {
		t.Fatal("Set accepted a malformed duration")
	}
	// The rejected value must not appear: this text reaches stderr and the log
	// entry, and a mistyped secret is exactly what would be echoed.
	if strings.Contains(err.Error(), "soon") {
		t.Errorf("error %q quotes the rejected value", err)
	}
}

// A credential on a command line lands in shell history and the process list.
func TestCredentialKeysHaveNoFlag(t *testing.T) {
	catalog := DefaultCatalog()
	for _, def := range catalog.Keys() {
		if def.Secret && def.FlagName != "" {
			t.Errorf("%s is secret but exposes --%s", def.Key, def.FlagName)
		}
	}
}

// viper reads a bare number as a nanosecond count, so ORBIT_JOBS_PAYLOAD_SAVER_TIMEOUT=300
// resolves to 300ns: positive, so it survives the "must be positive" check the reader
// applies, and then times out every request. The setting reads as configured and does
// the opposite. `orb config set` has always demanded the unit; the
// environment and config files are where these values actually come from.
func TestDurationRejectsAValueWithNoUnit(t *testing.T) {
	durationKey := KeyDef{Key: "tool.timeout", Default: 60 * time.Second, Owner: "test"}

	for name, raw := range map[string]string{
		"integer":    "300",
		"float":      "1.5",
		"scientific": "1e3",
		"nonsense":   "abc",
		"unit typo":  "5 m",
	} {
		t.Setenv("ORBITDUR_TOOL_TIMEOUT", raw)
		manager := New("orbit-duration-test",
			WithEnvPrefix("ORBITDUR"),
			WithUserConfigDir(t.TempDir()),
			WithCatalog(NewCatalog(durationKey)),
		)
		if err := manager.Init(); err != nil {
			t.Fatalf("%s: Init() = %v", name, err)
		}

		got, err := manager.Duration("tool.timeout")
		if err == nil {
			t.Errorf("%s: %q was accepted, as %s", name, raw, got)
			continue
		}
		if strings.Contains(err.Error(), raw) {
			t.Errorf("%s: the rejection echoed the value back: %v", name, err)
		}
		if !strings.Contains(err.Error(), "tool.timeout") {
			t.Errorf("%s: rejection should name the key, got: %v", name, err)
		}
	}
}

// A config file reaches viper as a typed YAML scalar rather than a string, so it
// needs the same guard by a different route.
func TestDurationRejectsAUnitlessValueFromAFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DefaultConfigFile), []byte("tool:\n  timeout: 300\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	manager := New("orbit-duration-test",
		WithUserConfigDir(dir),
		WithCatalog(NewCatalog(KeyDef{Key: "tool.timeout", Default: 60 * time.Second, Owner: "test"})),
	)
	if err := manager.Init(); err != nil {
		t.Fatalf("Init() = %v", err)
	}

	if got, err := manager.Duration("tool.timeout"); err == nil {
		t.Errorf("a bare 300 in YAML was accepted, as %s", got)
	}
}

// The unit is the only thing being demanded: everything time.ParseDuration takes
// still resolves, including the bare 0 that documented settings use to opt out.
func TestDurationAcceptsUnitBearingValues(t *testing.T) {
	for raw, want := range map[string]time.Duration{
		"5m":    5 * time.Minute,
		"1h30m": 90 * time.Minute,
		"250ms": 250 * time.Millisecond,
		"0":     0,
		"0s":    0,
		"-1m":   -time.Minute,
		"1.5h":  90 * time.Minute,
	} {
		t.Setenv("ORBITDUR_TOOL_TIMEOUT", raw)
		manager := New("orbit-duration-test",
			WithEnvPrefix("ORBITDUR"),
			WithUserConfigDir(t.TempDir()),
			WithCatalog(NewCatalog(KeyDef{Key: "tool.timeout", Default: time.Second, Owner: "test"})),
		)
		if err := manager.Init(); err != nil {
			t.Fatalf("%s: Init() = %v", raw, err)
		}

		got, err := manager.Duration("tool.timeout")
		if err != nil {
			t.Errorf("%q was rejected: %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("%q = %s, want %s", raw, got, want)
		}
	}
}

// Zero is the documented way to turn off a floor or a bound, and time.ParseDuration
// takes it bare. It worked as "0" from the environment and as a quoted string, but
// a YAML 0 reached the numeric branch and was refused -- so the spelling a config
// file naturally uses was the one that failed.
func TestDurationAcceptsANumericZeroFromAFile(t *testing.T) {
	for name, body := range map[string]string{
		"integer": "tool:\n  timeout: 0\n",
		"quoted":  "tool:\n  timeout: \"0\"\n",
		"float":   "tool:\n  timeout: 0.0\n",
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, DefaultConfigFile), []byte(body), 0o600); err != nil {
			t.Fatalf("%s: write config: %v", name, err)
		}
		manager := New("orbit-duration-test",
			WithUserConfigDir(dir),
			WithCatalog(NewCatalog(KeyDef{Key: "tool.timeout", Default: 60 * time.Second, Owner: "test"})),
		)
		if err := manager.Init(); err != nil {
			t.Fatalf("%s: Init() = %v", name, err)
		}

		got, err := manager.Duration("tool.timeout")
		if err != nil {
			t.Errorf("%s: a zero was refused: %v", name, err)
			continue
		}
		if got != 0 {
			t.Errorf("%s: got %s, want 0", name, got)
		}
	}
}

// Only zero. A nonzero bare number is still the nanosecond trap this rejects.
func TestDurationStillRejectsANonZeroNumberFromAFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DefaultConfigFile), []byte("tool:\n  timeout: 300\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	manager := New("orbit-duration-test",
		WithUserConfigDir(dir),
		WithCatalog(NewCatalog(KeyDef{Key: "tool.timeout", Default: 60 * time.Second, Owner: "test"})),
	)
	if err := manager.Init(); err != nil {
		t.Fatalf("Init() = %v", err)
	}
	if got, err := manager.Duration("tool.timeout"); err == nil {
		t.Errorf("a bare 300 was accepted, as %s", got)
	}
}
