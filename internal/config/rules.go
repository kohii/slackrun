package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/kohii/slackrun/internal/runner"
	"gopkg.in/yaml.v3"
)

// TriggerType is one of the two recognised event sources slackrun routes on.
type TriggerType string

const (
	TriggerTypeMessage    TriggerType = "message"
	TriggerTypeAppMention TriggerType = "app_mention"
)

// TriggerFrom narrows `type: message` rules by sender. At least one of the
// three lists must be non-empty (validated in ValidateRules).
//
// `usernames` is the weakest signal — any incoming webhook can pick its own
// display name. Prefer the ID-based fields when the source supports them.
type TriggerFrom struct {
	BotUserIDs []string `yaml:"bot_user_ids,omitempty"`
	AppIDs     []string `yaml:"app_ids,omitempty"`
	Usernames  []string `yaml:"usernames,omitempty"`
}

// Trigger is a discriminated union over TriggerType. Unmarshaling validates
// that only the fields appropriate for the active variant are present, so a
// stray `keyword` on a message rule (or `channel` on a mention rule) fails
// loudly at load time.
type Trigger struct {
	Type TriggerType `yaml:"type"`

	// message variant
	Channel string       `yaml:"channel,omitempty"`
	From    *TriggerFrom `yaml:"from,omitempty"`

	// app_mention variant. nil means "default rule" (matches when no other
	// keyword rule matched); at most one such rule may exist per file.
	Keyword *string `yaml:"keyword,omitempty"`
}

// rawTrigger is the strict-decode shadow type — we parse into it first so we
// can reject unexpected fields, then fold the result into Trigger.
type rawTrigger struct {
	Type    string       `yaml:"type"`
	Channel string       `yaml:"channel,omitempty"`
	From    *TriggerFrom `yaml:"from,omitempty"`
	Keyword *string      `yaml:"keyword,omitempty"`
}

// UnmarshalYAML enforces the discriminated-union shape.
func (t *Trigger) UnmarshalYAML(node *yaml.Node) error {
	var raw rawTrigger
	if err := node.Decode(&raw); err != nil {
		return err
	}
	switch raw.Type {
	case string(TriggerTypeMessage):
		if raw.Keyword != nil {
			return errors.New("trigger.keyword is only valid for type: app_mention")
		}
		if raw.Channel == "" {
			return errors.New("trigger.channel is required for type: message")
		}
		if raw.From == nil {
			return errors.New("trigger.from is required for type: message")
		}
		t.Type = TriggerTypeMessage
		t.Channel = raw.Channel
		t.From = raw.From
	case string(TriggerTypeAppMention):
		if raw.Channel != "" || raw.From != nil {
			return errors.New("trigger.channel / trigger.from are only valid for type: message")
		}
		t.Type = TriggerTypeAppMention
		t.Keyword = raw.Keyword
	default:
		return fmt.Errorf("trigger.type must be \"message\" or \"app_mention\" (got %q)", raw.Type)
	}
	return nil
}

// Action is the side-effect of a matched rule. `Command` is an argv (no
// shell): `;` `$(...)` and backticks are inert, and `{{...}}` tokens are
// rejected at load time. Variable content reaches the child only through
// `Stdin` (a `text:` part), so it never lands on argv where it would be
// visible to `ps aux`.
type Action struct {
	Cwd       string            `yaml:"cwd"`
	Command   []string          `yaml:"command"`
	TimeoutMs int               `yaml:"timeout_ms"`
	Env       map[string]string `yaml:"env,omitempty"`
	Label     string            `yaml:"label,omitempty"`
	// ExposeSlackToken passes SLACK_BOT_TOKEN to the spawned child so it can
	// call `slackrun post|react|upload`. Default false; opt-in per rule so
	// `rules.yaml` is the single place to audit which children get token
	// access.
	ExposeSlackToken bool `yaml:"expose_slack_token,omitempty"`
	// Stdin is an ordered list of parts. slackrun renders each part and
	// concatenates the results into the byte stream piped to the child's
	// stdin. Absent / nil means the child reads nothing.
	Stdin []StdinPart `yaml:"stdin,omitempty"`
}

// StdinPartKind discriminates the variants of StdinPart.
type StdinPartKind int

