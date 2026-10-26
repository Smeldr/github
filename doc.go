// Package github is the GitHub adapter of Smeldr's pull request check
// (smeldr.dev/core T918, D142 point 8, D143, D144).
//
// Core is host-neutral and never calls GitHub. This adapter is an
// http.Handler for a GitHub App's webhook: a pull_request opened,
// synchronized, reopened or made ready lists the changed files (up to the
// 3,000 GitHub lists) and calls core's CheckChange; a pull_request closed as
// merged calls RecordMerge. It posts the verdict as a commit status
// (pending while approval is awaited, success when approved or only
// commented, failure when not approved) and as one comment it keeps up to
// date: that comment is GitHub's copy, never stored by core.
//
// Mount [Adapter.CoreEvents] as a core webhook for changecheck.transitioned,
// so an approval answered in Smeldr moves the status on GitHub.
//
// Authentication is a GitHub App: the App ID and private key come from the
// operator ([Config]); the adapter signs a short-lived JWT and exchanges it
// for an installation token, cached until it expires. It stores and logs no
// secret. Webhooks are verified with X-Hub-Signature-256 (GitHub) and
// X-Smeldr-Signature (core).
package github
