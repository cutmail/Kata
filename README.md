<h1 align="center">kata</h1>

<p align="center">
  <b>A package manager for AI agent skills.</b><br>
  Declare your skills and commands in one YAML file, and get the same setup on every machine.
</p>

<p align="center">
  <a href="https://github.com/cutmail/kata/actions/workflows/ci.yml"><img src="https://github.com/cutmail/kata/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://goreportcard.com/report/github.com/cutmail/kata"><img src="https://goreportcard.com/badge/github.com/cutmail/kata" alt="Go Report Card"></a>
  <a href="https://pkg.go.dev/github.com/cutmail/kata"><img src="https://pkg.go.dev/badge/github.com/cutmail/kata.svg" alt="Go Reference"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-blue.svg" alt="License: MIT"></a>
</p>

<p align="center">
  <a href="README.ja.md">日本語</a>
</p>

---

Agent skills tend to accumulate in `~/.claude/` by hand: copied from a gist, tweaked in place,
and impossible to reproduce when you switch machines. **kata** turns that directory into
something you declare rather than something you accumulate.

Commit a `kata.yml`, a `kata.lock`, and your own skills under `local/`. On any other machine,
`kata sync` puts everything back — from the same commit, down to the same file.

```console
$ kata sync
+ my-review  skill  ~/.claude/skills/my-review
+ pdf        skill  ~/.claude/skills/pdf
2 created, 0 updated, 0 removed, 0 unchanged
```

## Features

- **Reproducible.** `kata.lock` pins the exact commit, so `ref: main` still resolves to the same
  tree on every machine.
- **Declarative.** `kata sync` converges the deployed state onto the manifest and is safe to run
  repeatedly. Drop a package from the manifest and it gets undeployed.
- **Non-destructive.** kata only removes what kata created. Anything you placed by hand is never
  overwritten or deleted without `--force`.
- **Bring your own skills.** Sources can be a git repository (with a subdirectory), or a directory
  inside your own repo — so skills you write yourself are versioned alongside the manifest.
- **Single binary.** Pure Go, no runtime dependencies, builds for macOS, Linux and Windows.

## Install

### Homebrew (macOS)

```console
$ brew install --cask cutmail/tap/kata
```

Upgrade later with `brew upgrade --cask kata`. Homebrew casks are macOS-only; on Linux use one of
the options below.

