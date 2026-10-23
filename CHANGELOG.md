# Changelog: smeldr.dev/github

## [Unreleased]

### Added

- The GitHub adapter of the pull request check (core T918): the App webhook handler (`pull_request` to `CheckChange` and `RecordMerge`), the commit status and the comment, the core events handler for `changecheck.transitioned`, GitHub App authentication (RS256 JWT, cached installation tokens), webhook signature checks. Needs smeldr.dev/core with the pull request check.
- D152: the comment shows addresses and states only; Area names and Decision titles only for a repository in `Config.NamesShown` (empty by default). The commit status holds the Check address and a state.
- The comment and the status use brand's approved wording (T921): fixed phrases for the verdict, the sentence of each case, the area modes and states, `nothing covered`; the first ten Decisions then `and n more`; partly checked as `Smeldr checked the first 3,000 of 4,312 files.` New `Config.ItemURL` links Area and Decision addresses. No update after a merge.
