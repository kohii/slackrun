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

// TriggerFrom narrows a rule by sender. At least one of `user_ids` / `app_ids`
// / `usernames` must be non-empty, OR `any: true` must be set (validated in
// ValidateRules).
//
//   - user_ids: `U`/`W`-prefixed Slack member IDs, matched against event.user.
//     Applies to both humans and bot users; the `bot_id` (B-prefix) namespace
//     is separate — use `app_ids` for bots that only publish bot_id.
//   - app_ids: `A`-prefixed Slack app IDs. Only meaningful for `type: message`
//     — `app_mention` events are always human-authored.
//   - usernames: display names (weakest signal — any incoming webhook can
//     pick its own name). Only meaningful for `type: message`.
//   - any: opt-in escape hatch to accept any sender. Mutually exclusive with
//     the ID/name lists. Only meaningful for `type: message`; on
//     `app_mention` the top-level `allowed_user_ids` already gates the
//     sender.
type TriggerFrom struct {
	UserIDs   []string `yaml:"user_ids,omitempty"`
	AppIDs    []string `yaml:"app_ids,omitempty"`
	Usernames []string `yaml:"usernames,omitempty"`
	Any       bool     `yaml:"any,omitempty"`
}

// rawTriggerFrom shadows TriggerFrom for strict decoding so we can detect the
// legacy `bot_user_ids` key and surface a migration hint instead of the
// unhelpful "field not found" from KnownFields.
type rawTriggerFrom struct {
	UserIDs       []string `yaml:"user_ids,omitempty"`
	AppIDs        []string `yaml:"app_ids,omitempty"`
	Usernames     []string `yaml:"usernames,omitempty"`
	Any           bool     `yaml:"any,omitempty"`
	LegacyBotUser []string `yaml:"bot_user_ids,omitempty"`
}

// UnmarshalYAML converts legacy `bot_user_ids` into a targeted error and
// otherwise decodes the fields verbatim.
func (f *TriggerFrom) UnmarshalYAML(node *yaml.Node) error {
	var raw rawTriggerFrom
	if err := node.Decode(&raw); err != nil {
		return err
	}
	if len(raw.LegacyBotUser) > 0 {
		return errors.New("trigger.from.bot_user_ids was renamed to `user_ids` (rename only — the match target has always been event.user's U/W-prefixed ID, human or bot user alike)")
	}
	f.UserIDs = raw.UserIDs
	f.AppIDs = raw.AppIDs
	f.Usernames = raw.Usernames
	f.Any = raw.Any
	return nil
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
	// MatchThreadReplies opts out of matching replies posted inside an
	// existing thread. nil (default) matches both top-level and thread
	// replies; false matches only top-level messages and thread parents.
	// Only meaningful for type: message.
	MatchThreadReplies *bool `yaml:"match_thread_replies,omitempty"`

	// app_mention variant. nil means "default rule" (matches when no other
	// keyword rule matched); at most one such rule may exist per file.
	Keyword *string `yaml:"keyword,omitempty"`

	// Extract declares named regex captures over the message body. The first
	// substring match per name is exposed to `text:` parts as
	// `{{extract.<name>}}`. A `required: true` extractor that finds no match
	// makes the rule non-match on that event (silently skipped, next rule
	// tried). Available for both message and app_mention triggers.
	Extract map[string]ExtractSpec `yaml:"extract,omitempty"`

	// extractRE holds the compiled Extract patterns, populated by
	// LoadRulesFile after ValidateRules. Unexported so YAML never sees it.
	extractRE map[string]*regexp.Regexp
}

// ExtractSpec is one named regex extractor. The regex uses Go's syntax
// (regexp/syntax). The first substring match becomes the value.
type ExtractSpec struct {
	Pattern  string `yaml:"pattern"`
	Required bool   `yaml:"required,omitempty"`
}

// CompiledExtract returns the compiled regex for a declared extractor. nil
// means no such extractor is declared (or compilation was skipped).
func (t Trigger) CompiledExtract(name string) *regexp.Regexp {
	return t.extractRE[name]
}

// AllowsThreadReplies reports whether the trigger accepts messages posted
// inside a thread (i.e. thread_ts != ts). Default true; opt-out via
// `match_thread_replies: false`.
func (t Trigger) AllowsThreadReplies() bool {
	return t.MatchThreadReplies == nil || *t.MatchThreadReplies
}