kata is not signed or notarized by Apple, and the cask does **not** clear the quarantine
attribute for you — see [Gatekeeper on macOS](#gatekeeper-on-macos) below.

### Download a binary

Pre-built binaries for macOS, Linux and Windows (amd64 and arm64) are attached to every
[release](https://github.com/cutmail/kata/releases/latest).

```console
$ curl -L https://github.com/cutmail/kata/releases/latest/download/kata_darwin_arm64.tar.gz | tar xz
$ sudo mv kata /usr/local/bin/
```

### go install

```console
$ go install github.com/cutmail/kata/cmd/kata@latest
```

### From source

```console
$ git clone https://github.com/cutmail/kata && cd kata
$ go build -o ~/bin/kata ./cmd/kata
```

Verify the installation with:

```console
$ kata --version
kata version 0.1.0
```

### Verify a download

Every release archive carries a [GitHub-native SLSA build
provenance](https://docs.github.com/actions/security-guides/using-artifact-attestations-to-establish-provenance-for-builds)
attestation, proving the archive was built by this repository's release workflow from the tagged
commit. Check it with the [GitHub CLI](https://cli.github.com/):

```console
$ gh attestation verify kata_darwin_arm64.tar.gz -R cutmail/kata
```

`checksums.txt` is published in the same GitHub Release as the archives, so it only protects
against a corrupted download — anyone able to replace a release can replace its checksums too.
The attestation is what protects against a replaced release, so prefer it.

### Gatekeeper on macOS

kata is not signed or notarized by Apple. macOS quarantines anything downloaded from the internet,
so the first launch may be blocked with a message about an unidentified developer. This applies to
the Homebrew cask as well: **kata deliberately does not strip the quarantine attribute for you.**

A package that silently removes quarantine removes your only warning that unsigned code is about
to run — and it does so on every `brew upgrade`, for every user, forever. Whether to trust an
unsigned binary is your call to make, once you have verified where it came from.

Verify the download first, then choose one of:

- Right-click the binary in Finder and pick **Open**, then confirm. macOS remembers the choice.
- Or clear the attribute yourself:

  ```console
  $ xattr -d com.apple.quarantine /usr/local/bin/kata
  ```

  With Homebrew, the installed path is `$(brew --prefix)/bin/kata`.

## Quick start

Create a repository for your agent configuration:

```console
$ mkdir my-agent-config && cd my-agent-config
$ git init && kata init
created kata.yml
put your own skills under local/ and register them with 'kata add'
```

Add a skill from a public repository:

```console
$ kata add anthropics/skills --path skills/pdf --ref main
added pdf (skill) to kata.yml
+ pdf  skill  ~/.claude/skills/pdf
1 created, 0 updated, 0 removed, 0 unchanged
```

Add a skill you wrote yourself:

```console
$ mkdir -p local/skills/my-review && $EDITOR local/skills/my-review/SKILL.md
$ kata add ./local/skills/my-review
added my-review (skill) to kata.yml
+ my-review  skill  ~/.claude/skills/my-review
```

Check what is deployed:

```console
$ kata list
NAME       TYPE   STATUS  PROFILES  SOURCE                                            DEST
pdf        skill  linked  all       git+https://github.com/anthropics/skills@f17010c  ~/.claude/skills/pdf
my-review  skill  linked  all       local:./local/skills/my-review                    ~/.claude/skills/my-review
```

Already have a `~/.claude` full of hand-made skills? Bring them in without disturbing anything:

```console
$ kata import --dry-run
dry run: nothing was changed
+ my-review  skill    ~/.claude/skills/my-review  -> ./local/skills/my-review
+ pr         command  ~/.claude/commands/pr.md    -> ./local/commands/pr.md
  skip pdf  managed by kata (declared in ~/dotfiles)
2 to import, 1 skipped
```

Commit and push:

```console
$ git add -A && git commit -m "my agent setup" && git push
```

On another machine, clone and sync — that's the whole story:

```console
$ git clone <your-repo> && cd <your-repo>
$ kata sync
```

## Commands

| Command | Description |
| --- | --- |
| `kata init` | Create a `kata.yml` and a `local/` directory |
| `kata add <source>` | Add a package to the manifest and deploy it |
| `kata sync` | Converge the deployed state onto the manifest (idempotent) |
| `kata list` | Show every declared package and its current state |
| `kata status` | Report only what is out of sync; exits 1 when anything is |
| `kata import` | Adopt entries already in `~/.claude` into the manifest |
| `kata update [name...]` | Re-resolve floating refs and move the lock forward |
| `kata doctor` | Check the environment and explain anything that looks wrong |
| `kata prune` | Remove cached content nothing refers to any more |
| `kata remove <name>` | Remove a package from the manifest and undeploy it |

`kata add` flags:

| Flag | Description |
| --- | --- |
| `--type skill\|command\|agent` | Package type (inferred when omitted; `agent` must be explicit) |
| `--name <name>` | Package name (defaults to the last path element) |
| `--path <subdir>` | Subdirectory inside the repository or archive |
| `--ref <branch\|tag>` | Branch or tag (defaults to the default branch) |
| `--url` | Treat the source as an archive URL rather than a git repository |
| `--scope user\|project` | Where to deploy (defaults to `user`) |
| `--strategy link\|copy\|auto` | How to deploy (defaults to `link`) |
| `--profile <name>` | Profiles this package belongs to (repeatable) |
| `--no-sync` | Only update the manifest, do not deploy |

`kata sync` flags:

| Flag | Description |
| --- | --- |
| `--dry-run` | Show what would change without touching anything |
| `--force` | Move an existing file into the backup directory before deploying |
| `--profile <name>` | Only deploy packages in this profile (defaults to `$KATA_PROFILE`) |
| `--prune` | Also undeploy packages the profile leaves out |
| `--adopt` | Take ownership of a copied destination whose contents already match |

`kata import` flags:

| Flag | Description |
| --- | --- |
| `--dry-run` | Show what would be imported without writing anything |
| `--adopt` | Move the originals aside and link to the copies under `local/` |
| `--type <list>` | Only import these types (comma separated) |

`kata prune` flags:

| Flag | Description |
| --- | --- |
| `--apply` | Actually remove the listed items (nothing is removed without it) |
| `--store` / `--staging` / `--state` | Which kinds to consider (defaults to store and staging) |
| `--older-than <dur>` | Only consider items older than this |

The source argument accepts `owner/repo`, `github.com/owner/repo`, a full git URL, an archive URL
ending in `.tar.gz`/`.tgz`/`.zip`, or a path inside the manifest directory. Type and name are
inferred when you leave them out: a `.md` file becomes a `command`, a directory becomes a `skill`.
An `agent` is also a `.md` file, so it cannot be inferred — pass `--type agent`.

`kata list` reports one of `linked`, `copied`, `missing`, `drifted`, `broken`, or `orphan`, so you
can tell at a glance whether the machine still matches what you declared. `kata status` shows only
the entries that need attention and exits with 1 when there are any, which makes it usable in CI.

## Manifest

```yaml
version: 1

defaults:
  scope: user
  strategy: link

# Reusable sources, referenced by packages via `from:`
sources:
  anthropic:
    git: https://github.com/anthropics/skills
    ref: main

packages:
  # A subdirectory of a shared source
  - name: pdf
    type: skill
    from: anthropic
    path: skills/pdf

  # Pinned to a tag
  - name: mcp-builder
    type: skill
    git: https://github.com/anthropics/skills
    ref: v1.2.0
    path: skills/mcp-builder

  # An archive over HTTPS. The content digest is the pin.
  - name: toolkit
    type: skill
    url: https://example.com/toolkit-1.4.0.tar.gz
    sha256: 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
    path: toolkit-1.4.0/skills/toolkit

  # Your own skill, versioned in this repository
  - name: my-review
    type: skill
    local: ./local/skills/my-review

  # A single-file command
  - name: pr
    type: command
    local: ./local/commands/pr.md

  # A subagent definition
  - name: reviewer
    type: agent
    local: ./local/agents/reviewer.md

  # Only deployed when you ask for this profile
  - name: work-notes
    type: skill
    local: ./local/skills/work-notes
    profiles: [work]

  # Copied into the repository's own .claude so the team shares it
  - name: house-style
    type: skill
    local: ./local/skills/house-style
    scope: project
    strategy: copy
```

`kata.lock` records the resolved commit — or, for a `url` source, the content digest — for every
package and belongs in version control. `sync` treats the lock as the source of truth, which is
what makes a floating `ref` reproducible.

Because the lock wins, editing `ref:` or `git:` in the manifest has no effect on its own. That is
deliberate, but it is also invisible, so `kata doctor` reports the mismatch and points you at
`kata update`. A `sha256:` that disagrees with the lock is different: kata stops rather than
picking one, because a digest is a claim about integrity rather than a moving reference.

### Profiles

A package with no `profiles:` is always selected. `kata sync --profile work` narrows the run to
packages that list `work`, and leaves everything else exactly as it is — deployed packages stay
deployed, and **every lock entry is kept**, so pinning is never lost by narrowing. Pass `--prune`
if you also want the packages outside the profile undeployed.

Set `KATA_PROFILE` in your shell to make a machine default.

### Scopes and strategies

`scope: project` deploys into `.claude/` next to `kata.yml` rather than `~/.claude`. With
`strategy: link` those links hold absolute paths, so add `.claude/` to `.gitignore` and let
`kata sync` recreate them. With `strategy: copy` the deployment is real content and can be
committed, which is how a team shares a directory.

`strategy: auto` picks `link` where symlinks work and `copy` where they do not — useful on Windows.

## How it works

```
kata.yml ──┐
           ├─→ fetch   git → cached in ~/.kata/store
kata.lock ─┘           local → used in place, straight from your repo
                              │
                              ↓
                    ~/.claude/skills/<name>          symlink
                    ~/.claude/commands/<name>.md
                              │
                              ↓
                    ~/.kata/state.json   record of what kata deployed
```

- Links are created as a temporary link and then `rename`d into place, so a swap is atomic and
  never leaves a half-applied state.
- `~/.kata/store` is a pure cache keyed by URL and commit. Delete it any time; `kata sync`
  rebuilds it.
- `state.json` is what makes removal safe: kata only ever undeploys entries it recorded itself.

### Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `KATA_HOME` | `~/.kata` | Cache, state and backups |
| `CLAUDE_CONFIG_DIR` | `~/.claude` | Deployment target |

## Safety

If a deploy target is occupied by a real file or directory that kata did not create, `sync`
reports that package as failed and **leaves your file exactly as it is**:

```console
$ kata sync
! handmade  skill  ~/.claude/skills/handmade
  error: handmade: destination is occupied by a non-kata file: ~/.claude/skills/handmade
0 created, 0 updated, 0 removed, 2 unchanged, 1 failed
```

Pass `--force` to move it to `~/.kata/backups/<timestamp>/` first. Nothing is ever deleted
outright, and the sources themselves — including your `local/` directory — are never modified.

With `strategy: copy` there is no symlink to prove ownership, so kata records a digest of what it
wrote and re-checks it before touching anything. If you edited the deployed copy, kata says so and
keeps your edit — `--force` moves it aside, and never deletes it:

```console
$ kata sync
! house-style  skill  ~/.claude/skills/house-style
  error: house-style: destination was modified after kata deployed it
0 created, 0 updated, 0 removed, 1 unchanged, 1 failed
```

`kata import` follows the same rule from the other direction: by default it only copies into
`local/` and writes the manifest, and never touches what is already in `~/.claude`. Pass `--adopt`
when you want the originals moved aside and replaced with links.

`kata prune` removes nothing unless you pass `--apply`, only ever considers paths it constructed
itself inside `~/.kata`, and treats a cache as live if **any** repository on the machine still
refers to it. It never touches `~/.kata/backups`: those are your files, and deleting them is
left to you.

To report a security problem, see [SECURITY.md](SECURITY.md) — please do not open a public issue
for one.

## Status

What the current release supports:

| | Supported |
| --- | --- |
| Types | `skill`, `command`, `agent` |
| Sources | git (with subdirectory), archive URL (`tar.gz`, `tgz`, `zip`), local |
| Strategies | `link`, `copy`, `auto` |
| Scopes | `user` (`~/.claude`), `project` (`<repo>/.claude`) |
| Selection | profiles |
| Targets | Claude Code |

A manifest that uses `type: agent`, `url:`, `scope: project`, `strategy: copy` or `profiles:`
cannot be read by kata 0.1.x, even though the manifest `version` is still 1.

### Roadmap

- MCP server configuration merging
- Additional targets beyond Claude Code
- A published JSON Schema for `kata.yml`

## Development

```console
$ go test ./...          # includes tests that hit the network
$ go test -short ./...   # offline only
$ go vet ./...
```

The layout follows the two interfaces the design is built around — `source.Fetcher` for where
content comes from, and `target.Resolver` for where it goes:

```
cmd/kata/          CLI
internal/manifest  kata.yml parsing, normalization and validation
internal/lockfile  kata.lock
internal/state     record of deployed entries
internal/store     content cache
internal/source    fetchers (git, local)
internal/target    destination resolution (Claude Code)
internal/linker    symlink deployment
internal/app       orchestration
```

### Releasing

Releases are cut by [GoReleaser](https://goreleaser.com/) from a tag. Pushing a `v*` tag builds
the cross-platform archives, publishes them to GitHub Releases, and updates the Homebrew cask
in [`cutmail/homebrew-tap`](https://github.com/cutmail/homebrew-tap):

```console
$ git tag -a v0.1.0 -m "v0.1.0" && git push origin v0.1.0
```

Artifacts are attested with `actions/attest-build-provenance`, so every archive can be traced
back to the workflow run and commit that produced it.

Updating the tap needs a `HOMEBREW_TAP_TOKEN` secret. It must be a **fine-grained** personal
access token scoped to `cutmail/homebrew-tap` alone, with `Contents: Read and write` and nothing
else — a classic PAT with the `repo` scope would hand every repository the owner can reach to
anyone who obtains the token. See [SECURITY.md](SECURITY.md#release-pipeline).

To check the configuration without publishing anything:

```console
$ goreleaser check
$ goreleaser release --snapshot --clean
```

## Contributing

Issues and pull requests are welcome. Please run `go test ./...` and `gofmt -l .` before opening
a PR. For a new source or target, implementing `source.Fetcher` or `target.Resolver` should be
all it takes.

## License

MIT — see [LICENSE](LICENSE).
