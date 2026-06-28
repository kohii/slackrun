// Package util holds small stateless helpers shared across the codebase:
// PII redaction, output sanitization, chunking, time formatting.
package util

import (
	"regexp"
	"strings"
)

const redactedMask = "[REDACTED]"

type redactPattern struct {
	name        string
	re          *regexp.Regexp
	replacement string
}

// Patterns ordered so the most specific token formats run first. Phone numbers
// are deliberately omitted: the obvious pattern collides with Slack IDs
// (UXXXXXXXX) and timestamps (1719456789.012345) and produces hard-to-debug
// logs.
var defaultRedactPatterns = []redactPattern{
	{
		name: "slack-token",
		// xox[a-z]- prefixes (xoxb, xoxp, xoxe, xoxa, xoxr, ...). Permissive so
		// future prefixes do not slip through.
		re:          regexp.MustCompile(`\bxox[a-z]-[A-Za-z0-9-]{10,}\b`),
		replacement: "[REDACTED_SLACK_TOKEN]",
	},
	{
		name:        "xapp-token",
		re:          regexp.MustCompile(`\bxapp-[A-Za-z0-9-]{10,}\b`),
		replacement: "[REDACTED_SLACK_APP_TOKEN]",
	},
	{
		name:        "jwt",
		re:          regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`),
		replacement: "[REDACTED_JWT]",
	},
	{
		name:        "bearer",
		re:          regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{16,}\b`),
		replacement: "Bearer [REDACTED_TOKEN]",
	},
	{
		name:        "aws-access-key",
		re:          regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`),
		replacement: "[REDACTED_AWS_KEY]",
	},
	{
		name:        "github-token",
		re:          regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`),
		replacement: "[REDACTED_GH_TOKEN]",
	},
	{
		// Fine-grained PATs are much longer than classic ghX_ tokens and use
		// underscore as an internal separator.
		name:        "github-fine-grained-pat",
		re:          regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{50,}\b`),
		replacement: "[REDACTED_GH_PAT]",
	},
	{
		name:        "anthropic-key",
		re:          regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}\b`),
		replacement: "[REDACTED_ANTHROPIC_KEY]",
	},
	{
		// Catches both sk-... and sk-proj-... OpenAI-style secrets.
		name:        "openai-key",
		re:          regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_-]{20,}\b`),
		replacement: "[REDACTED_OPENAI_KEY]",
	},
	{
		// Authorization: <scheme> <token> — covers Basic / Token / OAuth /
		// the various capitalizations.
		name:        "auth-scheme",
		re:          regexp.MustCompile(`(?i)\b(Basic|Token|OAuth)\s+[A-Za-z0-9._~+/=-]{10,}\b`),
		replacement: "${1} [REDACTED_TOKEN]",
	},
	{
		// URL userinfo: https://user:pass@host/...
		name:        "url-userinfo",
		re:          regexp.MustCompile(`\b([a-z][a-z0-9+.-]*://)[^/@\s:]+:[^/@\s]+@`),
		replacement: "${1}[REDACTED_USERINFO]@",
	},
	{
		name:        "email",
		re:          regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`),
		replacement: "[REDACTED_EMAIL]",
	},
}

// queryTokenParam masks values of obvious secret-shaped query string
// parameters (?access_token=..., &api_key=...).
var queryTokenParam = regexp.MustCompile(`(?i)([?&](?:access_token|token|api[_-]?key|key|secret|signature)=)[^&\s]+`)

// RedactPII returns input with secret-shaped substrings masked. Suitable for
// any outbound string (Slack post, log line, file upload).
func RedactPII(input string) string {
	out, _ := redactPIIDetailed(input)
	return out
}

// RedactPIIDetailed reports which pattern names matched, useful for logging
// "what kinds were redacted" without echoing the original text.
func RedactPIIDetailed(input string) (string, []string) {
	return redactPIIDetailed(input)
}

func redactPIIDetailed(input string) (string, []string) {
	if input == "" {
		return input, nil
	}
	var kinds []string
	seen := map[string]bool{}
	out := input
	for _, p := range defaultRedactPatterns {
		if p.re.MatchString(out) {
			if !seen[p.name] {
				seen[p.name] = true
				kinds = append(kinds, p.name)
			}
			out = p.re.ReplaceAllString(out, p.replacement)
		}
	}
	if queryTokenParam.MatchString(out) {
		if !seen["query-token"] {
			seen["query-token"] = true
			kinds = append(kinds, "query-token")
		}
		out = queryTokenParam.ReplaceAllString(out, "${1}"+redactedMask)
	}
	return out, kinds
}

// RedactSelfCheckResult reports the outcome of the boot-time self check.
type RedactSelfCheckResult struct {
	OK       bool
	Failures []string
}

// RunRedactSelfCheck asserts each pattern still strips a representative
// fixture. Failures are returned to the caller, which typically logs and keeps
// running so missing masks do not block real traffic.
func RunRedactSelfCheck() RedactSelfCheckResult {
	cases := []struct {
		name           string
		input          string
		mustNotContain string
	}{
		{"email", "ping me at alice@example.com", "alice@example.com"},
		{"slack-token", "tok xoxb-1234567890-abcdefghijkl", "xoxb-1234567890-abcdefghijkl"},
		{
			"jwt",
			"Authorization: eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1c2VyIn0.signaturepart",
			"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1c2VyIn0.signaturepart",
		},
		{"aws-access-key", "AKIAIOSFODNN7EXAMPLE", "AKIAIOSFODNN7EXAMPLE"},
		{"bearer", "Authorization: Bearer abcdefghijklmnopqrstuv", "abcdefghijklmnopqrstuv"},
		{"anthropic", "key sk-ant-api03-AAAAAAAAAAAAAAAAAAAA", "sk-ant-api03-AAAAAAAAAAAAAAAAAAAA"},
		{"github-pat", "github_pat_11ABCDEFGH0_abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJ", "github_pat_11ABCDEFGH0_abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJ"},
		{"basic-auth", "Authorization: Basic dXNlcjpwYXNzd29yZA==", "dXNlcjpwYXNzd29yZA=="},
		{"url-userinfo", "https://alice:secret@example.com/x", "alice:secret"},
	}
	var failures []string
	for _, c := range cases {
		if strings.Contains(RedactPII(c.input), c.mustNotContain) {
			failures = append(failures, c.name)
		}
	}
	return RedactSelfCheckResult{OK: len(failures) == 0, Failures: failures}
}