const (
	PartKindUnknown StdinPartKind = iota
	// PartKindText is author-written instructions. Trusted. May contain
	// {{event.*}} metadata variable references.
	PartKindText
	// PartKindTriggerMessage renders the message that fired the rule, wrapped
	// in <UNTRUSTED_SLACK_MESSAGE_<nonce>> tags. At most one per rule.
	PartKindTriggerMessage
	// PartKindThread renders the thread the trigger lives in, wrapped in
	// <UNTRUSTED_SLACK_THREAD_<nonce>> tags. At most one per rule.
	PartKindThread
)

// ContentMode selects which slice of the triggering message body lands in a
// TriggerMessage part. Empty resolves to ContentCommandText.
type ContentMode string

const (
	// ContentCommandText strips both the bot mention and any matched keyword
	// from app_mention bodies. For message-type triggers, equivalent to
	// ContentRawText.
	ContentCommandText ContentMode = "command_text"
	// ContentBodyText strips the bot mention but keeps the keyword. For
	// message-type triggers, equivalent to ContentRawText.
	ContentBodyText ContentMode = "body_text"
	// ContentRawText is the Slack event `text` verbatim.
	ContentRawText ContentMode = "raw_text"
)

// FilesMode selects how file attachments are rendered inside a Slack-derived
// block. Empty resolves to FilesNone.
type FilesMode string

const (
	FilesNone FilesMode = "none"
	// FilesLink renders each attachment as
	// `[file: name.ext url=https://files.slack.com/…]` after the message body.
	// Slack file URLs are token-gated; the link is a reference, not a
	// guaranteed-fetchable URL.
	FilesLink FilesMode = "link"
)

// OnFetchErrorPolicy controls what happens when conversations.replies fails
// for a Thread part.
type OnFetchErrorPolicy string

const (
	// OnFetchErrorFail aborts the spawn and posts a failure message to the
	// triggering thread.
	OnFetchErrorFail OnFetchErrorPolicy = "fail"
	// OnFetchErrorOmit makes the part render empty (the whole part, including
	// any heading, vanishes from stdin).
	OnFetchErrorOmit OnFetchErrorPolicy = "omit"
)

// TriggerMessageSpec configures a `trigger_message` stdin part. All fields
// are optional; zero values yield the documented defaults.
type TriggerMessageSpec struct {
	// Heading is a free-text label rendered on its own line before the
	// wrapper tag. If the part renders empty, the heading disappears with it.
	// `{{...}}` tokens are rejected here — heading is a static label.
	Heading string `yaml:"heading,omitempty"`
	// Content selects which slice of the message body to include. Defaults
	// to ContentCommandText.
	Content ContentMode `yaml:"content,omitempty"`
	// IncludeTimestamps adds a human-readable timestamp next to the speaker
	// tag inside the wrapper. The ts field is always present regardless.
	IncludeTimestamps bool `yaml:"include_timestamps,omitempty"`
	// Files chooses how to render file attachments. Defaults to FilesNone.
	Files FilesMode `yaml:"files,omitempty"`
}

// ThreadSpec configures a `thread` stdin part.
type ThreadSpec struct {
	// Heading is a free-text label rendered on its own line before the
	// wrapper tag. If the part renders empty (e.g. standalone mention with
	// IncludeTriggeringMessage: false, or fetch failure under
	// OnFetchError: omit), the heading disappears with it.
	Heading string `yaml:"heading,omitempty"`
	// IncludeTriggeringMessage controls whether the message whose ts matches
	// the triggering event's ts is kept in the rendered thread. Default
	// false: the typical pattern pairs a `trigger_message` part for the
	// latest message with a `thread` part for prior context only.
	IncludeTriggeringMessage bool `yaml:"include_triggering_message,omitempty"`
	// MaxMessages / MaxBytes default to slackthread package defaults if zero.
	MaxMessages int `yaml:"max_messages,omitempty"`
	MaxBytes    int `yaml:"max_bytes,omitempty"`
	// Format selects "text" (default, human-readable speaker tags) or
	// "jsonl" (one JSON object per line). Wrapper tags are emitted in both.
	Format string `yaml:"format,omitempty"`
	// IncludeTimestamps adds human-readable timestamps to each message.
	IncludeTimestamps bool `yaml:"include_timestamps,omitempty"`
	// Files chooses how to render file attachments per message.
	Files FilesMode `yaml:"files,omitempty"`
	// OnFetchError controls behaviour when conversations.replies fails.
	// Defaults to OnFetchErrorFail.
	OnFetchError OnFetchErrorPolicy `yaml:"on_fetch_error,omitempty"`
}

