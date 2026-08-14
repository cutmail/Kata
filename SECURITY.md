# Security Policy

kata fetches content from third-party sources — git repositories, archive URLs — and places it
into directories that AI agents read and act on, including `~/.claude`. A flaw that lets a
source write outside its intended destination, or that lets a malicious repository overwrite a
file kata did not create, is a security bug, not a feature request. Please report it as one.

## Supported versions

Only the latest released version receives security fixes. kata is pre-1.0 and moves quickly;
there are no long-term support branches, and fixes are not backported.

| Version | Supported |
| --- | --- |
| Latest release | Yes |
| Anything older | No — upgrade first |

## Reporting a vulnerability

**Please do not open a public issue for a security problem.**

Report it through GitHub's Private vulnerability reporting:

1. Go to <https://github.com/cutmail/kata/security/advisories/new>
2. Describe the issue, the version you tested, and the platform
3. Include the smallest reproduction you can manage — a `kata.yml` plus the commands you ran is
   ideal

If you cannot use GitHub Advisories, open a regular issue that says only that you have a security
report and asks for a private channel. Do not put the details in it.

### What to expect

- **Acknowledgement within 7 days.** If you have not heard back by then, assume the report was
  missed and ping the issue tracker without details.
- **An assessment within 14 days**, including whether the report is accepted and a rough
  timeline.
- **A fix in the next release** for accepted reports, with a GitHub Security Advisory published
  once the fix is available.
- **Credit** in the advisory, unless you ask otherwise.

Please give a reasonable window before public disclosure. There is no bug bounty.

### In scope

- Path traversal or symlink attacks that let a source write outside its declared destination
- Overwriting or deleting files kata did not create, without `--force`
- Command injection reachable from manifest contents, source URLs, or refs
- Credential or token leakage — for example, a remote URL with embedded credentials appearing in
  output, logs, or errors
- Cache or state poisoning that causes a later `sync` to deploy content the manifest did not ask
  for
- Anything in the release pipeline that would let a third party ship a kata binary users trust

### Out of scope

- **The contents of the skills you install.** kata is a package manager; it deploys what you tell
  it to. Reviewing a third-party skill before installing it is your responsibility, exactly as it
  is with any other package manager.
- Vulnerabilities in an upstream dependency with no impact on kata — report those upstream.
- Findings that require an attacker who already has write access to the user's home directory or
  to the machine.

## Verifying a release

Release artifacts are built by GoReleaser in GitHub Actions and carry a GitHub-native SLSA build
provenance attestation. `checksums.txt` is published in the same GitHub Release as the artifacts,
so an attacker who can replace a release can replace its checksums too. The attestation cannot be
forged the same way — verify it:

```console
$ gh attestation verify kata_darwin_arm64.tar.gz -R cutmail/kata
```

Binaries are **not** signed or notarized by Apple. On macOS, Gatekeeper will refuse to run a
downloaded binary until the quarantine attribute is cleared. kata does not clear it for you — not
from the installer, and not from the Homebrew cask. Removing it is a decision you make after you
have verified what you downloaded:

```console
$ xattr -d com.apple.quarantine /usr/local/bin/kata
```

## Release pipeline

Practices that maintainers are expected to keep in place. Changing any of them is a security
change and should be reviewed as one.

- **Workflow actions are pinned to commit SHAs.** Tags are mutable; a compromised upstream action
  can rewrite a tag and every workflow referencing it picks up the change silently. The version
  is recorded in a trailing comment, and Dependabot updates both.
- **The release workflow grants permissions per job**, not workflow-wide: `contents: write` for
  the release, plus `id-token: write` and `attestations: write` for provenance.
- **`HOMEBREW_TAP_TOKEN` must be a fine-grained personal access token scoped to
  `cutmail/homebrew-tap` alone, with `Contents: Read and write` and nothing else.** A classic PAT
  with the `repo` scope grants read and write access to *every* repository the owner can reach; a
  leak of that token is a compromise of the whole account, not just the tap. Give the token the
  shortest expiry you can live with and rotate it on schedule.
- **The tap is a distribution channel with real reach.** Anyone who can write to
  `cutmail/homebrew-tap` can push a cask that every `brew upgrade` installs. Treat write access to
  it as equivalent to write access to this repository.
- **`go mod tidy` never runs during a release.** It would rewrite `go.mod` and `go.sum` mid-build;
  CI enforces that the tree is already tidy instead.
- **CI runs `govulncheck`** against dependencies and the Go standard library, and the Go version
  is taken from `go.mod` so releases cannot be built with an unpatched toolchain.
