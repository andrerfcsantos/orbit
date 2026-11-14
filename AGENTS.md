# orbit

orbit is a collection of personal tools and automations in one Go binary, `orb`.
Every tool is a cobra CLI command today; the code is layered so an API or a UI
could be added later without moving behavior.

Read [docs/architecture.md](docs/architecture.md) before changing how layers fit
together. README.md documents every command and config key users see.

## Commands

```bash
make check                          # what CI runs: gofmt + vet, go test, go test -race
make test                           # go test ./... (architecture_test.go included)
make fmt                            # gofmt -w
make run ARGS="git list ~/code"     # run the CLI from source
make build                          # bin/orb, version stamped in
make release-snapshot               # every release archive into dist/, publishing nothing
```

The git tests create real repositories in temp directories and take about a minute.

## Layout

| Path | What lives there |
| --- | --- |
| `internal/core/<tool>/` | Pure domain logic. No cobra, no terminal. |
| `internal/app/<tool>/` | The tool's single implementation: a plain Go service. No cobra, `net/http` or transport. |
| `internal/platform/` | Infrastructure: config, logging, running git. |
| `internal/cmd/<tool>/` | CLI commands, and building the service from configuration. |
| `internal/transport/tui/<tool>/` | The CLI's text rendering, Bubble Tea views and prompts. |

`architecture_test.go` enforces these import rules. Never weaken a rule to get a
build passing. If a rule is in your way, the code is in the wrong layer.

## Principles

### Configuration: flags, environment, files

Every setting can come from a flag, an `ORBIT_*` environment variable, or a config
file (local `./.orbit/config/config.yaml`, then the user's, then the system's).

- **Declare every setting as a `KeyDef`** in `internal/platform/config/catalog_<tool>.go`,
  and add it to `DefaultCatalog()`. Name keys `<tool>.<setting>` in kebab-case. The
  environment variable follows from the key: `git.release-tag.push` is
  `ORBIT_GIT_RELEASE_TAG_PUSH`.
- **Create flags with `manager.Register*Flag(cmd, Key)`**, from the same `KeyDef`, so
  the flag, its default and its `--help` text can't drift.
- **Read config only in `internal/cmd/<tool>`**, where the service is built. Pass
  plain values into `internal/app`. Don't call `os.Getenv` for orbit settings.
- Mark credentials `Secret: true`, with no flag. Never log or print config values,
  only key names.
- Add new keys to the tool's section in README.md.

### Canonical logs

orb writes one structured log entry per invocation, emitted by `cmd.Execute` exactly
once, even on errors and panics.

- **Add attributes to that entry; don't write new log lines.**
  `logging.AddFields(ctx, zap.Int("git.repositories", n))` works from any layer that
  has the `ctx`. Record them in the service in `internal/app`.
- **Namespace field names by tool**: `git.repositories`, `git.operation`.
- **Summarize what the work found** with `logging.Summarize(ctx, "...")`, in words
  that name only values safe to record.
- **Never log secrets, config values, or CLI argument values.**

## Gotchas

- **Every Go package needs a `// Package <name> ...` doc comment** (`// Command <name>`
  for a main package). The architecture test enforces it.
- **CLI exit codes:** `exit.Usage(err)` (2) for misuse, `exit.Fail(err)` (1) for a
  runtime failure, from `internal/cmd/internal/exit`.
- **The project is orbit, the binary is orb.** Config and log directories and the
  `ORBIT_` prefix use `bootstrap.AppName` (`orbit`); the root command uses
  `bootstrap.CommandName` (`orb`). User-facing text names commands as `orb ...`.
- **The version is stamped at link time.** `go run` reports `dev`; use `make build` to
  see a real version.

## Style

- Match the surrounding code. Comment only what is non-obvious, in a line or two. Put
  reasoning about the change in the commit or PR, not in the source.
- Test the service in `internal/app` directly, and each adapter for its own concerns.
- Commit and PR titles: `<tool>: <what a user can now do>`, e.g.
  `git: delete stale branches on every remote`.
- Update README.md (commands, the tool's section, config keys) and
  docs/architecture.md when a layering decision changes.

## Before you call a tool done

- [ ] Service in `internal/app/<tool>`, CLI under `orb <tool>`.
- [ ] Every setting is a catalog key (flag, `ORBIT_<TOOL>_*` variable, config file),
      documented in README.md.
- [ ] Canonical log fields added. Nothing secret logged.
- [ ] `make check` passes.