// StdinPart is exactly one of `text`, `trigger_message`, or `thread`. The
// strict unmarshaler enforces single-variant occupancy at load time.
type StdinPart struct {
	Kind           StdinPartKind
	Text           string
	TriggerMessage *TriggerMessageSpec
	Thread         *ThreadSpec
}

// rawStdinPart is the strict-decode shadow used to detect "more than one
// variant present" cases.
type rawStdinPart struct {
	Text           *string             `yaml:"text,omitempty"`
	TriggerMessage *TriggerMessageSpec `yaml:"trigger_message,omitempty"`
	Thread         *ThreadSpec         `yaml:"thread,omitempty"`
}

// UnmarshalYAML decodes a part and ensures exactly one variant is set. The
// surrounding loader then validates contents (template var names, enum
// fields, etc.).
func (p *StdinPart) UnmarshalYAML(node *yaml.Node) error {
	var raw rawStdinPart
	if err := node.Decode(&raw); err != nil {
		return err
	}
	set := 0
	if raw.Text != nil {
		set++
	}
	if raw.TriggerMessage != nil {
		set++
	}
	if raw.Thread != nil {
		set++
	}
	switch set {
	case 0:
		return errors.New("stdin part must set exactly one of: text, trigger_message, thread")
	case 1:
		// ok
	default:
		return errors.New("stdin part must set only one of: text, trigger_message, thread")
	}
	switch {
	case raw.Text != nil:
		p.Kind = PartKindText
		p.Text = *raw.Text
	case raw.TriggerMessage != nil:
		p.Kind = PartKindTriggerMessage
		p.TriggerMessage = raw.TriggerMessage
	case raw.Thread != nil:
		p.Kind = PartKindThread
		p.Thread = raw.Thread
	}
	return nil
}

// Rule is the on-disk unit slackrun matches events against. Order matters:
// the first match wins.
type Rule struct {
	Name        string  `yaml:"name"`
	Description string  `yaml:"description,omitempty"`
	Trigger     Trigger `yaml:"trigger"`
	Action      Action  `yaml:"action"`
}

// RulesFile is the YAML document root.
type RulesFile struct {
	Rules []Rule `yaml:"rules"`
}

// ValidationIssueLevel separates fatal errors from soft warnings (warnings
// surface to operators but do not abort startup).
type ValidationIssueLevel string

const (
	IssueError ValidationIssueLevel = "error"
	IssueWarn  ValidationIssueLevel = "warn"
)

// ValidationIssue describes a single problem found by ValidateRules.
type ValidationIssue struct {
	Level    ValidationIssueLevel
	RuleName string // empty when the issue is file-wide
	Message  string
}

// CheckOptions toggles expensive / side-effecting validators (e.g. cwd
// existence). Tests typically pass SkipFsChecks: true.
type CheckOptions struct {
	SkipFsChecks bool
}

var (
	nameRe       = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	channelIDRe  = regexp.MustCompile(`^[CG][A-Z0-9]{2,}$`)
	botUserIDRe  = regexp.MustCompile(`^[UW][A-Z0-9]{2,}$`)
	appIDRe      = regexp.MustCompile(`^A[A-Z0-9]{2,}$`)
	envVarNameRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

	// anyTemplateRe matches any `{{name}}` token in a config string. Kept
	// independent of the dispatch package's recognized-name regex so the
	// rules loader stays cycle-free and so a typo like `{{event.permalinks}}`
	// is rejected here without needing to round-trip through expansion.
	anyTemplateRe = regexp.MustCompile(`\{\{\s*([^{}\s]+)\s*\}\}`)

	// knownTemplateVarsInRules lists the variables that may appear inside a
	// `text:` part. Strictly metadata — opaque identifiers / URLs — so the
	// trust boundary between author-written instructions and Slack-derived
	// content stays intact. Body variables route through trigger_message.
	// Keep in sync with dispatch.ExpandTemplate.
	knownTemplateVarsInRules = map[string]struct{}{
		"event.permalink":  {},
		"event.channel_id": {},
		"event.user_id":    {},
		"event.ts":         {},
		"event.thread_ts":  {},
	}

	// legacyVarHints maps a removed variable name to a hint for the rule
	// author. Surfaces at load time so half-remembered old syntax does not
	// silently no-op. Both bare (`text`) and namespaced (`event.text`)
	// forms are covered.
	legacyVarHints = map[string]string{
		"text":           "use a `trigger_message` part (content: command_text is the default)",
		"rest":           "use a `trigger_message` part (content: command_text is the default)",
		"body":           "use a `trigger_message` part with content: body_text",
		"raw_text":       "use a `trigger_message` part with content: raw_text",
		"user":           "use {{event.user_id}}",
		"channel":        "use {{event.channel_id}}",
		"permalink":      "use {{event.permalink}}",
		"ts":             "use {{event.ts}}",
		"thread_ts":      "use {{event.thread_ts}}",
		"event.text":     "use a `trigger_message` part (content: command_text is the default)",
		"event.rest":     "use a `trigger_message` part (content: command_text is the default)",
		"event.body":     "use a `trigger_message` part with content: body_text",
		"event.raw_text": "use a `trigger_message` part with content: raw_text",
	}
)

