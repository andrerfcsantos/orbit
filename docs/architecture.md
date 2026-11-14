# Architecture

orbit is a single Go binary, `orb`, holding a collection of personal tools. Today
every tool is a CLI command. The code is still layered as if other surfaces (an
HTTP API, a UI) could come later: a tool's behavior lives in one place, and the CLI
is a thin adapter over it, so adding a surface means adding an adapter rather than
moving code.

## Package map

```text
cmd/orb/                  entry point: ldflags build metadata, then hands off to internal/cmd
internal/
├── app/                  use cases, one package per tool, plus the per-command Runtime
│   ├── config/           inspecting and persisting configuration (CLI-only)
│   ├── git/              git in every repository beneath a directory, at once; release tags
│   └── version/          build metadata
├── bootstrap/            resolves configuration and builds the per-command runtime
├── cmd/                  the cobra command tree: root.go, plus one package per tool
│   ├── config/           `orb config`
│   ├── git/              `orb git`
│   ├── version/          `orb version`
│   └── internal/exit/    exit codes: 1 for a failure, 2 for a usage error
├── core/                 domain logic, framework-free
│   └── git/              semantic versions, tags, the next release, branch ages, git's output
├── platform/             infrastructure
│   ├── build/            the build metadata type
│   ├── config/           the viper manager, the key catalog, persistence
│   ├── git/              running git with nothing able to prompt; finding repositories on disk
│   └── logging/          zap, canonical entries and their mapped diagnostic context
└── transport/            delivery adapters
    └── tui/              the terminal: styles/ (lipgloss), git/ (Bubble Tea views, prompts, rendering)
```

## Dependency rules

Enforced by `architecture_test.go`, which reads the real import graph with
`go list`. They are tests rather than conventions because layering is easy to state
and easy to erode.

| Rule | Why |
| --- | --- |
| `internal/app` must not import cobra, `net/http`, the Charm stack (`charm.land/...`: lipgloss, Bubble Tea, Bubbles), or `internal/transport` | A use case that reaches for a delivery mechanism can only be driven from it. |
| `internal/core` must not import cobra, `net/http`, the Charm stack, `internal/cmd`, or `internal/transport` | Domain logic must be testable without a framework. |
| `internal/platform` must not import `internal/app`, `internal/cmd`, or `internal/transport` | Infrastructure is built upon, not the reverse. |
| `internal/transport` must not import `internal/cmd` | An adapter must not depend on the CLI's wiring. |
| Every package has a `// Package ...` comment | An undocumented layer invites the wrong code. |

## Adding a tool

1. **Use case.** Add `internal/app/<tool>/`, a plain Go service: no cobra, no
   terminal. Put domain logic in `internal/core/<tool>/` if it has any, and
   infrastructure (clients, stores, processes) in `internal/platform/<tool>/`.
2. **Config.** Declare every setting as a `KeyDef` in
   `internal/platform/config/catalog_<tool>.go`, with its flag name, description,
   default and owner, and add it to `DefaultCatalog()`. Name keys
   `<tool>.<setting>` in kebab-case: the environment variable follows from the key
   (`git.release-tag.push` is `ORBIT_GIT_RELEASE_TAG_PUSH`).
3. **CLI.** Add `internal/cmd/<tool>/` and register it in `internal/cmd/root.go`.
   Create flags with `manager.Register*Flag(cmd, Key)`, so the flag, its default and
   its `--help` text come from the `KeyDef` and can't drift. Read configuration only
   here, and pass plain values into the service. Render through
   `internal/transport/tui/<tool>`, with styles from `transport/tui/styles`.
