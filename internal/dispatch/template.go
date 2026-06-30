// Package dispatch turns a Slack event into a matched rule and the rendered
// stdin payload that will be piped to the spawned command.
package dispatch

import "regexp"

// TemplateVars captures the metadata variables a `text:` stdin part can
// reference. These are all opaque identifiers or URLs derived from the
// event envelope — never Slack-authored body text. Body content reaches
// the child only through `trigger_message` / `thread` parts, both of which
// are XML-wrapped as untrusted by the renderer.
type TemplateVars struct {
	Permalink string
	ChannelID string
	UserID    string
	TS        string
	ThreadTS  string
}

var templateVarRe = regexp.MustCompile(`\{\{\s*(event\.permalink|event\.channel_id|event\.user_id|event\.ts|event\.thread_ts)\s*\}\}`)

// ExpandTemplate replaces `{{event.*}}` placeholders. Unknown names are
// left untouched; the config loader rejects them at load time so reaching
// ExpandTemplate with one means the loader was bypassed (e.g. tests).
func ExpandTemplate(template string, vars TemplateVars) string {
	return templateVarRe.ReplaceAllStringFunc(template, func(match string) string {
		groups := templateVarRe.FindStringSubmatch(match)
		if len(groups) < 2 {
			return match
		}
		switch groups[1] {
		case "event.permalink":
			return vars.Permalink
		case "event.channel_id":
			return vars.ChannelID
		case "event.user_id":
			return vars.UserID
		case "event.ts":
			return vars.TS
		case "event.thread_ts":
			return vars.ThreadTS
		}
		return match
	})
}

// TemplateVarsUsed lists the variable names referenced by a template
// string. Used by dry-run output and to decide whether the permalink fetch
// (a Slack API call) is needed.
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
		if v == "event.permalink" {
			return true
		}
	}
	return false
}
