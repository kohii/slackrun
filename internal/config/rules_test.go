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
      command: ["echo", "alert"]
      timeout_ms: 60000
      stdin:
        parts:
          - template: "permalink: {{permalink}}"

  - name: mention-default
    trigger:
      type: app_mention
    action:
      cwd: /tmp
      command: ["claude", "-p"]
      timeout_ms: 60000
      stdin:
        parts:
          - template: "{{text}}"
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

func TestValidate_RejectsProtectedActionEnv(t *testing.T) {
	t.Parallel()
	cases := []string{
		"SLACK_BOT_TOKEN", "SLACK_APP_TOKEN", "ALLOWED_USER_IDS",
		"SLACKRUN_CHANNEL", "SLACKRUN_TS", "SLACKRUN_THREAD_TS", "SLACKRUN_USER",
	}
	for _, key := range cases {
		key := key
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			src := `
rules:
  - name: r
    trigger: { type: app_mention, keyword: r }
    action:
      cwd: /tmp
      command: [echo]
      timeout_ms: 1000
      env:
        ` + key + `: "x"
`
			res := parseAndValidate(t, src)
			if !hasIssue(res.Issues, "is reserved") {
				t.Fatalf("missing reserved-env error for %s: %+v", key, res.Issues)
			}
		})
	}
}

func TestValidate_AllowsExposeSlackToken(t *testing.T) {
	t.Parallel()
	src := `
rules:
  - name: r
    trigger: { type: app_mention, keyword: r }
    action:
      cwd: /tmp
      command: [echo]
      timeout_ms: 1000
      expose_slack_token: true
`
	res := parseAndValidate(t, src)
	if res.HasErrors() {
		t.Fatalf("unexpected errors: %+v", res.Issues)
	}
	if !res.Rules[0].Action.ExposeSlackToken {
		t.Fatal("expose_slack_token should be true")
	}
}

func TestValidate_RejectsTemplateInCommandArgv(t *testing.T) {
	t.Parallel()
	src := `
rules:
  - name: r
    trigger: { type: app_mention, keyword: r }
    action:
      cwd: /tmp
      command: ["claude", "-p", "{{text}}"]
      timeout_ms: 1000
`
	res := parseAndValidate(t, src)
	if !hasIssue(res.Issues, "argv expansion is forbidden") {
		t.Fatalf("expected argv template rejection: %+v", res.Issues)
	}
}

func TestValidate_StdinPartsAllVariants(t *testing.T) {
	t.Parallel()
	src := `
rules:
  - name: r
    trigger: { type: app_mention, keyword: r }
    action:
      cwd: /tmp
      command: [claude, -p]
      timeout_ms: 1000
      stdin:
        parts:
          - text: "you are an assistant"
          - slack_thread:
              max_messages: 50
              max_bytes: 65536
              format: text
              on_fetch_error: fail
          - template: "user: {{user}}"
`
	res := parseAndValidate(t, src)
	if res.HasErrors() {
		t.Fatalf("unexpected errors: %+v", res.Issues)
	}
	parts := res.Rules[0].Action.Stdin.Parts
	if len(parts) != 3 {
		t.Fatalf("got %d parts, want 3", len(parts))
	}
	if parts[0].Kind != PartKindText {
		t.Errorf("parts[0] kind = %v", parts[0].Kind)
	}
	if parts[1].Kind != PartKindSlackThread {
		t.Errorf("parts[1] kind = %v", parts[1].Kind)
	}
	if parts[2].Kind != PartKindTemplate {
		t.Errorf("parts[2] kind = %v", parts[2].Kind)
	}
}

func TestValidate_StdinPartRejectsMultipleVariants(t *testing.T) {
	t.Parallel()
	src := `
rules:
  - name: r
    trigger: { type: app_mention, keyword: r }
    action:
      cwd: /tmp
      command: [echo]
      timeout_ms: 1000
      stdin:
        parts:
          - text: "x"
            template: "y"
`
	if _, err := ParseRulesYAML([]byte(src), "<test>"); err == nil {
		t.Fatal("expected error on multi-variant part")
	}
}

func TestValidate_StdinPartRejectsEmpty(t *testing.T) {
	t.Parallel()
	src := `
rules:
  - name: r
    trigger: { type: app_mention, keyword: r }
    action:
      cwd: /tmp
      command: [echo]
      timeout_ms: 1000
      stdin:
        parts:
          - {}
`
	if _, err := ParseRulesYAML([]byte(src), "<test>"); err == nil {
		t.Fatal("expected error on empty part")
	}
}

func TestValidate_TemplatePartRejectsUnknownVar(t *testing.T) {
	t.Parallel()
	src := `
rules:
  - name: r
    trigger: { type: app_mention, keyword: r }
    action:
      cwd: /tmp
      command: [echo]
      timeout_ms: 1000
      stdin:
        parts:
          - template: "hello {{bogus}}"
`
	res := parseAndValidate(t, src)
	if !hasIssue(res.Issues, "unknown variable {{bogus}}") {
		t.Fatalf("expected unknown-var error: %+v", res.Issues)
	}
}

func TestValidate_SlackThreadEnums(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		body   string
		needle string
	}{
		{"format", `format: yaml`, `format must be "text" or "jsonl"`},
		{"on_fetch_error", `on_fetch_error: retry`, `on_fetch_error must be "fail" or "fallback_event"`},
		{"max_messages", `max_messages: -1`, "max_messages must be >= 0"},
		{"max_bytes", `max_bytes: -1`, "max_bytes must be >= 0"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			src := `
rules:
  - name: r
    trigger: { type: app_mention, keyword: r }
    action:
      cwd: /tmp
      command: [echo]
      timeout_ms: 1000
      stdin:
        parts:
          - slack_thread:
              ` + c.body + `
`
			res := parseAndValidate(t, src)
			if !hasIssue(res.Issues, c.needle) {
				t.Fatalf("missing %q: %+v", c.needle, res.Issues)
			}
		})
	}
}

func TestValidate_ExcludeTriggeringMessage_WithFallbackEventWarns(t *testing.T) {
	t.Parallel()
	src := `
rules:
  - name: r
    trigger: { type: app_mention, keyword: r }
    action:
      cwd: /tmp
      command: [echo]
      timeout_ms: 1000
      stdin:
        parts:
          - slack_thread:
              exclude_triggering_message: true
              on_fetch_error: fallback_event
`
	res := parseAndValidate(t, src)
	if res.HasErrors() {
		t.Fatalf("expected warn, not error: %+v", res.Issues)
	}
	var sawWarn bool
	for _, i := range res.Issues {
		if i.Level == IssueWarn && strings.Contains(i.Message, "empty fallback") {
			sawWarn = true
		}
	}
	if !sawWarn {
		t.Fatalf("expected empty-fallback warn: %+v", res.Issues)
	}
}

func TestValidate_StdinPartsEmptyArrayRejected(t *testing.T) {
	t.Parallel()
	src := `
rules:
  - name: r
    trigger: { type: app_mention, keyword: r }
    action:
      cwd: /tmp
      command: [echo]
      timeout_ms: 1000
      stdin:
        parts: []
`
	res := parseAndValidate(t, src)
	if !hasIssue(res.Issues, "must contain at least one part") {
		t.Fatalf("expected empty-parts error: %+v", res.Issues)
	}
}
