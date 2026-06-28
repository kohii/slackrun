package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

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
// shell), so `;` `$(...)` and backticks are inert; each element is run
// through ExpandTemplate first.
type Action struct {
	Cwd       string            `yaml:"cwd"`
	Command   []string          `yaml:"command"`
	TimeoutMs int               `yaml:"timeout_ms"`
	Env       map[string]string `yaml:"env,omitempty"`
	Label     string            `yaml:"label,omitempty"`
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

// nameRe constrains rule.name to identifier-ish characters so the value is
// safe in logs and metric labels.
var (
	nameRe         = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	channelIDRe    = regexp.MustCompile(`^[CG][A-Z0-9]{2,}$`)
	botUserIDRe    = regexp.MustCompile(`^[UW][A-Z0-9]{2,}$`)
	appIDRe        = regexp.MustCompile(`^A[A-Z0-9]{2,}$`)
	envVarNameRe   = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
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

	// Per-rule shape checks (the schema layer rejects unknown fields but does
	// not validate inter-field constraints like "from must list at least one
	// id type").
	for _, r := range rules {
		issues = append(issues, validateRule(r)...)
	}

	// Duplicate rule names — show up in logs / dry-run / future metrics.
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

	// app_mention: at most one keyword-less default rule.
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

	// Duplicate keywords (case-insensitive) — first match wins so silent
	// shadowing is bad UX.
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

	// Channel overlap warning — multiple message rules on the same channel
	// silently make every rule but the first dead.
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

	// cwd existence — easy to miss on a fresh machine and the failure mode is
	// silent (exec returns ENOENT). Check at load time.
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

	// Action checks
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
	if r.Action.TimeoutMs <= 0 {
		add(IssueError, fmt.Sprintf("action.timeout_ms must be > 0 (got %d)", r.Action.TimeoutMs))
	}
	for k := range r.Action.Env {
		if !envVarNameRe.MatchString(k) {
			add(IssueError, fmt.Sprintf("action.env key %q is not a valid env var name", k))
		}
	}

	// Trigger checks
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
