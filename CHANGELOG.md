# Changelog: smeldr.dev/github

## [Unreleased]

### Added

- The GitHub adapter of the pull request check (core T918): the App webhook handler (`pull_request` to `CheckChange` and `RecordMerge`), the commit status and the comment, the core events handler for `changecheck.transitioned`, GitHub App authentication (RS256 JWT, cached installation tokens), webhook signature checks. Needs smeldr.dev/core with the pull request check.
