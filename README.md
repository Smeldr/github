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
| `pull_request` opened, synchronize, reopened, ready_for_review | `CheckChange` with the changed files (up to 3,000) | status: `pending` (awaiting approval), `success` (approved, commented, nothing covered), `failure` (not approved); one comment |
| `pull_request` closed and merged | `RecordMerge` | nothing (an audit opens in Smeldr) |
| core `changecheck.transitioned` | `ChangeCheckOf` | the new status and comment |

A webhook that does not verify is 401 and calls nothing. A failure in core or at GitHub is 502, so GitHub marks the delivery failed and it can be redelivered: core's check is idempotent. The App needs read access to pull requests and write access to commit statuses and issues (comments). No secret is stored or logged.