// rawTrigger is the strict-decode shadow type — we parse into it first so we
// can reject unexpected fields, then fold the result into Trigger.
type rawTrigger struct {
	Type               string                 `yaml:"type"`
	Channel            string                 `yaml:"channel,omitempty"`
	From               *TriggerFrom           `yaml:"from,omitempty"`
	MatchThreadReplies *bool                  `yaml:"match_thread_replies,omitempty"`
	Keyword            *string                `yaml:"keyword,omitempty"`
	Extract            map[string]ExtractSpec `yaml:"extract,omitempty"`
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
			return errors.New("trigger.from is required for type: message (use `from: { any: true }` to opt out of sender filtering)")
		}
		t.Type = TriggerTypeMessage
		t.Channel = raw.Channel
		t.From = raw.From
		t.MatchThreadReplies = raw.MatchThreadReplies
	case string(TriggerTypeAppMention):
		if raw.Channel != "" {
			return errors.New("trigger.channel is only valid for type: message")
		}
		if raw.MatchThreadReplies != nil {
			return errors.New("trigger.match_thread_replies is only valid for type: message")
		}
		if raw.From != nil {
			// app_mention: only user_ids is meaningful. The event is always
			// human-authored, so app_ids / usernames never fire; and per-rule
			// sender filtering pairs with top-level allowed_user_ids, so
			// `any: true` is redundant — omit `from` for the same effect.
			if raw.From.Any {
				return errors.New("trigger.from.any is not valid for type: app_mention (omit `from` for the same effect; top-level `allowed_user_ids` is the sender gate)")
			}
			if len(raw.From.AppIDs) > 0 || len(raw.From.Usernames) > 0 {
				return errors.New("trigger.from on type: app_mention only accepts user_ids (app_mention events are always human-authored)")
			}
			if len(raw.From.UserIDs) == 0 {
				return errors.New("trigger.from on type: app_mention must set user_ids (omit `from` entirely to accept any sender in allowed_user_ids)")
			}
		}
		t.Type = TriggerTypeAppMention
		t.Keyword = raw.Keyword
		t.From = raw.From
	default:
		return fmt.Errorf("trigger.type must be \"message\" or \"app_mention\" (got %q)", raw.Type)
	}
	t.Extract = raw.Extract
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
	// ReplyWithStdout controls whether slackrun posts the child's stdout
	// to the triggering Slack thread on success. Pointer so we can default
	// to true while still detecting an explicit `false`:
	//   nil / true  — current behaviour: progress message is overwritten
	//                 with stdout (chunked or uploaded as a file).
	//   false       — child takes full responsibility for replying (it can
	//                 use `slackrun post` directly); the progress message
	//                 is updated to "✅ Done" on success. Failures still
	//                 surface in Slack so silent crashes stay visible.
	ReplyWithStdout *bool `yaml:"reply_with_stdout,omitempty"`
	// ProgressStyle picks how slackrun surfaces "job in progress" state while
	// the child runs:
	//   "" / "message"     (default) — post a "⏳ Working…" placeholder
	//                       message and rewrite it via chat.update as the
	//                       job progresses and completes.
	//   "assistant_status" — use assistant.threads.setStatus for a
	//                        transient status indicator instead of a visible
	//                        message. There is no placeholder to rewrite, so
	//                        the final reply is always posted as a new
	//                        message.
	ProgressStyle string `yaml:"progress_style,omitempty"`
	// Stdin is an ordered list of parts. slackrun renders each part and
	// concatenates the results into the byte stream piped to the child's
	// stdin. Absent / nil means the child reads nothing.
	Stdin []StdinPart `yaml:"stdin,omitempty"`
}

// ReplyWithStdoutEnabled returns the resolved reply-with-stdout setting.
// Default is true (current behaviour); an explicit `false` opts out.
func (a Action) ReplyWithStdoutEnabled() bool {
	return a.ReplyWithStdout == nil || *a.ReplyWithStdout
}

// Progress style values for Action.ProgressStyle.
const (
	ProgressStyleMessage         = "message"
	ProgressStyleAssistantStatus = "assistant_status"
)