// ValidationResult is what loaders return.
type ValidationResult struct {
	Rules  []Rule
	Issues []ValidationIssue
}

// HasErrors reports whether any issue prevents startup.
func (r ValidationResult) HasErrors() bool {
	for _, i := range r.Issues {
		if i.Level == IssueError {
			return true
		}
	}
	return false
}

// ParseRulesYAML parses YAML text and applies the strict (no unknown fields)
// schema check. Semantic validation (cwd existence, duplicate names, etc.)
// happens in ValidateRules.
func ParseRulesYAML(text []byte, source string) (RulesFile, error) {
	dec := yaml.NewDecoder(bytes.NewReader(text))
	dec.KnownFields(true)
	var out RulesFile
	if err := dec.Decode(&out); err != nil {
		return RulesFile{}, fmt.Errorf("parse %s: %w", source, err)
	}
	if len(out.Rules) == 0 {
		return RulesFile{}, fmt.Errorf("parse %s: at least one rule is required", source)
	}
	return out, nil
}

// LoadRulesFile reads + parses + validates a rules file from disk.
func LoadRulesFile(path string, opts CheckOptions) (ValidationResult, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return ValidationResult{}, fmt.Errorf("resolve %s: %w", path, err)
	}
	text, err := os.ReadFile(abs)
	if err != nil {
		return ValidationResult{}, fmt.Errorf("read %s: %w", abs, err)
	}
	parsed, err := ParseRulesYAML(text, abs)
	if err != nil {
		return ValidationResult{}, err
	}
	issues := ValidateRules(parsed.Rules, opts)
	return ValidationResult{Rules: parsed.Rules, Issues: issues}, nil
}

// ValidateRules runs the semantic checks on top of schema parsing. Returns a
// flat list of issues; callers decide whether to abort.
func ValidateRules(rules []Rule, opts CheckOptions) []ValidationIssue {
	var issues []ValidationIssue

	for _, r := range rules {
		issues = append(issues, validateRule(r)...)
	}

	nameCount := map[string]int{}
	for _, r := range rules {
		nameCount[r.Name]++
	}
	for name, n := range nameCount {
		if n > 1 {
			issues = append(issues, ValidationIssue{
				Level:   IssueError,
				Message: fmt.Sprintf("duplicate rule name %q (%d times)", name, n),
			})
		}
	}

	var defaults []string
	for _, r := range rules {
		if r.Trigger.Type == TriggerTypeAppMention && r.Trigger.Keyword == nil {
			defaults = append(defaults, r.Name)
		}
	}
	if len(defaults) > 1 {
		issues = append(issues, ValidationIssue{
			Level:   IssueError,
			Message: fmt.Sprintf("multiple default app_mention rules without keyword: %s — only one is allowed", strings.Join(defaults, ", ")),
		})
	}

	keywordSeen := map[string]string{}
	for _, r := range rules {
		if r.Trigger.Type != TriggerTypeAppMention || r.Trigger.Keyword == nil {
			continue
		}
		key := strings.ToLower(*r.Trigger.Keyword)
		if prev, ok := keywordSeen[key]; ok {
			issues = append(issues, ValidationIssue{
				Level:    IssueError,
				RuleName: r.Name,
				Message:  fmt.Sprintf("duplicate keyword %q also used by rule %s", *r.Trigger.Keyword, prev),
			})
		} else {
			keywordSeen[key] = r.Name
		}
	}

	chanToRules := map[string][]string{}
	for _, r := range rules {
		if r.Trigger.Type != TriggerTypeMessage {
			continue
		}
		chanToRules[r.Trigger.Channel] = append(chanToRules[r.Trigger.Channel], r.Name)
	}
	for ch, names := range chanToRules {
		if len(names) > 1 {
			issues = append(issues, ValidationIssue{
				Level:   IssueWarn,
				Message: fmt.Sprintf("multiple message rules on channel %s: %s (first match wins)", ch, strings.Join(names, ", ")),
			})
		}
	}

	if !opts.SkipFsChecks {
		for _, r := range rules {
			st, err := os.Stat(r.Action.Cwd)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					issues = append(issues, ValidationIssue{
						Level:    IssueError,
						RuleName: r.Name,
						Message:  fmt.Sprintf("cwd does not exist: %s", r.Action.Cwd),
					})
				} else {
					issues = append(issues, ValidationIssue{
						Level:    IssueError,
						RuleName: r.Name,
						Message:  fmt.Sprintf("cwd check failed: %v", err),
					})
				}
				continue
			}
			if !st.IsDir() {
				issues = append(issues, ValidationIssue{
					Level:    IssueError,
					RuleName: r.Name,
					Message:  fmt.Sprintf("cwd is not a directory: %s", r.Action.Cwd),
				})
			}
		}
	}

	return issues
}

