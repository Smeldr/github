# Changelog: smeldr.dev/github

## [Unreleased]

### Added

- **A pull request closed without a merge is reported to core (core T1294).** The webhook now calls `RecordClose` for a `pull_request` closed with `merged: false`, with the repository, the number and the head commit. Core then no longer reopens that pull request's Check when the Decisions for its Areas change, and withdraws the Check's open approval requests. A failure in core is 502, so GitHub can redeliver; `RecordClose` is idempotent. `Checker` is not widened: the adapter calls `RecordClose` only when the configured Checker has it (`*smeldr.App` from core v1.194.0). With a Checker without it (an own implementation, or a wrapper around the App that does not pass it on), the event is 204 and nothing is called, as before: closed pull requests are not reported, and their Checks can still be reopened.
- go.mod requires smeldr.dev/core v1.194.0 (was v1.174.0).

---

## [0.1.2] - 2026-11-13

### Security

- **`smeldr.dev/github` now builds with Go 1.26.11, which fixes a net/http vulnerability (T1197).** govulncheck reported GO-2026-6859 in the standard library's net/http for this module. The toolchain line in go.mod now names go1.26.11 instead of go1.26.10, so this module's own build and tests use it. The `go 1.26.5` line is unchanged: a Go command older than go1.26.11 fetches go1.26.11 by itself, unless `GOTOOLCHAIN=local` is set, and then Go 1.26.5 or later still builds it. A program that imports this module builds with its own toolchain. There is no code change.

## [0.1.1] - 2026-10-26

### Fixed

- The README named the wrong permission: a comment on a pull request needs **Pull requests: read and write**, not read (found live in core T945: with read, posting the comment answered 403). It also says that an installation must accept a widened permission before it applies.
- A call GitHub refuses now returns GitHub's own `message` in the error, on one line and at most 300 characters (for example `403 Forbidden: Resource not accessible by integration`), instead of the status alone. Nothing from the request goes into the error (core T950).

## [0.1.0] - 2026-10-26

First published version (core T940). Requires smeldr.dev/core v1.174.0 or later; the example server mounts it with `ENABLE_GITHUB` and environment variables only.

### Added

- The GitHub adapter of the pull request check (core T918): the App webhook handler (`pull_request` to `CheckChange` and `RecordMerge`), the commit status and the comment, the core events handler for `changecheck.transitioned`, GitHub App authentication (RS256 JWT, cached installation tokens), webhook signature checks. Needs smeldr.dev/core with the pull request check.
- D152: the comment shows addresses and states only; Area names and Decision titles only for a repository in `Config.NamesShown` (empty by default). The commit status holds the Check address and a state.
- The comment and the status use brand's approved wording (T921): fixed phrases for the verdict, the sentence of each case, the area modes and states, `nothing covered`; the first ten Decisions then `and n more`; partly checked as `Smeldr checked the first 3,000 of 4,312 files.` New `Config.ItemURL` links Area and Decision addresses. No update after a merge.
