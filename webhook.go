// AGPL-3.0-or-later

package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	smeldr "smeldr.dev/core"
)

// maxWebhookBody bounds a webhook body.
const maxWebhookBody = 5 << 20

// pullRequestEvent is the part of GitHub's pull_request event the adapter
// reads.
type pullRequestEvent struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Merged         bool   `json:"merged"`
		MergeCommitSHA string `json:"merge_commit_sha"`
		ChangedFiles   int    `json:"changed_files"`
		Head           struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// ServeHTTP is the GitHub App's webhook endpoint. A request whose
// X-Hub-Signature-256 does not verify is 401 and nothing is called. A
// pull_request opened, synchronized, reopened or made ready is checked; one
// closed as merged is recorded as a merge. Other events are 204. A failure
// in core or at GitHub is 502, so GitHub shows the delivery as failed and it
// can be redelivered (core's check is idempotent).
func (a *Adapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
	if err != nil {
		http.Error(w, "unreadable body", http.StatusBadRequest)
		return
	}
	if !validGitHubSignature(a.cfg.WebhookSecret, body, r.Header.Get("X-Hub-Signature-256")) {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	if r.Header.Get("X-GitHub-Event") != "pull_request" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var ev pullRequestEvent
	if err := json.Unmarshal(body, &ev); err != nil || ev.Repository.FullName == "" || ev.Number <= 0 {
		http.Error(w, "not a pull_request event", http.StatusBadRequest)
		return
	}
	a.remember(ev.Repository.FullName, ev.Installation.ID)
	ctx := r.Context()
	switch {
	case ev.Action == "opened" || ev.Action == "synchronize" || ev.Action == "reopened" || ev.Action == "ready_for_review":
		err = a.checkPullRequest(ctx, ev)
	case ev.Action == "closed" && ev.PullRequest.Merged:
		err = a.recordMerge(ctx, ev)
	default:
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		logError(ctx, "pull_request not handled", err, "repository", ev.Repository.FullName, "number", ev.Number, "action", ev.Action)
		http.Error(w, "not handled", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validGitHubSignature checks "sha256=<hex>" against the HMAC-SHA256 of body,
// in constant time.
func validGitHubSignature(secret, body []byte, header string) bool {
	got, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	sig, err := hex.DecodeString(got)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(sig, mac.Sum(nil))
}

// CoreEvents is the endpoint for core's own webhook: a delivery of
// changecheck.transitioned (an approval, a not approved, a supersede) posts
// the Check's new verdict to GitHub. A request whose X-Smeldr-Signature does
// not verify, or is older than five minutes, is 401. Without
// Config.CoreWebhookSecret every request is 401.
func (a *Adapter) CoreEvents() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
		if err != nil {
			http.Error(w, "unreadable body", http.StatusBadRequest)
			return
		}
		if !a.validCoreSignature(body, r.Header.Get("X-Smeldr-Signature"), r.Header.Get("X-Smeldr-Timestamp")) {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		var ev smeldr.WebhookEventPayload
		if err := json.Unmarshal(body, &ev); err != nil {
			http.Error(w, "not a core event", http.StatusBadRequest)
			return
		}
		if ev.Event != "changecheck.transitioned" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var data struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(ev.Data, &data); err != nil || data.ID == "" {
			http.Error(w, "no Check in the event", http.StatusBadRequest)
			return
		}
		if err := a.Refresh(r.Context(), data.ID); err != nil {
			logError(r.Context(), "Check not posted", err, "check", data.ID)
			http.Error(w, "not posted", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// validCoreSignature checks core's "sha256=<hex>" over "<timestamp>.<body>"
// and that the timestamp is recent.
func (a *Adapter) validCoreSignature(body []byte, header, timestamp string) bool {
	if len(a.cfg.CoreWebhookSecret) == 0 {
		return false
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || a.now().Sub(time.Unix(ts, 0)).Abs() > 5*time.Minute {
		return false
	}
	got, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	sig, err := hex.DecodeString(got)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, a.cfg.CoreWebhookSecret)
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	return hmac.Equal(sig, mac.Sum(nil))
}
