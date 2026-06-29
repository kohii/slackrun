package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kohii/slackrun/internal/config"
	"github.com/kohii/slackrun/internal/dispatch"
	"github.com/kohii/slackrun/internal/slackthread"
)

// dryRunInput is the shape we expect from --event. We try to mirror Slack's
// own JSON keys so users can paste a real payload.
type dryRunInput struct {
	Type     string `json:"type"`
	Subtype  string `json:"subtype"`
	Channel  string `json:"channel"`
	User     string `json:"user"`
	BotID    string `json:"bot_id"`
	AppID    string `json:"app_id"`
	Username string `json:"username"`
	TS       string `json:"ts"`
	ThreadTS string `json:"thread_ts"`
	Text     string `json:"text"`
	BotProfile *struct {
		Name string `json:"name"`
	} `json:"bot_profile"`
	Message *struct {
		Text       string `json:"text"`
		User       string `json:"user"`
		BotID      string `json:"bot_id"`
		AppID      string `json:"app_id"`
		Username   string `json:"username"`
		BotProfile *struct {
			Name string `json:"name"`
		} `json:"bot_profile"`
	} `json:"message"`
}

// RunDryRun parses args and emits a JSON report of how a single event would
// be routed. Spawns nothing.
//
// Usage: slackrun dry-run <rules.yaml> --event <file.json> [--self-user-id U…] [--self-bot-id B…] [--allowed-user-ids U…,U…]
func RunDryRun(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: slackrun dry-run <rules.yaml> --event <event.json> [...]")
		return 2
	}
	rulesPath := args[0]

	fs := flag.NewFlagSet("dry-run", flag.ContinueOnError)
	fs.SetOutput(stderr)

	eventPath := fs.String("event", "", "path to JSON file containing a Slack event")
	selfUser := fs.String("self-user-id", "U00SELFTEST", "the bot's own Slack user ID (used for self-loop guard)")
	selfBot := fs.String("self-bot-id", "", "the bot's own B-prefixed bot ID (optional)")
	allowed := fs.String("allowed-user-ids", "", "comma-separated user IDs allowed to invoke mentions")

	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	if *eventPath == "" {
		fmt.Fprintln(stderr, "--event is required")
		return 2
	}

	rulesResult, err := config.LoadRulesFile(rulesPath, config.CheckOptions{SkipFsChecks: true})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	raw, err := os.ReadFile(*eventPath)
	if err != nil {
		fmt.Fprintln(stderr, "read event:", err)
		return 1
	}
	var ev dryRunInput
	if err := json.Unmarshal(raw, &ev); err != nil {
		fmt.Fprintln(stderr, "parse event JSON:", err)
		return 1
	}

	dispEv := dispatch.IncomingEvent{
		Type:     ev.Type,
		Subtype:  ev.Subtype,
		Channel:  ev.Channel,
		User:     ev.User,
		BotID:    ev.BotID,
		AppID:    ev.AppID,
		Username: ev.Username,
		TS:       ev.TS,
		ThreadTS: ev.ThreadTS,
		Text:     ev.Text,
	}
	if ev.BotProfile != nil {
		dispEv.BotProfileName = ev.BotProfile.Name
	}
	if ev.Message != nil {
		dispEv.Nested = &dispatch.NestedMessage{
			Text:     ev.Message.Text,
			User:     ev.Message.User,
			BotID:    ev.Message.BotID,
			AppID:    ev.Message.AppID,
			Username: ev.Message.Username,
		}
		if ev.Message.BotProfile != nil {
			dispEv.Nested.BotProfileName = ev.Message.BotProfile.Name
		}
	}

	allowedIDs := splitCSV(*allowed)
	res := dispatch.Match(dispEv, rulesResult.Rules, dispatch.MatcherContext{
		SelfUserID:     *selfUser,
		SelfBotID:      *selfBot,
		AllowedUserIDs: allowedIDs,
	})

	report := map[string]any{
		"kind":   matchKindName(res.Kind),
		"reason": res.Reason,
	}
	if res.Kind == dispatch.MatchKindMatched {
		vars := dispatch.TemplateVars{
			Permalink: "<<permalink>>",
			Text:      res.Text,
			Rest:      res.Rest,
			Channel:   dispEv.Channel,
			User:      dispEv.User,
		}
		report["rule"] = res.Rule.Name
		report["cwd"] = res.Rule.Action.Cwd
		report["timeout_ms"] = res.Rule.Action.TimeoutMs
		report["command"] = res.Rule.Action.Command
		report["text"] = res.Text
		report["first_token"] = res.FirstToken
		report["rest"] = res.Rest
		// Render stdin parts so operators can see the shape (and template
		// expansion) without actually calling Slack. To make
		// `exclude_triggering_message` visible we synthesize a single-message
		// "thread" from the event payload when the event has a TS.
		if spec := res.Rule.Action.Stdin; spec != nil {
			report["stdin"] = renderStdinPreview(spec, vars, dispEv)
			report["thread_fetch"] = stdinUsesThread(spec)
		}
	} else if res.Kind == dispatch.MatchKindNoMatch {
		report["text"] = res.Text
		report["first_token"] = res.FirstToken
	}

	out, _ := json.MarshalIndent(report, "", "  ")
	fmt.Fprintln(stdout, string(out))
	return 0
}

