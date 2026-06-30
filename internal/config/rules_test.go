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
        - text: "permalink: {{event.permalink}}"

  - name: mention-default
    trigger:
      type: app_mention
    action:
      cwd: /tmp
      command: ["claude", "-p"]
      timeout_ms: 60000
      stdin:
        - trigger_message: {}
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
      command: ["claude", "-p", "{{event.user_id}}"]
      timeout_ms: 1000
`
	res := parseAndValidate(t, src)
	if !hasIssue(res.Issues, "argv expansion is forbidden") {
		t.Fatalf("expected argv template rejection: %+v", res.Issues)
	}
}

func TestValidate_StdinAllPartKinds(t *testing.T) {
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
        - text: "you are an assistant"
        - trigger_message:
            heading: 最新の依頼
            content: command_text
        - thread:
            heading: 参考スレッド
            include_triggering_message: false
            max_messages: 50
            on_fetch_error: omit
`
	res := parseAndValidate(t, src)
	if res.HasErrors() {
		t.Fatalf("unexpected errors: %+v", res.Issues)
	}
	parts := res.Rules[0].Action.Stdin
	if len(parts) != 3 {
		t.Fatalf("got %d parts, want 3", len(parts))
	}
	if parts[0].Kind != PartKindText {
		t.Errorf("parts[0] kind = %v", parts[0].Kind)
	}
	if parts[1].Kind != PartKindTriggerMessage {
		t.Errorf("parts[1] kind = %v", parts[1].Kind)
	}
	if parts[1].TriggerMessage.Heading != "最新の依頼" {
		t.Errorf("heading = %q", parts[1].TriggerMessage.Heading)
	}
	if parts[1].TriggerMessage.Content != ContentCommandText {
		t.Errorf("content = %q", parts[1].TriggerMessage.Content)
	}
	if parts[2].Kind != PartKindThread {
		t.Errorf("parts[2] kind = %v", parts[2].Kind)
	}
	if parts[2].Thread.IncludeTriggeringMessage {
		t.Errorf("expected IncludeTriggeringMessage default false")
	}
	if parts[2].Thread.OnFetchError != OnFetchErrorOmit {
		t.Errorf("on_fetch_error = %q", parts[2].Thread.OnFetchError)
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
        - text: "x"
          trigger_message: {}
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
        - {}
`
	if _, err := ParseRulesYAML([]byte(src), "<test>"); err == nil {
		t.Fatal("expected error on empty part")
	}
}

func TestValidate_TextPartRejectsUnknownVar(t *testing.T) {
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
        - text: "hello {{bogus}}"
`
	res := parseAndValidate(t, src)
	if !hasIssue(res.Issues, "unknown variable {{bogus}}") {
		t.Fatalf("expected unknown-var error: %+v", res.Issues)
	}
}

func TestValidate_TextPartRejectsLegacyBodyVar(t *testing.T) {
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
        - text: "hello {{text}}"
`
	res := parseAndValidate(t, src)
	if !hasIssue(res.Issues, "trigger_message") {
		t.Fatalf("expected legacy-var hint pointing at trigger_message: %+v", res.Issues)
	}
}

func TestValidate_TextPartRejectsLegacyMetaVar(t *testing.T) {
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
        - text: "user: {{user}}"
`
	res := parseAndValidate(t, src)
	if !hasIssue(res.Issues, "{{event.user_id}}") {
		t.Fatalf("expected legacy hint pointing at event.user_id: %+v", res.Issues)
	}
}

func TestValidate_ThreadEnums(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		body   string
		needle string
	}{
		{"format", `format: yaml`, `format must be "text" or "jsonl"`},
		{"on_fetch_error", `on_fetch_error: retry`, `on_fetch_error must be "fail" or "omit"`},
		{"files", `files: bogus`, `files must be "none" or "link"`},
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
        - thread:
            ` + c.body + `
`
			res := parseAndValidate(t, src)
			if !hasIssue(res.Issues, c.needle) {
				t.Fatalf("missing %q: %+v", c.needle, res.Issues)
			}
		})
	}
}

func TestValidate_TriggerMessageEnums(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		body   string
		needle string
	}{
		{"content", `content: bogus`, `content must be "command_text", "body_text", or "raw_text"`},
		{"files", `files: bogus`, `files must be "none" or "link"`},
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
        - trigger_message:
            ` + c.body + `
`
			res := parseAndValidate(t, src)
			if !hasIssue(res.Issues, c.needle) {
				t.Fatalf("missing %q: %+v", c.needle, res.Issues)
			}
		})
	}
}

