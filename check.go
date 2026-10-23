// AGPL-3.0-or-later

package github

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	smeldr "smeldr.dev/core"
)

// commentMarker finds the adapter's own comment on a pull request again.
const commentMarker = "<!-- smeldr-check -->"

// checkPullRequest checks the pull request's head commit and posts the
// verdict.
func (a *Adapter) checkPullRequest(ctx context.Context, ev pullRequestEvent) error {
	repo, number, installation := ev.Repository.FullName, ev.Number, ev.Installation.ID
	paths, err := a.changedFiles(ctx, installation, repo, number)
	if err != nil {
		return err
	}
	total := ev.PullRequest.ChangedFiles
	if total < len(paths) {
		total = len(paths)
	}
	res, err := a.cfg.Checker.CheckChange(a.coreCtx(), smeldr.ChangeInput{
		Repository: repo, Number: number, HeadCommit: ev.PullRequest.Head.SHA, Paths: paths, FilesTotal: total,
	})
	if err != nil {
		return fmt.Errorf("check %s#%d: %w", repo, number, err)
	}
	return a.post(ctx, installation, repo, number, ev.PullRequest.Head.SHA, res)
}

// recordMerge records that the pull request was merged.
func (a *Adapter) recordMerge(ctx context.Context, ev pullRequestEvent) error {
	_, err := a.cfg.Checker.RecordMerge(a.coreCtx(), smeldr.MergeInput{
		Repository: ev.Repository.FullName, Number: ev.Number, HeadCommit: ev.PullRequest.Head.SHA, MergeCommit: ev.PullRequest.MergeCommitSHA,
	})
	if err != nil {
		return fmt.Errorf("record the merge of %s#%d: %w", ev.Repository.FullName, ev.Number, err)
	}
	return nil
}

// Refresh posts a Check's current verdict to its pull request: the status
// on its head commit and the comment. It needs the repository's
// installation, learnt from an earlier webhook; a superseded Check posts
// nothing (the newer one owns the pull request).
func (a *Adapter) Refresh(ctx context.Context, check string) error {
	res, err := a.cfg.Checker.ChangeCheckOf(a.coreCtx(), check)
	if err != nil {
		return fmt.Errorf("read the Check %s: %w", check, err)
	}
	c := res.Check
	if c == nil || string(c.Status) == smeldr.ChangeCheckSuperseded {
		return nil
	}
	installation, err := a.installationOf(ctx, c.Repository)
	if err != nil {
		return err
	}
	return a.post(ctx, installation, c.Repository, c.Number, c.HeadCommit, res)
}

// post sets the commit status and writes or updates the comment.
func (a *Adapter) post(ctx context.Context, installation int64, repo string, number int, head string, res smeldr.ChangeCheckResult) error {
	state, description := statusOf(res)
	status := map[string]any{"state": state, "context": a.cfg.StatusContext, "description": description}
	if a.cfg.CheckURL != "" && res.Check != nil {
		status["target_url"] = fmt.Sprintf(a.cfg.CheckURL, res.Check.Address)
	}
	if err := a.api(ctx, installation, http.MethodPost, fmt.Sprintf("/repos/%s/statuses/%s", repo, head), status, nil); err != nil {
		return err
	}
	// A pull request that touches nothing covered passes silently: no
	// comment (walk-through answer 8b). One written for an earlier commit
	// is updated, so it never shows a stale verdict.
	return a.comment(ctx, installation, repo, number, commentBody(res, a.namesShown(repo)), res.Covered && res.Check != nil)
}

// statusOf maps a Check to a commit status: pending while it awaits
// approval, failure when not approved, success otherwise. The description
// holds the Check address and a state, never a name or title (D152).
func statusOf(res smeldr.ChangeCheckResult) (state, description string) {
	if !res.Covered || res.Check == nil {
		return "success", "No Area covers these files"
	}
	c := res.Check
	switch string(c.Status) {
	case smeldr.ChangeCheckApproved:
		return "success", c.Address + ": approved"
	case smeldr.ChangeCheckNotApproved:
		return "failure", c.Address + ": not approved"
	case smeldr.ChangeCheckAwaitingApproval, smeldr.ChangeCheckChecking:
		return "pending", c.Address + ": awaiting approval"
	default:
		return "success", c.Address + ": " + string(c.Status)
	}
}

// commentBody is the host's copy of the Check, never stored by core. It
// shows addresses and states; Area names and Decision titles only when names
// is true (D152): instance records follow the instance's read rules, and
// the comment is read by anyone who can read the pull request.
func commentBody(res smeldr.ChangeCheckResult, names bool) string {
	var b strings.Builder
	b.WriteString(commentMarker + "\n")
	if !res.Covered || res.Check == nil {
		b.WriteString("**Smeldr check:** the latest commit touches nothing an Area covers.\n")
		return b.String()
	}
	c := res.Check
	fmt.Fprintf(&b, "**Smeldr check %s:** %s (%d of %d files checked", c.Address, c.Status, c.FilesChecked, c.FilesTotal)
	if c.CheckedPartly {
		b.WriteString(", checked partly")
	}
	b.WriteString(")\n\n")
	if len(res.Areas) > 0 {
		b.WriteString("| Area | Mode | Verdict |\n|---|---|---|\n")
		for _, area := range res.Areas {
			label := area.Address
			if names && area.Name != "" {
				label += " " + area.Name
			}
			fmt.Fprintf(&b, "| %s | %s | %s |\n", label, area.ApprovalMode, area.Verdict)
		}
	}
	if len(res.Decisions) > 0 {
		b.WriteString("\nDecisions in force:\n")
		for _, d := range res.Decisions {
			if names && d.Title != "" {
				fmt.Fprintf(&b, "- %s %s\n", d.Address, d.Title)
			} else {
				fmt.Fprintf(&b, "- %s\n", d.Address)
			}
		}
	}
	return b.String()
}

// comment updates the adapter's own comment on the pull request; when it has
// none, it writes one only if create is true.
func (a *Adapter) comment(ctx context.Context, installation int64, repo string, number int, body string, create bool) error {
	for page := 1; page <= 10; page++ {
		var comments []struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
			User struct {
				Type string `json:"type"`
			} `json:"user"`
			App *struct {
				ID int64 `json:"id"`
			} `json:"performed_via_github_app"`
		}
		if err := a.api(ctx, installation, http.MethodGet, fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100&page=%d", repo, number, page), nil, &comments); err != nil {
			return err
		}
		for _, c := range comments {
			// Only the App's own comment: anyone who can comment could plant
			// the marker (S3158 fix 2).
			own := c.User.Type == "Bot" && c.App != nil && strconv.FormatInt(c.App.ID, 10) == a.cfg.AppID
			if own && strings.HasPrefix(c.Body, commentMarker) {
				return a.api(ctx, installation, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/comments/%d", repo, c.ID), map[string]any{"body": body}, nil)
			}
		}
		if len(comments) < 100 {
			break
		}
	}
	if !create {
		return nil
	}
	return a.api(ctx, installation, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/comments", repo, number), map[string]any{"body": body}, nil)
}
