package config

import (
	"strings"
	"testing"
)

func parseAndValidate(t *testing.T, yamlText string) ValidationResult {
	t.Helper()
	parsed, err := ParseRulesYAML([]byte(yamlText), "<test>")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return ValidationResult{
		Rules:  parsed.Rules,
		Issues: ValidateRules(parsed.Rules, CheckOptions{SkipFsChecks: true}),
	}
}

const validSample = `
rules:
  - name: sentry
    trigger:
      type: message
      channel: C01ALERT123
      from:
        bot_user_ids: [U01SENTRY1]
        app_ids: [A01SENTRY1]
        usernames: ["Sentry"]
    action:
      cwd: /tmp
      command: ["echo", "{{permalink}}"]
      timeout_ms: 60000

  - name: mention-default
    trigger:
      type: app_mention
    action:
      cwd: /tmp
      command: ["echo", "{{text}}"]
      timeout_ms: 60000
`

func TestParseRulesYAML_Valid(t *testing.T) {
	t.Parallel()
	res := parseAndValidate(t, validSample)
	if res.HasErrors() {
		t.Fatalf("unexpected errors: %+v", res.Issues)
	}
	if len(res.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(res.Rules))
	}
}

func TestParseRulesYAML_RejectsUnknownField(t *testing.T) {
	t.Parallel()
	bad := `
rules:
  - name: x
    foo: bar
    trigger: { type: app_mention }
    action: { cwd: /tmp, command: [echo], timeout_ms: 1000 }
`
	if _, err := ParseRulesYAML([]byte(bad), "<test>"); err == nil {
		t.Fatal("expected error on unknown field, got nil")
	}
}

func TestTrigger_RejectsKeywordOnMessage(t *testing.T) {
	t.Parallel()
	bad := `
rules:
  - name: x
    trigger:
      type: message
      channel: C01X1234
      keyword: nope
      from: { usernames: ["x"] }
    action: { cwd: /tmp, command: [echo], timeout_ms: 1000 }
`
	if _, err := ParseRulesYAML([]byte(bad), "<test>"); err == nil {
		t.Fatal("expected error")
	}
}

func TestTrigger_RejectsChannelOnMention(t *testing.T) {
	t.Parallel()
	bad := `
rules:
  - name: x
    trigger: { type: app_mention, channel: C01X1234 }
    action: { cwd: /tmp, command: [echo], timeout_ms: 1000 }
`
	if _, err := ParseRulesYAML([]byte(bad), "<test>"); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidate_DuplicateNames(t *testing.T) {
	t.Parallel()
	src := `
rules:
  - name: dup
    trigger: { type: app_mention, keyword: a }
    action: { cwd: /tmp, command: [echo], timeout_ms: 1000 }
  - name: dup
    trigger: { type: app_mention, keyword: b }
    action: { cwd: /tmp, command: [echo], timeout_ms: 1000 }
`
	res := parseAndValidate(t, src)
	if !res.HasErrors() {
		t.Fatal("expected duplicate-name error")
	}
	if !hasIssue(res.Issues, "duplicate rule name") {
		t.Fatalf("missing duplicate-name issue: %+v", res.Issues)
	}
}

func TestValidate_DuplicateKeyword(t *testing.T) {
	t.Parallel()
	src := `
rules:
  - name: a
    trigger: { type: app_mention, keyword: Henry }
    action: { cwd: /tmp, command: [echo], timeout_ms: 1000 }
  - name: b
    trigger: { type: app_mention, keyword: henry }
    action: { cwd: /tmp, command: [echo], timeout_ms: 1000 }
`
	res := parseAndValidate(t, src)
	if !hasIssue(res.Issues, "duplicate keyword") {
		t.Fatalf("missing duplicate-keyword issue: %+v", res.Issues)
	}
}

func TestValidate_MultipleDefaults(t *testing.T) {
	t.Parallel()
	src := `
rules:
  - name: a
    trigger: { type: app_mention }
    action: { cwd: /tmp, command: [echo], timeout_ms: 1000 }
  - name: b
    trigger: { type: app_mention }
    action: { cwd: /tmp, command: [echo], timeout_ms: 1000 }
`
	res := parseAndValidate(t, src)
	if !hasIssue(res.Issues, "multiple default") {
		t.Fatalf("missing multi-default issue: %+v", res.Issues)
	}
}

func TestValidate_ChannelOverlapWarn(t *testing.T) {
	t.Parallel()
	src := `
rules:
  - name: a
    trigger:
      type: message
      channel: C01X1234
      from: { usernames: ["x"] }
    action: { cwd: /tmp, command: [echo], timeout_ms: 1000 }
  - name: b
    trigger:
      type: message
      channel: C01X1234
      from: { usernames: ["y"] }
    action: { cwd: /tmp, command: [echo], timeout_ms: 1000 }
`
	res := parseAndValidate(t, src)
	if res.HasErrors() {
		t.Fatalf("unexpected errors: %+v", res.Issues)
	}
	var sawWarn bool
	for _, i := range res.Issues {
		if i.Level == IssueWarn && strings.Contains(i.Message, "multiple message rules on channel") {
			sawWarn = true
		}
	}
	if !sawWarn {
		t.Fatalf("missing channel-overlap warn: %+v", res.Issues)
	}
}

func TestValidate_BadIDs(t *testing.T) {
	t.Parallel()
	src := `
rules:
  - name: x
    trigger:
      type: message
      channel: nope
      from: { bot_user_ids: ["B01invalid"] }
    action: { cwd: relative/path, command: [], timeout_ms: 0 }
`
	res := parseAndValidate(t, src)
	if !hasIssue(res.Issues, "must be a Slack channel ID") {
		t.Fatalf("missing channel-id issue: %+v", res.Issues)
	}
	if !hasIssue(res.Issues, "must look like UXXXXXXXX") {
		t.Fatalf("missing bot-user-id issue: %+v", res.Issues)
	}
	if !hasIssue(res.Issues, "must be an absolute path") {
		t.Fatalf("missing absolute-path issue: %+v", res.Issues)
	}
	if !hasIssue(res.Issues, "command must be a non-empty") {
		t.Fatalf("missing empty-command issue: %+v", res.Issues)
	}
	if !hasIssue(res.Issues, "timeout_ms must be > 0") {
		t.Fatalf("missing timeout issue: %+v", res.Issues)
	}
}

func TestValidate_FromMustNotBeEmpty(t *testing.T) {
	t.Parallel()
	src := `
rules:
  - name: x
    trigger:
      type: message
      channel: C01X1234
      from: {}
    action: { cwd: /tmp, command: [echo], timeout_ms: 1000 }
`
	res := parseAndValidate(t, src)
	if !hasIssue(res.Issues, "trigger.from must list at least one") {
		t.Fatalf("missing empty-from issue: %+v", res.Issues)
	}
}

func hasIssue(issues []ValidationIssue, needle string) bool {
	for _, i := range issues {
		if strings.Contains(i.Message, needle) {
			return true
		}
	}
	return false
}