func TestValidate_RejectsMultipleTriggerMessage(t *testing.T) {
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
        - trigger_message: {}
        - trigger_message: {}
`
	res := parseAndValidate(t, src)
	if !hasIssue(res.Issues, "trigger_message parts; at most one") {
		t.Fatalf("expected multi-trigger_message rejection: %+v", res.Issues)
	}
}

func TestValidate_RejectsMultipleThread(t *testing.T) {
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
        - thread: {}
        - thread: {}
`
	res := parseAndValidate(t, src)
	if !hasIssue(res.Issues, "thread parts; at most one") {
		t.Fatalf("expected multi-thread rejection: %+v", res.Issues)
	}
}

func TestValidate_RejectsTemplateInHeading(t *testing.T) {
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
        - trigger_message:
            heading: "from {{event.user_id}}"
`
	res := parseAndValidate(t, src)
	if !hasIssue(res.Issues, "heading must not contain") {
		t.Fatalf("expected heading-template rejection: %+v", res.Issues)
	}
}

func TestValidate_SlackrunHelpPartParses(t *testing.T) {
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
      stdin:
        - slackrun_help: {}
`
	res := parseAndValidate(t, src)
	if res.HasErrors() {
		t.Fatalf("unexpected errors: %+v", res.Issues)
	}
	parts := res.Rules[0].Action.Stdin
	if len(parts) != 1 || parts[0].Kind != PartKindSlackrunHelp {
		t.Fatalf("expected one slackrun_help part: %+v", parts)
	}
}

func TestValidate_SlackrunHelpWithoutTokenWarns(t *testing.T) {
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
        - slackrun_help: {}
`
	res := parseAndValidate(t, src)
	if res.HasErrors() {
		t.Fatalf("expected warn-only, got errors: %+v", res.Issues)
	}
	var sawWarn bool
	for _, i := range res.Issues {
		if i.Level == IssueWarn && strings.Contains(i.Message, "slackrun_help") {
			sawWarn = true
		}
	}
	if !sawWarn {
		t.Fatalf("expected slackrun_help warn: %+v", res.Issues)
	}
}

func TestValidate_ReplyWithStdoutBool(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"true", "false"} {
		value := value
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			src := `
rules:
  - name: r
    trigger: { type: app_mention, keyword: r }
    action:
      cwd: /tmp
      command: [echo]
      timeout_ms: 1000
      reply_with_stdout: ` + value + `
`
			res := parseAndValidate(t, src)
			if res.HasErrors() {
				t.Fatalf("unexpected errors: %+v", res.Issues)
			}
			got := res.Rules[0].Action.ReplyWithStdout
			if got == nil {
				t.Fatal("expected pointer to be set when YAML provides a value")
			}
			want := value == "true"
			if *got != want {
				t.Errorf("got %v, want %v", *got, want)
			}
		})
	}
}

func TestValidate_ReplyWithStdoutDefaultsTrue(t *testing.T) {
	t.Parallel()
	src := `
rules:
  - name: r
    trigger: { type: app_mention, keyword: r }
    action:
      cwd: /tmp
      command: [echo]
      timeout_ms: 1000
`
	res := parseAndValidate(t, src)
	if res.HasErrors() {
		t.Fatalf("unexpected errors: %+v", res.Issues)
	}
	if got := res.Rules[0].Action.ReplyWithStdout; got != nil {
		t.Errorf("expected nil pointer when field omitted, got *%v", *got)
	}
	if !res.Rules[0].Action.ReplyWithStdoutEnabled() {
		t.Error("ReplyWithStdoutEnabled should default to true")
	}
}

func TestValidate_StdinEmptyArrayRejected(t *testing.T) {
	t.Parallel()
	src := `
rules:
  - name: r
    trigger: { type: app_mention, keyword: r }
    action:
      cwd: /tmp
      command: [echo]
      timeout_ms: 1000
      stdin: []
`
	res := parseAndValidate(t, src)
	if !hasIssue(res.Issues, "must contain at least one part") {
		t.Fatalf("expected empty-stdin error: %+v", res.Issues)
	}
}
