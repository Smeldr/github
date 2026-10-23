// AGPL-3.0-or-later

package github

import (
	"fmt"
	"strconv"
	"strings"

	smeldr "smeldr.dev/core"
)

// The fixed phrases of the comment and the commit status, from brand's
// approved wording (smeldr/brand drafts/pr-comment-wording-v1.md, T921,
// approved by Peter 2026-10-23). The adapter writes no prose of its own:
// every sentence is one of these, with addresses and counts filled in.

// verdictPhrase is the heading's and the status's phrase for a Check state.
var verdictPhrase = map[string]string{
	smeldr.ChangeCheckChecking:         "checking",
	smeldr.ChangeCheckCommented:        "nothing blocks the merge",
	smeldr.ChangeCheckAwaitingApproval: "waiting for approval",
	smeldr.ChangeCheckApproved:         "approved",
	smeldr.ChangeCheckNotApproved:      "not approved",
}

// statusState is the host's status state for a Check state.
var statusState = map[string]string{
	smeldr.ChangeCheckChecking:         "pending",
	smeldr.ChangeCheckCommented:        "success",
	smeldr.ChangeCheckAwaitingApproval: "pending",
	smeldr.ChangeCheckApproved:         "success",
	smeldr.ChangeCheckNotApproved:      "failure",
}

// modePhrase is the "How changes are handled" column for an Area setting.
var modePhrase = map[string]string{
	smeldr.ApprovalModeCommentOnly:      "comment only",
	smeldr.ApprovalModeRequiresApproval: "needs approval",
	smeldr.ApprovalModeAuditAfterMerge:  "approval after merge",
}

// areaStatusPhrase is the "Status" column for an Area's verdict.
var areaStatusPhrase = map[string]string{
	smeldr.AreaVerdictAwaiting:      "waiting for approval",
	smeldr.AreaVerdictApproved:      "approved",
	smeldr.AreaVerdictNotApproved:   "not approved",
	smeldr.AreaVerdictCommented:     "no approval needed",
	smeldr.AreaVerdictAwaitingAudit: "approval after merge",
}

// The second line, one sentence per case.
const (
	lineChecking       = "Smeldr is checking this change."
	lineCommentOnly    = "This change touches only areas that do not need approval. Nothing blocks the merge."
	lineAuditOnly      = "This change touches an area where approval comes after the merge. Nothing blocks the merge."
	lineWaitingOne     = "This change touches 1 area that needs approval before it can be merged."
	lineWaitingMany    = "This change touches %d areas that need approval before it can be merged. Each area answers on its own."
	lineApproved       = "Every area that needs approval has approved this commit."
	lineNotApproved    = "%s did not approve this commit. The reason is on the instance."
	linePartly         = "Smeldr checked the first %s of %s files."
	lineNothingCovered = "The latest commit touches nothing that Smeldr covers, so there is nothing to approve."
	statusNothing      = "nothing covered"
)

// maxDecisions is how many Decisions the comment lists before "and n more".
const maxDecisions = 10

// view is how one repository's comment is written: whether names and titles
// show (D152) and where addresses link.
type view struct {
	names    bool
	checkURL string // a format with one %s, the Check address
	itemURL  string // a format with one %s, any other address
}

// view returns the view for repo.
func (a *Adapter) view(repo string) view {
	return view{names: a.namesShown(repo), checkURL: a.cfg.CheckURL, itemURL: a.cfg.ItemURL}
}

// phrase returns the table's phrase for key, or key itself for a state the
// table does not know (a newer core).
func phrase(table map[string]string, key string) string {
	if p, ok := table[key]; ok {
		return p
	}
	return key
}

// statusOf maps a Check to a commit status. The description holds the Check
// address and a state phrase, never a name or title (D152 point 4).
func statusOf(res smeldr.ChangeCheckResult) (state, description string) {
	if !res.Covered || res.Check == nil {
		return "success", statusNothing
	}
	c := res.Check
	state, ok := statusState[string(c.Status)]
	if !ok {
		state = "success"
	}
	return state, c.Address + ": " + phrase(verdictPhrase, string(c.Status))
}

// link writes address as a link to format, or plain when format is empty.
func link(format, address string) string {
	if format == "" {
		return address
	}
	return "[" + address + "](" + fmt.Sprintf(format, address) + ")"
}

// thousands writes n with comma separators, as the wording's "3,000".
func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// comment is the host's copy of the Check, never stored by core.
func (v view) comment(res smeldr.ChangeCheckResult) string {
	var b strings.Builder
	b.WriteString(commentMarker + "\n")
	if !res.Covered || res.Check == nil {
		b.WriteString(lineNothingCovered + "\n")
		return b.String()
	}
	c := res.Check
	fmt.Fprintf(&b, "**Smeldr check %s: %s**\n\n", c.Address, phrase(verdictPhrase, string(c.Status)))
	if line := secondLine(res); line != "" {
		b.WriteString(line + "\n")
	}
	if c.CheckedPartly {
		fmt.Fprintf(&b, linePartly+"\n", thousands(c.FilesChecked), thousands(c.FilesTotal))
	}
	if len(res.Areas) > 0 {
		b.WriteString("\n| Area | How changes are handled | Status |\n|---|---|---|\n")
		for _, area := range res.Areas {
			label := link(v.itemURL, area.Address)
			if v.names && area.Name != "" {
				label += " " + area.Name
			}
			fmt.Fprintf(&b, "| %s | %s | %s |\n", label, phrase(modePhrase, area.ApprovalMode), phrase(areaStatusPhrase, area.Verdict))
		}
	}
	if len(res.Decisions) > 0 {
		var items []string
		for i, d := range res.Decisions {
			if i == maxDecisions {
				more := fmt.Sprintf("and %d more", len(res.Decisions)-maxDecisions)
				if v.checkURL != "" {
					more = "[" + more + "](" + fmt.Sprintf(v.checkURL, c.Address) + ")"
				}
				items = append(items, more)
				break
			}
			item := link(v.itemURL, d.Address)
			if v.names && d.Title != "" {
				item += " " + d.Title
			}
			items = append(items, item)
		}
		b.WriteString("\nDecisions that apply: " + strings.Join(items, ", ") + "\n")
	}
	if v.checkURL != "" {
		fmt.Fprintf(&b, "\nOpen %s in Smeldr: %s\n", c.Address, fmt.Sprintf(v.checkURL, c.Address))
	}
	return b.String()
}

// secondLine is the one sentence under the heading, from the Check's state
// and its Areas.
func secondLine(res smeldr.ChangeCheckResult) string {
	switch string(res.Check.Status) {
	case smeldr.ChangeCheckChecking:
		return lineChecking
	case smeldr.ChangeCheckCommented:
		for _, area := range res.Areas {
			if area.ApprovalMode == smeldr.ApprovalModeAuditAfterMerge {
				return lineAuditOnly
			}
		}
		return lineCommentOnly
	case smeldr.ChangeCheckAwaitingApproval:
		n := 0
		for _, area := range res.Areas {
			if area.ApprovalMode == smeldr.ApprovalModeRequiresApproval {
				n++
			}
		}
		if n == 1 {
			return lineWaitingOne
		}
		return fmt.Sprintf(lineWaitingMany, n)
	case smeldr.ChangeCheckApproved:
		return lineApproved
	case smeldr.ChangeCheckNotApproved:
		var refused []string
		for _, area := range res.Areas {
			if area.Verdict == smeldr.AreaVerdictNotApproved {
				refused = append(refused, area.Address)
			}
		}
		return fmt.Sprintf(lineNotApproved, strings.Join(refused, " and "))
	}
	return ""
}
