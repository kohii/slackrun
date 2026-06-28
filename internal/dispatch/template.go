// Package dispatch turns a Slack event into a matched rule and the rendered
// command argv that will be executed.
package dispatch

import "regexp"

// TemplateVars captures everything a rule template can reference. Fields are
// the names recognized inside `{{...}}`.
type TemplateVars struct {
	Permalink string
	Text      string
	Rest      string
	Channel   string
	User      string
}

var templateVarRe = regexp.MustCompile(`\{\{\s*(permalink|text|rest|channel|user)\s*\}\}`)

// ExpandTemplate replaces `{{var}}` placeholders. Unknown names are left
// untouched (they will surface in dry-run output and logs, which is the cue
// to fix the rule).
func ExpandTemplate(template string, vars TemplateVars) string {
	return templateVarRe.ReplaceAllStringFunc(template, func(match string) string {
		groups := templateVarRe.FindStringSubmatch(match)
		if len(groups) < 2 {
			return match
		}
		switch groups[1] {
		case "permalink":
			return vars.Permalink
		case "text":
			return vars.Text
		case "rest":
			return vars.Rest
		case "channel":
			return vars.Channel
		case "user":
			return vars.User
		}
		return match
	})
}

// TemplateVarsUsed lists the placeholders referenced by a template. Used by
// dry-run output and to decide whether to resolve `chat.getPermalink` (a
// network call) at job start.
func TemplateVarsUsed(template string) []string {
	matches := templateVarRe.FindAllStringSubmatch(template, -1)
	seen := map[string]bool{}
	var out []string
	for _, m := range matches {
		if len(m) >= 2 && !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// TemplateUsesPermalink is a tiny convenience for the hot path so we do not
// allocate per-event for the common "does this rule need permalink?" check.
func TemplateUsesPermalink(template string) bool {
	for _, v := range TemplateVarsUsed(template) {
		if v == "permalink" {
			return true
		}
	}
	return false
}
