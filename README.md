# smeldr.dev/github

The GitHub adapter of Smeldr's pull request check (core T918). It turns a GitHub App's `pull_request` webhooks into core's `CheckChange` and `RecordMerge`, and posts each Check's verdict back as a commit status (`smeldr/check`) and one comment it updates in place.

```go
adapter, err := github.New(github.Config{
    Checker:           app,                                  // *smeldr.App
    Actor:             smeldr.User{ID: adapterUserID},       // holds check_change
    WebhookSecret:     []byte(os.Getenv("GITHUB_WEBHOOK_SECRET")),
    CoreWebhookSecret: []byte(os.Getenv("SMELDR_WEBHOOK_SECRET")),
    AppID:             os.Getenv("GITHUB_APP_ID"),
    PrivateKeyPEM:     []byte(os.Getenv("GITHUB_APP_PRIVATE_KEY")),
})
mux.Handle("POST /github/webhook", adapter)                 // the GitHub App's webhook URL
mux.Handle("POST /github/core-events", adapter.CoreEvents()) // a core webhook for changecheck.transitioned
```

| GitHub event | Core call | Posted back |
|---|---|---|
| `pull_request` opened, synchronize, reopened, ready_for_review | `CheckChange` with the changed files (up to 3,000) | status: `pending` (checking, waiting for approval), `success` (approved, nothing blocks the merge, nothing covered), `failure` (not approved); one comment |
| `pull_request` closed and merged | `RecordMerge` | nothing (an audit opens in Smeldr) |
| core `changecheck.transitioned` | `ChangeCheckOf` | the new status and comment |

A webhook that does not verify is 401 and calls nothing. A failure in core or at GitHub is 502, so GitHub marks the delivery failed and it can be redelivered: core's check is idempotent. The App needs read access to pull requests and write access to commit statuses and issues (comments). No secret is stored or logged.

The comment and the status show addresses and states only (D152): the Check's address and verdict, each touched Area's address, approval mode and verdict, and the addresses of the Decisions in force. Anyone who can read the pull request reads the comment, and GitHub does not know the instance's read rules. Area names and Decision titles appear only for a repository listed in `Config.NamesShown`, for example a private repository whose readers are all members; the list is empty by default and is logged at start.

Every sentence and phrase of the comment and the status comes from brand's approved wording (T921), held as fixed tables in `wording.go`: a heading `Smeldr check CHK5: waiting for approval`, one sentence for the case, a table of areas (`How changes are handled`, `Status`), the first ten Decisions that apply (then `and n more`), and a link to the Check. The status description is `CHK5: <phrase>`, or `nothing covered`. `Config.CheckURL` links the Check and `Config.ItemURL` every other address. The comment is not updated after a merge in this version: the audit lives on the instance.
