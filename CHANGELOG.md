# Changelog: smeldr.dev/github

## [Unreleased]

### Added

- The GitHub adapter of the pull request check (core T918): the App webhook handler (`pull_request` to `CheckChange` and `RecordMerge`), the commit status and the comment, the core events handler for `changecheck.transitioned`, GitHub App authentication (RS256 JWT, cached installation tokens), webhook signature checks. Needs smeldr.dev/core with the pull request check.
- D152: the comment shows addresses and states only; Area names and Decision titles only for a repository in `Config.NamesShown` (empty by default). The commit status holds the Check address and a state.