func validateRule(r Rule) []ValidationIssue {
	var out []ValidationIssue
	add := func(level ValidationIssueLevel, msg string) {
		out = append(out, ValidationIssue{Level: level, RuleName: r.Name, Message: msg})
	}

	if r.Name == "" {
		add(IssueError, "rule.name is required")
	} else if !nameRe.MatchString(r.Name) {
		add(IssueError, fmt.Sprintf("rule.name %q must match [a-zA-Z0-9_-]+", r.Name))
	}

	if r.Action.Cwd == "" {
		add(IssueError, "action.cwd is required")
	} else if !filepath.IsAbs(r.Action.Cwd) {
		add(IssueError, fmt.Sprintf("action.cwd must be an absolute path (got %q)", r.Action.Cwd))
	}
	if len(r.Action.Command) == 0 {
		add(IssueError, "action.command must be a non-empty argv")
	} else if strings.TrimSpace(r.Action.Command[0]) == "" {
		add(IssueError, "action.command[0] (program) is empty")
	}
	// `{{var}}` in argv is rejected because the expanded value would land on
	// the child's argv where it is visible to `ps aux`. Route variable
	// content through a `text:` stdin part instead.
	for i, arg := range r.Action.Command {
		if anyTemplateRe.MatchString(arg) {
			add(IssueError, fmt.Sprintf("action.command[%d] contains a `{{var}}` token — argv expansion is forbidden (leaks via `ps aux`); use a `text:` stdin part instead", i))
		}
	}
	if r.Action.TimeoutMs <= 0 {
		add(IssueError, fmt.Sprintf("action.timeout_ms must be > 0 (got %d)", r.Action.TimeoutMs))
	}
	if r.Action.Stdin != nil {
		validateStdin(r.Action.Stdin, add)
	}
	for k := range r.Action.Env {
		if !envVarNameRe.MatchString(k) {
			add(IssueError, fmt.Sprintf("action.env key %q is not a valid env var name", k))
		}
		if runner.IsProtectedEnvKey(k) {
			add(IssueError, fmt.Sprintf("action.env key %q is reserved (slackrun manages it; use expose_slack_token for SLACK_BOT_TOKEN)", k))
		}
	}

	switch r.Trigger.Type {
	case TriggerTypeMessage:
		if !channelIDRe.MatchString(r.Trigger.Channel) {
			add(IssueError, fmt.Sprintf("trigger.channel %q must be a Slack channel ID like CXXXXXXXX", r.Trigger.Channel))
		}
		if r.Trigger.From != nil {
			f := r.Trigger.From
			total := len(f.BotUserIDs) + len(f.AppIDs) + len(f.Usernames)
			if total == 0 {
				add(IssueError, "trigger.from must list at least one of bot_user_ids/app_ids/usernames")
			}
			for _, id := range f.BotUserIDs {
				if !botUserIDRe.MatchString(id) {
					add(IssueError, fmt.Sprintf("trigger.from.bot_user_ids: %q must look like UXXXXXXXX", id))
				}
			}
			for _, id := range f.AppIDs {
				if !appIDRe.MatchString(id) {
					add(IssueError, fmt.Sprintf("trigger.from.app_ids: %q must look like AXXXXXXXX", id))
				}
			}
			for _, u := range f.Usernames {
				if strings.TrimSpace(u) == "" {
					add(IssueError, "trigger.from.usernames must not contain empty entries")
				}
			}
		}
	case TriggerTypeAppMention:
		if r.Trigger.Keyword != nil && strings.TrimSpace(*r.Trigger.Keyword) == "" {
			add(IssueError, "trigger.keyword must not be empty (omit the field for a default rule)")
		}
	}
	return out
}