4. **Logs.** Add fields to the invocation's canonical entry from the service (see
   [Logging](#logging)), not new log lines.
5. **Tests.** Test the service directly, and each adapter for its own concerns
   (argument parsing, output shape). Document the commands and keys in README.md.

## Configuration

Precedence, highest first:

1. Command flags
2. Environment variables: `ORBIT_*` (`ORBIT_LOG_LEVEL` sets `log.level`)
3. Local config: `./.orbit/config/config.yaml`
4. User config: the OS user config dir, and **the file `orb config <key> <value>` writes**
5. System config: the OS system config dir
6. Defaults declared on the keys

Directories come from [gappdirs](https://github.com/andrerfcsantos/gappdirs), named
after the project (`orbit`), not the binary. Files are merged in ascending
precedence, so a local file overrides the user's, which overrides the machine's,
while lower layers still supply values the higher ones omit. `orb config` prints
which files were loaded, so an unexpected value can be traced without knowing the
lookup order.

Only flags scoped to the *running* command are bound. Not for precedence: viper
consults a flag's `Changed` state, so an unset flag does not override a file or an
environment value. What scoping prevents is a command's configuration advertising
keys that belong to unrelated commands.

Flag conflicts (two keys in one scope claiming the same flag) are detected in
`BindFlags`, not at registration: commands register their keys inside their own
constructors, before `AddCommand` attaches them, so at registration time a
subcommand has no ancestors and a shared-scope conflict is invisible.

`manager.Explicit(key)` says whether a flag, a variable or a file set a key, rather
than its default. `orb git release-tag` uses it to ask, in a terminal, only what
nothing said.

Unknown keys are refused on write unless `--allow-unknown` is passed: a typo should
fail loudly, not persist a setting nothing reads. Durations need a unit everywhere
(`30s`, not `30`), since a bare number would be read as nanoseconds.

### Secrets

No key holds a secret yet, but the rules for one are in place, for the first tool
that needs a token:

- A `KeyDef` with `Secret: true` has no flag (a flag's value lands in shell history
  and the process list), can't be set with `orb config <key> <value>` for the same
  reason, and is printed by `orb config` as `••••••`. An unknown key that looks like
  a credential (`token`, `password`, `.key`, ...) is masked too.
- Canonical entries record config *keys*, never values, and the number of
  arguments, never the arguments. Errors are held to the same rule: a value that
  fails to parse is not quoted back.
- The user config file is written `0600`.
- `orb config` is CLI-only. Should orbit grow an API, `internal/app/config` and the
  config manager (and `app.Runtime`, which carries it) must stay out of its reach.

## Logging

One canonical entry per CLI invocation, rather than many scattered lines. Each
carries the same shape (what ran, its outcome, how long, the identifying context),
so entries can be filtered and aggregated.

`logging.Canonical` is a mapped diagnostic context: fields are keyed, so a later
layer can refine an earlier layer's value without the entry carrying a key twice.
It travels in the `context.Context`, so any layer can annotate it with
`logging.AddFields(ctx, ...)`, without a reference to the logger. Name fields by
tool (`git.repositories`); `event.kind`, `event.outcome`, `error` and `duration` are
set for you. `logging.Summarize(ctx, "...")` ends the entry's message with what the
work found, in words that name only values safe to record.

### Construction order

Configuration decides how to log, but loading configuration can fail, and that
failure must be logged. So:

1. `cmd.Execute` creates the canonical entry and defers its emit, before cobra
   parses anything. The entry starts against a minimal bootstrap logger (stderr).
2. `PersistentPreRunE` resolves configuration, builds the configured logger, and
   retargets the entry at it, keeping the fields already gathered.
3. An invocation cobra answers or rejects before `PersistentPreRunE` (`--help`, a
   mistyped flag or command) never reaches step 2, so `Execute` resolves
   configuration for it afterwards. If configuration can't be read, the entry goes
   to the defaults' log file.
4. The deferred emit fires exactly once on every path: success, failure, a
   flag-parse error, `--help`, and a panic inside a command.

The entry is emitted from `Execute`, not a cobra `PostRun` hook, because those are
skipped when a command returns an error: the invocation most worth logging.
`Execute` returns the exit code rather than calling `os.Exit`, so the log file is
flushed before the process ends.

### Destination

Entries go to a rotating file in the OS per-user log directory, keeping stdout clean
for command output. `--log-stdout` (`ORBIT_LOG_STDOUT=true`) switches to stdout.

## Build metadata

`cmd/orb/main.go` holds `version`, `commit` and `date` variables, set at link time
with `-ldflags -X`, by `scripts/build.sh` (which `make build` and `make install`
call) from `git describe`, and by GoReleaser from the release tag. An unstamped
build reports `dev`, `none` and `unknown`. They reach `orb version` through
`bootstrap.Inputs` and `app.Runtime`, as a `build.Info`: a fixed, three-field type,
so what it discloses can't be widened by accident.
