package util

import (
	"regexp"
	"strings"
)

// systemTagPatterns are control / meta markers that Claude or other CLIs may
// emit. We strip them before posting back to Slack so users see plain prose.
// Conservative — over-stripping risks losing real content.
var systemTagPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?is)<system-reminder>.*?</system-reminder>`),
	regexp.MustCompile(`(?is)<command-name>.*?</command-name>`),
	regexp.MustCompile(`(?is)<command-message>.*?</command-message>`),
	regexp.MustCompile(`(?is)<command-args>.*?</command-args>`),
	regexp.MustCompile(`(?is)<local-command-stdout>.*?</local-command-stdout>`),
	regexp.MustCompile(`(?is)<local-command-caveat>.*?</local-command-caveat>`),
	regexp.MustCompile(`(?m)^\[claude-code\][^\n]*\n?`),
}

// ANSI escape sequence matcher (CSI + final byte). Source: the same shape
// strip-ansi uses on npm.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b[PX^_].*?\x1b\\|\x1b\][^\x07]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]`)

// StripANSI removes ANSI escape sequences.
func StripANSI(input string) string {
	return ansiRe.ReplaceAllString(input, "")
}

// StripSystemTags removes Claude/system marker spans.
func StripSystemTags(input string) string {
	out := input
	for _, re := range systemTagPatterns {
		out = re.ReplaceAllString(out, "")
	}
	return out
}

// SanitizeForSlack is the full outbound pipeline: ANSI → system tags → PII
// redaction → trim. Used for every CLI stdout before it lands on Slack.
func SanitizeForSlack(input string) string {
	return strings.TrimSpace(RedactPII(StripSystemTags(StripANSI(input))))
}
