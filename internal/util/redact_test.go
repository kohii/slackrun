package util

import (
	"strings"
	"testing"
)

func TestRedactPII(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		in          string
		mustNotHave string
		mustHave    string
	}{
		{"email", "contact alice@example.com today", "alice@example.com", "[REDACTED_EMAIL]"},
		{"slack-bot-token", "set token to xoxb-1234567890-AbCdEfGhIjKlMnO now", "xoxb-1234567890-AbCdEfGhIjKlMnO", "[REDACTED_SLACK_TOKEN]"},
		{"slack-app-token", "use xapp-1-A0000-1234567890123-abcdefghijklmn", "xapp-1-A0000-1234567890123-abcdefghijklmn", "[REDACTED_SLACK_APP_TOKEN]"},
		{"jwt", "auth: eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1c2VyIn0.signaturepart", "eyJhbGciOiJIUzI1NiJ9", "[REDACTED_JWT]"},
		{"bearer", "Authorization: Bearer abcdefghijklmnopqrstuv yes", "abcdefghijklmnopqrstuv", "Bearer [REDACTED_TOKEN]"},
		{"aws", "key AKIAIOSFODNN7EXAMPLE rotated", "AKIAIOSFODNN7EXAMPLE", "[REDACTED_AWS_KEY]"},
		{"github", "token ghp_abcdefghijklmnopqrstuv12345 active", "ghp_abcdefghijklmnopqrstuv12345", "[REDACTED_GH_TOKEN]"},
		{"github-pat", "token github_pat_11ABCDEFGH0_abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJabc here", "github_pat_11ABCDEFGH0_abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJabc", "[REDACTED_GH_PAT]"},
		{"anthropic", "key sk-ant-api03-AAAAAAAAAAAAAAAAAAAAA active", "sk-ant-api03-AAAAAAAAAAAAAAAAAAAAA", "[REDACTED_ANTHROPIC_KEY]"},
		{"openai", "key sk-proj-AAAAAAAAAAAAAAAAAAAAAAAA used", "sk-proj-AAAAAAAAAAAAAAAAAAAAAAAA", "[REDACTED_OPENAI_KEY]"},
		{"basic-auth", "Authorization: Basic dXNlcjpwYXNzd29yZA==", "dXNlcjpwYXNzd29yZA==", "Basic [REDACTED_TOKEN]"},
		{"url-userinfo", "use https://alice:secret@example.com/path now", "alice:secret", "[REDACTED_USERINFO]"},
		{"query-token", "https://x.test/cb?access_token=abc123&foo=bar", "access_token=abc123", "[REDACTED]"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := RedactPII(c.in)
			if strings.Contains(got, c.mustNotHave) {
				t.Fatalf("expected %q to be masked but got: %q", c.mustNotHave, got)
			}
			if !strings.Contains(got, c.mustHave) {
				t.Fatalf("expected %q in output, got: %q", c.mustHave, got)
			}
		})
	}
}

func TestRedactPIIDetailed_ReturnsKinds(t *testing.T) {
	t.Parallel()
	_, kinds := RedactPIIDetailed("a@b.com and xoxb-1-abcdefghijkl token")
	want := map[string]bool{"email": true, "slack-token": true}
	for _, k := range kinds {
		delete(want, k)
	}
	if len(want) > 0 {
		t.Fatalf("missing kinds: %v (got %v)", want, kinds)
	}
}

func TestRedactPII_PreservesSlackIDs(t *testing.T) {
	// Slack IDs (UXXXXXX) and timestamps (123456.7890) must NOT be touched.
	t.Parallel()
	for _, fragment := range []string{"U01ABCD2EFG", "C01XYZ7HIJK", "B0123ABC", "1719456789.012345"} {
		out := RedactPII("event " + fragment + " done")
		if !strings.Contains(out, fragment) {
			t.Fatalf("Slack ID %q was incorrectly masked: %q", fragment, out)
		}
	}
}

func TestRunRedactSelfCheck(t *testing.T) {
	t.Parallel()
	res := RunRedactSelfCheck()
	if !res.OK {
		t.Fatalf("self-check failed: %v", res.Failures)
	}
}