func matchKindName(k dispatch.MatchKind) string {
	switch k {
	case dispatch.MatchKindMatched:
		return "matched"
	case dispatch.MatchKindSkip:
		return "skip"
	case dispatch.MatchKindUnauthorized:
		return "unauthorized"
	case dispatch.MatchKindNoMatch:
		return "no-match"
	}
	return "unknown"
}

// renderStdinPreview composes the parts as runMatched would, but without
// fetching a real thread. To make exclude_triggering_message observable in
// preview, we synthesize a single-message "thread" from the event payload
// when the event has a TS. operators can therefore see the part collapse
// to empty when exclude_triggering_message removes the only message.
func renderStdinPreview(s *config.StdinSpec, vars dispatch.TemplateVars, ev dispatch.IncomingEvent) string {
	if s == nil {
		return ""
	}
	thread := previewThread(ev)
	var sb strings.Builder
	for _, p := range s.Parts {
		switch p.Kind {
		case config.PartKindText:
			sb.WriteString(p.Text)
		case config.PartKindTemplate:
			sb.WriteString(dispatch.ExpandTemplate(p.Template, vars))
		case config.PartKindSlackThread:
			msgs := thread
			if p.SlackThread != nil && p.SlackThread.ExcludeTriggeringMessage && ev.TS != "" {
				msgs = filterOutTS(msgs, ev.TS)
				if len(msgs) == 0 {
					continue
				}
			}
			opts := slackthread.FormatOptions{}
			if p.SlackThread != nil {
				opts.Format = slackthread.Format(p.SlackThread.Format)
				opts.MaxMessages = p.SlackThread.MaxMessages
				opts.MaxBytes = p.SlackThread.MaxBytes
			}
			sb.WriteString(slackthread.Render(msgs, opts))
		}
	}
	return sb.String()
}

// previewThread synthesizes a one-message thread from the event payload so
// the preview reflects what runMatched would receive when the thread has no
// replies (or when exclude_triggering_message is applied). Returns nil if
// the event lacks a TS.
func previewThread(ev dispatch.IncomingEvent) []slackthread.Message {
	if ev.TS == "" {
		return nil
	}
	m := slackthread.Message{TS: ev.TS, Text: ev.Text}
	if ev.User != "" {
		m.Source = slackthread.SourceUser
		m.User = ev.User
	} else {
		m.Source = slackthread.SourceBot
		if ev.Username != "" {
			m.Bot = ev.Username
		} else if ev.BotProfileName != "" {
			m.Bot = ev.BotProfileName
		} else {
			m.Bot = ev.BotID
		}
	}
	return []slackthread.Message{m}
}

func filterOutTS(msgs []slackthread.Message, ts string) []slackthread.Message {
	out := msgs[:0:0]
	for _, m := range msgs {
		if m.TS == ts {
			continue
		}
		out = append(out, m)
	}
	return out
}

func stdinUsesThread(s *config.StdinSpec) bool {
	if s == nil {
		return false
	}
	for _, p := range s.Parts {
		if p.Kind == config.PartKindSlackThread {
			return true
		}
	}
	return false
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