// validateStdin checks the parts list: empty array is an error, body
// variables in `text:` parts are rejected with a hint, trigger_message /
// thread parts must be unique, and enum fields must match the supported
// set.
func validateStdin(parts []StdinPart, add func(ValidationIssueLevel, string)) {
	if len(parts) == 0 {
		add(IssueError, "action.stdin must contain at least one part")
		return
	}
	triggerMessageCount := 0
	threadCount := 0
	for i, p := range parts {
		prefix := fmt.Sprintf("action.stdin[%d]", i)
		switch p.Kind {
		case PartKindText:
			validateTextPart(p.Text, prefix, add)
		case PartKindTriggerMessage:
			triggerMessageCount++
			validateTriggerMessagePart(p.TriggerMessage, prefix, add)
		case PartKindThread:
			threadCount++
			validateThreadPart(p.Thread, prefix, add)
		default:
			add(IssueError, prefix+" has no recognized part variant")
		}
	}
	if triggerMessageCount > 1 {
		add(IssueError, fmt.Sprintf("action.stdin has %d trigger_message parts; at most one is allowed", triggerMessageCount))
	}
	if threadCount > 1 {
		add(IssueError, fmt.Sprintf("action.stdin has %d thread parts; at most one is allowed", threadCount))
	}
}

func validateTextPart(text, prefix string, add func(ValidationIssueLevel, string)) {
	for _, m := range anyTemplateRe.FindAllStringSubmatch(text, -1) {
		name := m[1]
		if _, ok := knownTemplateVarsInRules[name]; ok {
			continue
		}
		if hint, ok := legacyVarHints[name]; ok {
			add(IssueError, fmt.Sprintf("%s.text references {{%s}} — that variable was removed; %s", prefix, name, hint))
			continue
		}
		add(IssueError, fmt.Sprintf("%s.text references unknown variable {{%s}}", prefix, name))
	}
}

func validateTriggerMessagePart(spec *TriggerMessageSpec, prefix string, add func(ValidationIssueLevel, string)) {
	if spec == nil {
		// `trigger_message: {}` is valid and decodes to an empty struct;
		// `trigger_message:` (nil literal) reaches here.
		return
	}
	if anyTemplateRe.MatchString(spec.Heading) {
		add(IssueError, prefix+".trigger_message.heading must not contain `{{var}}` tokens (emit a `text:` part for computed content)")
	}
	switch spec.Content {
	case "", ContentCommandText, ContentBodyText, ContentRawText:
	default:
		add(IssueError, fmt.Sprintf("%s.trigger_message.content must be \"command_text\", \"body_text\", or \"raw_text\" (got %q)", prefix, spec.Content))
	}
	switch spec.Files {
	case "", FilesNone, FilesLink:
	default:
		add(IssueError, fmt.Sprintf("%s.trigger_message.files must be \"none\" or \"link\" (got %q)", prefix, spec.Files))
	}
}

func validateThreadPart(spec *ThreadSpec, prefix string, add func(ValidationIssueLevel, string)) {
	if spec == nil {
		return
	}
	if anyTemplateRe.MatchString(spec.Heading) {
		add(IssueError, prefix+".thread.heading must not contain `{{var}}` tokens (emit a `text:` part for computed content)")
	}
	if spec.MaxMessages < 0 {
		add(IssueError, fmt.Sprintf("%s.thread.max_messages must be >= 0 (got %d)", prefix, spec.MaxMessages))
	}
	if spec.MaxBytes < 0 {
		add(IssueError, fmt.Sprintf("%s.thread.max_bytes must be >= 0 (got %d)", prefix, spec.MaxBytes))
	}
	switch spec.Format {
	case "", "text", "jsonl":
	default:
		add(IssueError, fmt.Sprintf("%s.thread.format must be \"text\" or \"jsonl\" (got %q)", prefix, spec.Format))
	}
	switch spec.Files {
	case "", FilesNone, FilesLink:
	default:
		add(IssueError, fmt.Sprintf("%s.thread.files must be \"none\" or \"link\" (got %q)", prefix, spec.Files))
	}
	switch spec.OnFetchError {
	case "", OnFetchErrorFail, OnFetchErrorOmit:
	default:
		add(IssueError, fmt.Sprintf("%s.thread.on_fetch_error must be \"fail\" or \"omit\" (got %q)", prefix, spec.OnFetchError))
	}
}