// ProgressStyleResolved returns the resolved progress style, defaulting to
// ProgressStyleMessage when unset.
func (a Action) ProgressStyleResolved() string {
	if a.ProgressStyle == "" {
		return ProgressStyleMessage
	}
	return a.ProgressStyle
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
	// PartKindSlackrunHelp injects clidoc.ChildUsage — the static help for
	// the child-facing subcommands (post / react / upload plus the
	// read side: history / replies / reactions / user / usergroups) — into
	// stdin. Useful when the spawned program is an LLM that needs to learn
	// how to interact with Slack. Requires `expose_slack_token: true` on
	// the rule (warning at load time if missing).
	PartKindSlackrunHelp
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

// SlackrunHelpSpec configures a `slackrun_help` stdin part. Currently no
// fields; the struct exists so the YAML form `slackrun_help: {}` decodes
// cleanly and so future options (e.g. selecting which subcommands to
// document) have a home.
type SlackrunHelpSpec struct{}

// StdinPart is exactly one of `text`, `trigger_message`, `thread`, or
// `slackrun_help`. The strict unmarshaler enforces single-variant
// occupancy at load time.
type StdinPart struct {
	Kind           StdinPartKind
	Text           string
	TriggerMessage *TriggerMessageSpec
	Thread         *ThreadSpec
	SlackrunHelp   *SlackrunHelpSpec
}

// rawStdinPart is the strict-decode shadow used to detect "more than one
// variant present" cases.
type rawStdinPart struct {
	Text           *string             `yaml:"text,omitempty"`
	TriggerMessage *TriggerMessageSpec `yaml:"trigger_message,omitempty"`
	Thread         *ThreadSpec         `yaml:"thread,omitempty"`
	SlackrunHelp   *SlackrunHelpSpec   `yaml:"slackrun_help,omitempty"`
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
	if raw.SlackrunHelp != nil {
		set++
	}
	switch set {
	case 0:
		return errors.New("stdin part must set exactly one of: text, trigger_message, thread, slackrun_help")
	case 1:
		// ok
	default:
		return errors.New("stdin part must set only one of: text, trigger_message, thread, slackrun_help")
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
	case raw.SlackrunHelp != nil:
		p.Kind = PartKindSlackrunHelp
		p.SlackrunHelp = raw.SlackrunHelp
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
//
// AllowedUserIDs is the top-level authorization list for `type: app_mention`
// rules: only these `U`/`W`-prefixed member IDs may @-mention the bot.
// Per-rule `trigger.from.user_ids` narrows this set further (must be a
// subset; enforced at load time). Has no effect on `type: message` rules —
// those use `trigger.from` for sender filtering, not authorization.
type RulesFile struct {
	AllowedUserIDs []string `yaml:"allowed_user_ids,omitempty"`
	Rules          []Rule   `yaml:"rules"`
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
	nameRe        = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	channelIDRe   = regexp.MustCompile(`^[CG][A-Z0-9]{2,}$`)
	botUserIDRe   = regexp.MustCompile(`^[UW][A-Z0-9]{2,}$`)
	appIDRe       = regexp.MustCompile(`^A[A-Z0-9]{2,}$`)
	envVarNameRe  = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	extractNameRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

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
	Rules          []Rule
	AllowedUserIDs []string
	Issues         []ValidationIssue
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
	for i := range parsed.Rules {
		expanded, err := expandHomeInCwd(parsed.Rules[i].Action.Cwd)
		if err != nil {
			return ValidationResult{}, fmt.Errorf("rule %q action.cwd: %w", parsed.Rules[i].Name, err)
		}
		parsed.Rules[i].Action.Cwd = expanded
	}
	issues := ValidateRulesFile(parsed, opts)
	// Compile extract patterns for rules that validated cleanly. Skipping
	// broken rules here avoids double-reporting a bad pattern (validateExtract
	// already surfaced the compile error as an issue).
	for i := range parsed.Rules {
		compileExtractors(&parsed.Rules[i])
	}
	return ValidationResult{
		Rules:          parsed.Rules,
		AllowedUserIDs: parsed.AllowedUserIDs,
		Issues:         issues,
	}, nil
}

// CompileForTest is a test-only helper that runs compileExtractors so tests
// in other packages can hand-build a Rule literal and then exercise the
// matcher's extract path without going through LoadRulesFile.
func CompileForTest(r *Rule) {
	compileExtractors(r)
}

func compileExtractors(r *Rule) {
	if len(r.Trigger.Extract) == 0 {
		return
	}
	compiled := make(map[string]*regexp.Regexp, len(r.Trigger.Extract))
	for name, spec := range r.Trigger.Extract {
		re, err := regexp.Compile(spec.Pattern)
		if err != nil {
			// validateExtract already flagged this; skip silently so callers
			// see one error, not two.
			continue
		}
		compiled[name] = re
	}
	r.Trigger.extractRE = compiled
}

// expandHomeInCwd resolves a leading "~" / "~/" against $HOME. `~user` is
// rejected: crossing home dirs from a rules file would be surprising, and
// the cross-platform lookup would drag in os/user.
func expandHomeInCwd(cwd string) (string, error) {
	if cwd == "" || !strings.HasPrefix(cwd, "~") {
		return cwd, nil
	}
	if cwd != "~" && !strings.HasPrefix(cwd, "~/") {
		return cwd, fmt.Errorf("`~user` prefix is not supported (got %q); use an absolute path or `~/…`", cwd)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return cwd, fmt.Errorf("resolve $HOME: %w", err)
	}
	if cwd == "~" {
		return home, nil
	}
	return filepath.Join(home, cwd[2:]), nil
}

// ValidateRulesFile runs the semantic checks over both top-level and per-rule
// fields. Returns a flat list of issues; callers decide whether to abort.
func ValidateRulesFile(file RulesFile, opts CheckOptions) []ValidationIssue {
	var issues []ValidationIssue

	allowed := map[string]struct{}{}
	for i, id := range file.AllowedUserIDs {
		if !botUserIDRe.MatchString(id) {
			issues = append(issues, ValidationIssue{
				Level:   IssueError,
				Message: fmt.Sprintf("allowed_user_ids[%d] %q must look like UXXXXXXXX", i, id),
			})
			continue
		}
		allowed[id] = struct{}{}
	}

	hasMention := false
	for _, r := range file.Rules {
		if r.Trigger.Type == TriggerTypeAppMention {
			hasMention = true
			break
		}
	}
	switch {
	case hasMention && len(file.AllowedUserIDs) == 0:
		issues = append(issues, ValidationIssue{
			Level:   IssueError,
			Message: "allowed_user_ids is required when the file has any type: app_mention rule (top-level authorization list)",
		})
	case !hasMention && len(file.AllowedUserIDs) > 0:
		// authorization list is only consulted for app_mention events; warn
		// so the operator notices the dead setting rather than assume it's
		// gating message rules.
		issues = append(issues, ValidationIssue{
			Level:   IssueWarn,
			Message: "allowed_user_ids is set but no type: app_mention rules exist — the list has no effect (message rules gate on trigger.from)",
		})
	}

	// Only run the subset check when a top-level list actually exists;
	// otherwise the "not in top-level" errors are all downstream of the
	// missing-list error above and would just add noise.
	if len(allowed) > 0 {
		for _, r := range file.Rules {
			if r.Trigger.Type == TriggerTypeAppMention && r.Trigger.From != nil {
				for _, id := range r.Trigger.From.UserIDs {
					if _, ok := allowed[id]; !ok {
						issues = append(issues, ValidationIssue{
							Level:    IssueError,
							RuleName: r.Name,
							Message:  fmt.Sprintf("trigger.from.user_ids contains %q which is not in top-level allowed_user_ids", id),
						})
					}
				}
			}
		}
	}

	issues = append(issues, validateRulesShared(file.Rules, opts)...)
	return issues
}

// ValidateRules is the legacy entry point that skips top-level allowed_user_ids
// checks. Retained for tests that hand-build a []Rule literal; new code should
// call ValidateRulesFile.
func ValidateRules(rules []Rule, opts CheckOptions) []ValidationIssue {
	return validateRulesShared(rules, opts)
}

func validateRulesShared(rules []Rule, opts CheckOptions) []ValidationIssue {
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
	switch r.Action.ProgressStyle {
	case "", ProgressStyleMessage, ProgressStyleAssistantStatus:
	default:
		add(IssueError, fmt.Sprintf("action.progress_style must be %q or %q (got %q)",
			ProgressStyleMessage, ProgressStyleAssistantStatus, r.Action.ProgressStyle))
	}
	validateExtract(r.Trigger.Extract, add)
	if r.Action.Stdin != nil {
		validateStdin(r.Action.Stdin, r.Action.ExposeSlackToken, r.Trigger.Extract, add)
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
			validateTriggerFrom(r.Trigger.From, add)
		}
	case TriggerTypeAppMention:
		if r.Trigger.Keyword != nil && strings.TrimSpace(*r.Trigger.Keyword) == "" {
			add(IssueError, "trigger.keyword must not be empty (omit the field for a default rule)")
		}
		if r.Trigger.From != nil {
			// Shape-checked in UnmarshalYAML (user_ids only). Here we only
			// validate the ID format.
			for _, id := range r.Trigger.From.UserIDs {
				if !botUserIDRe.MatchString(id) {
					add(IssueError, fmt.Sprintf("trigger.from.user_ids: %q must look like UXXXXXXXX", id))
				}
			}
		}
	}
	return out
}

// validateTriggerFrom is the message-variant validator: `any: true` is
// mutually exclusive with the ID/name lists, and at least one must be set.
func validateTriggerFrom(f *TriggerFrom, add func(ValidationIssueLevel, string)) {
	total := len(f.UserIDs) + len(f.AppIDs) + len(f.Usernames)
	if f.Any {
		if total > 0 {
			add(IssueError, "trigger.from.any is mutually exclusive with user_ids/app_ids/usernames")
		}
		return
	}
	if total == 0 {
		add(IssueError, "trigger.from must list at least one of user_ids/app_ids/usernames, or set `any: true`")
	}
	for _, id := range f.UserIDs {
		if !botUserIDRe.MatchString(id) {
			add(IssueError, fmt.Sprintf("trigger.from.user_ids: %q must look like UXXXXXXXX", id))
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

// validateStdin checks the parts list: empty array is an error, body
// variables in `text:` parts are rejected with a hint, trigger_message /
// thread parts must be unique, and enum fields must match the supported
// set.
//
// exposeSlackToken is the rule's flag so we can warn when `slackrun_help`
// is included without the token forwarding that makes the documented
// subcommands actually usable.
func validateStdin(parts []StdinPart, exposeSlackToken bool, extract map[string]ExtractSpec, add func(ValidationIssueLevel, string)) {
	if len(parts) == 0 {
		add(IssueError, "action.stdin must contain at least one part")
		return
	}
	triggerMessageCount := 0
	threadCount := 0
	hasSlackrunHelp := false
	for i, p := range parts {
		prefix := fmt.Sprintf("action.stdin[%d]", i)
		switch p.Kind {
		case PartKindText:
			validateTextPart(p.Text, prefix, extract, add)
		case PartKindTriggerMessage:
			triggerMessageCount++
			validateTriggerMessagePart(p.TriggerMessage, prefix, add)
		case PartKindThread:
			threadCount++
			validateThreadPart(p.Thread, prefix, add)
		case PartKindSlackrunHelp:
			hasSlackrunHelp = true
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
	if hasSlackrunHelp && !exposeSlackToken {
		// The injected help documents subcommands that need
		// SLACK_BOT_TOKEN. Without expose_slack_token: true the child will
		// hit "missing token" the first time it tries to follow the help.
		add(IssueWarn, "action.stdin contains a `slackrun_help` part but action.expose_slack_token is false — the documented subcommands will fail at runtime")
	}
}

func validateTextPart(text, prefix string, extract map[string]ExtractSpec, add func(ValidationIssueLevel, string)) {
	for _, m := range anyTemplateRe.FindAllStringSubmatch(text, -1) {
		name := m[1]
		if _, ok := knownTemplateVarsInRules[name]; ok {
			continue
		}
		if strings.HasPrefix(name, "extract.") {
			key := strings.TrimPrefix(name, "extract.")
			if _, ok := extract[key]; ok {
				continue
			}
			add(IssueError, fmt.Sprintf("%s.text references {{%s}} but trigger.extract has no such entry", prefix, name))
			continue
		}
		if hint, ok := legacyVarHints[name]; ok {
			add(IssueError, fmt.Sprintf("%s.text references {{%s}} — that variable was removed; %s", prefix, name, hint))
			continue
		}
		add(IssueError, fmt.Sprintf("%s.text references unknown variable {{%s}}", prefix, name))
	}
}

func validateExtract(extract map[string]ExtractSpec, add func(ValidationIssueLevel, string)) {
	for name, spec := range extract {
		prefix := fmt.Sprintf("trigger.extract[%q]", name)
		if !extractNameRe.MatchString(name) {
			add(IssueError, prefix+" name must match [a-z_][a-z0-9_]*")
		}
		if strings.TrimSpace(spec.Pattern) == "" {
			add(IssueError, prefix+".pattern is required")
			continue
		}
		if _, err := regexp.Compile(spec.Pattern); err != nil {
			add(IssueError, fmt.Sprintf("%s.pattern does not compile: %v", prefix, err))
		}
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
