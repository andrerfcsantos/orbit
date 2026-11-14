# orbit

Personal tools and automations, in one Go binary: `orb`.

orbit starts with git tooling: running git in every repository beneath a
directory at once, and tagging releases. Every setting can come from a flag, an
environment variable or a config file, and `orb config` shows and sets them.

## Install

Download an archive for your system from the
[releases](https://github.com/andrerfcsantos/orbit/releases), for `darwin`,
`linux` and `windows` on `x86_64` and `arm64`:

```bash
gh release download --repo andrerfcsantos/orbit --pattern 'orbit_*_linux_x86_64.tar.gz'   # latest
tar -xzf orbit_*.tar.gz orb && ./orb version
```

Or build it from source, with the version stamped in:

```bash
make install        # into $GOBIN, or $(go env GOPATH)/bin
```

`go install github.com/andrerfcsantos/orbit/cmd/orb@latest` works too, but reports
its version as `dev`, since nothing stamps it.

## Commands

```bash
orb version                         # the version, commit and build date of this binary
orb config                          # every setting in effect, and the files it came from
orb config git.concurrency          # one setting, and every way to set it
orb config git.concurrency 8        # save a setting for later runs
orb git list ~/code                 # each repository's branch, changes, upstream and stashes
orb git update ~/code               # fetch everything, fast-forward the branch checked out
orb git branch delete ~/code -n     # list the branches older than 6 months
orb git gc ~/code                   # collect garbage, and say how much it freed
orb git clean ~/code/thing -n       # what git clean -ffxd would remove
orb git release-tag                 # tag and push the next version of this repository
```

Every command takes `--help`. `--log-level`, `--log-format` and `--log-stdout` work
on every command (see [Logging](#logging)).

## Configuration

Every setting can come from a flag, an environment variable, or a config file.
Precedence, highest first:

1. Command flags: `--concurrency 8`
2. Environment variables: `ORBIT_GIT_CONCURRENCY=8`. A key's variable is `ORBIT_`
   and the key in capitals, with `.` and `-` as `_`.
3. Local config: `./.orbit/config/config.yaml`, in the directory you run `orb` from
4. User config: `orb config <key> <value>` writes here
   - Linux: `~/.config/orbit/config.yaml` (`$XDG_CONFIG_HOME/orbit/`)
   - macOS: `~/Library/Application Support/orbit/config/config.yaml`
   - Windows: `%APPDATA%\orbit\Config\config.yaml`
5. System config: `/etc/xdg/orbit/`, `/Library/Application Support/orbit/config/`
   or `%ProgramData%\orbit\Config\`
6. The key's default

A config file holds the keys as YAML, nested at each dot:

```yaml
log:
  level: debug
git:
  concurrency: 8
  release-tag:
    push: false
```

`orb config` lists every key with its value and the files that were loaded, so an
unexpected value can be traced. `orb config <key>` describes a key: its flag, its
environment variable, its default. `orb config <key> <value>` checks the value
against the key's type and refuses a key orbit doesn't know (`--allow-unknown` to
set it anyway). The user file is written `0600`. A key marked secret is printed as
`••••••`, and can't be set from the command line, where it would land in your shell
history: export its variable or edit the file instead.

| Key | Flag | Default | Purpose |
| --- | --- | --- | --- |
| `log.level` | `--log-level` | `info` | Minimum level emitted: `debug`, `info`, `warn`, `error` |
| `log.format` | `--log-format` | `json` | `json` or `console` |
| `log.stdout` | `--log-stdout` | `false` | Log to stdout instead of the log file |
| `git.*` | | | See [`git`](#git--git-in-every-repository-beneath-a-directory-at-once) |

## Tools

### `git` — git in every repository beneath a directory, at once

Runs an operation in every git repository beneath the paths you give, all at once.
A path can be one repository, which works like running git there, or a directory
holding repositories at any depth: each repository is found, and nothing beneath
it is searched. A directory inside a repository that holds no repository of its
own names that repository, as it would for git. With no path, it's `.`. Hidden
directories and `node_modules` aren't searched, nor `~/Library` on macOS.

```bash
orb git list ~/code                         # each repository's branch, changes, upstream and stashes
orb git update ~/code                       # fetch everything, fast-forward the branch checked out
orb git update ~/code --default             # switch each to its default branch, then pull it
orb git branch delete ~/code --dry-run      # list the branches older than 6 months
orb git branch delete ~/code --older-than 1y --remote
orb git gc ~/code
orb git clean ~/code/thing --dry-run        # what git clean -ffxd would remove
orb git release-tag                         # asks what to release, minor first, then tags and pushes
orb git release-tag --bump patch --yes
orb git release-tag --pre beta --dry-run    # v1.5.0-beta.1, then beta.2, ...
```

Each repository is worked on in a goroutine of its own, as many at once as suits
the command: 32 for `list`, 16 for `update` and `branch delete`, half the cores for
`gc` and `clean`, since git gc already repacks on every core. `-j`/`--concurrency`
sets it. Worktrees of one repository take turns, since they share its stash and
refs. In a terminal, a live view shows the repositories being worked on, what
each is doing, and the batch's progress, and prints a line for each as it finishes;
piped, there is a line per repository, and `--json` prints every result in one
document. Then the repositories that need your attention are listed again: failures,
skips, and changes left in a stash. Ctrl-C stops the git that is running and starts
nothing more. The exit code is 1 when any repository failed or was stopped.

Nothing orb runs can prompt: a remote that needs a password or a passphrase fails
and says why, rather than hang the batch. Each repository's work is bounded by
`--timeout`, 10 minutes by default. git always works in the repository found:
`GIT_DIR` and the other variables that point git elsewhere are not passed on, and
a `.git` git can't read fails rather than lead it to a repository above.

**`update`** fetches every remote (`git fetch --all --prune`), and fast-forwards
the branch checked out to its upstream. Your changes, untracked files included, are
stashed for the pull and put back after it. If they touch a file the pull changed,
they are left in the stash rather than risk a conflict, and orb says so; `git
stash pop` brings them back. A pull is only ever a fast-forward: a branch that has
diverged from its upstream is left as it is and listed, as is a repository in the
middle of a merge or a rebase, a detached HEAD, and a branch with no upstream. An
ignored file, such as a `.env`, that a commit pulled or a branch switched to starts
tracking stops the update rather than being overwritten.

`--default` switches each repository to its default branch first and pulls that.
The default branch is found without any forge's API: orb asks the remote what its
HEAD names (`git ls-remote --symref origin HEAD`). `origin/HEAD` only says what that
was when the repository was cloned, since git doesn't follow a default that changes
on the remote (from `master` to `main`, say), so it's used only when the remote
can't be reached, and failing that `main` or `master`. Changes on the branch you
leave stay in a stash named after it (`git stash list` shows `On feature: orb git
update: changes on feature before switching to main [orbit 1f2e3d4c]`; the token is
how orb tells its stash from one another worktree makes at the same moment).

**`branch delete`** lists every branch whose last commit is older than
`--older-than` (6 months by default: a number and `h`, `d`, `w`, `mo` or `y`), asks,
and deletes them. `--yes` doesn't ask, `--dry-run` only lists. Without a terminal it
needs one or the other. It never deletes the branch checked out, any branch checked
out, rebased or bisected in another worktree, the default branch, `main` or `master`.
A repository whose default branch can't be known (offline with no `origin/HEAD`, or
with no remote and neither `main` nor `master`) is left alone. Local branches are
deleted whether merged or not (`git branch -D`), since a branch squash-merged on a
forge never reads as merged; the list shows each branch's last commit, which
recreates it (`git branch <name> <commit>`) until git collects it as garbage, as
`orb git gc` and `clean` do.

`--remote` also deletes branches as old on origin (or a repository's only remote),
after fetching it, except the default branch, `main`, `master` and any branch that a
local branch it keeps tracks. A remote branch is named as origin has it, read back
through the remote's fetch refspecs, and a ref fetched from anything but a branch,
such as a pull request's, is never one, nor is an alias of another branch on the
remote, since deleting it there would delete the branch it names. A remote branch
at the same commit as another branch kept on the remote is never deleted either. A
server that speaks only git's protocol v0 can't say which branches are aliases, so
none of its branches are deleted (GitHub, GitLab and Bitbucket all speak v2). A
remote that pushes somewhere other than it fetches from (a `pushurl`) has none of
its branches deleted. They're gone for everyone, so read the list. A branch
somebody has committed to since it was listed is left alone, locally and on the
remote (`--force-with-lease`). Deleting doesn't run pre-push hooks.

**`gc`** runs `git gc` (`--aggressive` passes it on) and says how much space each
repository freed. **`clean`** runs `git clean -ffxd` and then `git gc`. That removes
everything git doesn't track, ignored files included: build output, `node_modules`,
`.env` files and other local configuration, editor settings, and repositories nested
in untracked directories. It can't be undone, so the paths are listed and you're
asked first, as for `branch delete`, and only those are removed: a file that
appears meanwhile stays, unless it's beneath a directory listed. A repository whose
paths couldn't be listed is left alone. Given a directory inside a repository,
`clean` cleans that directory alone, as `git clean` run there would; the other
commands work on the whole repository, as git does.

**`release-tag`** works in one repository, the one the path is or is inside (`.` by
default). It tags the tip of a branch, as the remote has it, with the next semantic
version, and pushes the tag. orbit's own releases are made with it (see
[Releasing](#releasing)).

- **What is tagged.** The branch checked out, or `--branch`, is fetched with every
  tag from the remote first: `origin`, the only remote, or `--remote`. Its tip there
  is what's tagged, so commits you haven't pushed are left out, and orb says so.
  `--push=false` tags without pushing, and is the only way in a repository with no
  remote. A shallow clone is refused, since its history hides the releases it is on
  (`git fetch --unshallow` fetches the rest).
- **The version.** The latest release the branch has (tags on other branches don't
  count) is bumped: minor by default, or `--bump patch` or `--bump major`.
  `--version 2.0.0` names it instead, and must come after the latest. Only tags the
  remote has count, so a tag never pushed can't decide the next version. A
  repository with no release yet starts at `0.1.0` (`0.0.1` with `--bump patch`,
  `1.0.0` with `--bump major`), as semver.org suggests for initial development.
- **The tag's name** follows the repository's convention: the prefix of the latest
  version tag on the branch, by when it was made. That is `v` for `v1.4.0`,
  `release-` for `release-1.4.0`, and `api/v` for `api/v1.4.0`. A repository with no
  version tag gets `v`. `--prefix` sets it, and `--prefix ""` means none. Tags
  that aren't semantic versions (`155`, `schema-3.17`) are ignored.
- **Pre-releases.** `--pre` makes a pre-release of the next version instead:
  `alpha`, `beta`, `rc`, or a word of your own. It's numbered after the last of its
  stage, from 1 (`v1.5.0-rc.1`, `-rc.2`, ...), counting ones numbered by hand as
  `beta3` or plain `beta` too. Without `--bump`, it continues a series already under
  way on the branch, so `v2.0.0-beta.2` is followed by `v2.0.0-beta.3`. A release
  (`v1.5.0`) can be made of the commit a release candidate was.
- **Never made:**
  - a tag that exists already, here or on the remote;
  - a pre-release of a version already released (the next unreleased one is
    pre-released instead, as on a branch behind `main`);
  - a release of a commit that's already released.

  After you're asked to go ahead, the branch and tags are read again, and a plan
  they have moved on from (a commit pushed, or a tag made meanwhile) is refused.
  The tag is checked for on the remote again just before it's pushed. When a push
  fails or is stopped, the remote is asked whether it took the tag anyway. If it
  did, the tag is released. If it has no such tag, the tag here is deleted, so
  trying again starts afresh. If it has another tag of the name, or can't be
  asked, the tag here stays and orb says so. A remote that pushes somewhere other
  than it fetches from (a `pushurl`, or several URLs) is refused, since its tags
  would be read from one place and the tag pushed to another. A prefix that would
  change how the version reads (`v1` before `2.0.0` is `v12.0.0`) is refused.

In a terminal, `release-tag` asks what nothing said, with the default first:

- what to release (minor, patch, major, the next of a pre-release series under way,
  or a new pre-release), unless `--bump`, `--pre` or `--version` was given in a
  flag, the environment or a config file;
- the branch, when HEAD is detached, with the default branch filled in;
- whether to go ahead, unless `--yes`.

Each choice shows the tag it makes. At the end orb prints the command that does
the same without asking (`orb git release-tag --bump minor --pre beta --yes`);
after a `--dry-run`, that command is a dry run too. Without a terminal, and with
`--json`, it needs `--yes` or `--dry-run`. `--json` prints what was tagged: the tag,
the version, the release it follows, the commit, the choice, and whether it was
created and pushed.

| Key | Flag | Default |
| --- | --- | --- |
| `git.concurrency` | `-j`, `--concurrency` | `0`, what suits the command |
| `git.timeout` | `--timeout` | `10m` per repository, `0` for none |
| `git.json` | `--json` | `false` |
| `git.branch-delete.older-than` | `--older-than` (on `branch delete`) | `6mo` |
| `git.release-tag.bump` | `--bump` | `minor` (asked in a terminal unless set) |
| `git.release-tag.pre` | `--pre` | none: a release |
| `git.release-tag.prefix` | `--prefix` | the latest tag's, or `v` |
| `git.release-tag.branch` | `--branch` | the branch checked out |
| `git.release-tag.remote` | `--remote` | `origin`, or the only remote |
| `git.release-tag.push` | `--push` | `true` |
| `git.release-tag.message` | `-m`, `--message` | `Release <tag>` |

`--default`, `--remote` (on `branch delete`), `--dry-run`, `--yes`, `--version` and
`--aggressive` say what one invocation does, so they are flags alone.

## Logging

orb writes one *canonical log entry* per invocation, rather than many scattered
lines. Each carries the same shape, so entries can be filtered and aggregated:

```json
{"level":"info","ts":"...","message":"orb git list succeeded in 235ms: listed 0 of 20 repositories, 20 listed",
 "service":"orbit","version":"v0.1.0","commit":"1f2e3d4","event.kind":"cli_invocation","cli.command":"orb git list",
 "cli.args":1,"git.paths":1,"git.repositories":20,"git.concurrency":20,"git.operation":"list","git.changed":0,
 "git.unchanged":20,"git.skipped":0,"git.failed":0,"git.cancelled":0,"exit_code":0,"duration":235,"event.outcome":"success"}
```

Entries go to a rotating file in the OS log directory, keeping stdout and stderr
for the command's own output: `~/.local/state/orbit/log/orbit.log` on Linux,
`~/Library/Logs/orbit/orbit.log` on macOS, `%LOCALAPPDATA%\orbit\Logs\orbit.log` on
Windows. `--log-stdout` sends them to stdout instead. Entries record config keys and
the number of arguments, never their values.

## Development

You need Go (the version in `go.mod`; with `GOTOOLCHAIN=auto`, the default, an older
Go fetches it) and git. GoReleaser is needed only for the `release-*` targets.

```bash
make                              # list every target
make build                        # bin/orb, with the version, commit and date stamped in
make install                      # the same, go installed
make run ARGS="git list ~/code"   # run the CLI from source
make check                        # what CI runs: gofmt, go vet, tests, race tests
make test                         # go test ./... (includes the architecture rules)
make fmt                          # gofmt -w
make deps-upgrade                 # every module to its latest version
make release-snapshot             # every release archive, locally into dist/
```

[docs/architecture.md](docs/architecture.md) describes how the code is laid out,
and how to add a tool.

### Versioning

`orb version` prints the version, commit and build date stamped into the binary at
link time (`-X main.version=... -X main.commit=... -X main.date=...`):

- `make build` and `make install` (through `scripts/build.sh`) stamp
  `git describe --tags --always --dirty`, so a build between releases reads
  `v0.3.0-4-g1f2e3d4`, and one with uncommitted changes ends in `-dirty`.
- GoReleaser stamps the release's tag.
- `go run` and a plain `go build` report `dev`.

`orb version --json` prints the same as JSON.

### Releasing

Releases are versioned `vMAJOR.MINOR.PATCH` and published to
[GitHub releases](https://github.com/andrerfcsantos/orbit/releases) by GoReleaser
(`.goreleaser.yaml`), from the `release` GitHub Actions workflow. Pushing a `v*` tag
starts it: it runs the tests, builds `orb` for every target, and uploads the archives
(`.tar.gz`, `.zip` for Windows) and their checksums with a changelog. A tag with a
pre-release (`v1.2.0-rc.1`) is published as a prerelease.

The workflows run GoReleaser Pro, with the license key from the `GORELEASER_KEY`
repository secret. `.goreleaser.yaml` uses no Pro-only features yet, so the free
GoReleaser runs the `release-*` targets locally too.

To release, tag the latest `origin/main` with the next version:

```bash
make release-tag-dry-run          # show the tag it would create (BUMP=patch|minor|major, default minor)
make release-tag BUMP=patch       # create and push it, which starts the release
```

Both run `orb git release-tag --branch main --bump $(BUMP)` from source, with every
other setting given too (`--remote origin --prefix v`, no pre-release), so no
`ORBIT_GIT_RELEASE_TAG_*` variable or config file changes orbit's own release.
`orb git release-tag --branch main` asks instead, and `--pre rc` makes a release
candidate.

If a release fails partway for a transient reason, re-run the `release` workflow
from the Actions tab with the same tag. A re-run builds the tagged commit again, so a
failure that needs a code or `.goreleaser.yaml` fix needs a new tag on the fixed
commit.

The `development release` workflow, run from the Actions tab, publishes a prerelease
of a branch, tag or commit, for trying it out before a release. It is tagged
`v<next version>-dev.<branch>.<run number>`.

`make release-snapshot` builds the same archives locally into `dist/` without
publishing anything, and `make release-check` validates `.goreleaser.yaml`. The `ci`
workflow does both on every push to `main` and every pull request, along with
`make check`.

## License

[MIT](LICENSE).
