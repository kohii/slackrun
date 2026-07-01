package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kohii/slackrun/internal/clidoc"
	"github.com/kohii/slackrun/internal/config"
	"github.com/kohii/slackrun/internal/dispatch"
	"github.com/kohii/slackrun/internal/slackthread"
)

// dryRunInput is the shape we expect from --event. We try to mirror Slack's
// own JSON keys so users can paste a real payload.
type dryRunInput struct {
	Type       string `json:"type"`
	Subtype    string `json:"subtype"`
	Channel    string `json:"channel"`
	User       string `json:"user"`
	BotID      string `json:"bot_id"`
	AppID      string `json:"app_id"`
	Username   string `json:"username"`
	TS         string `json:"ts"`
	ThreadTS   string `json:"thread_ts"`
	Text       string `json:"text"`
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
	allowed := fs.String("allowed-user-ids", "", "override rules.yaml top-level allowed_user_ids (comma-separated)")

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

	allowedIDs := rulesResult.AllowedUserIDs
	if *allowed != "" {
		allowedIDs = splitCSV(*allowed)
	}
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
			ChannelID: dispEv.Channel,
			UserID:    dispEv.User,
			TS:        dispEv.TS,
			ThreadTS:  dispEv.ThreadTS,
			Extract:   res.Extract,
		}
		if len(res.Extract) > 0 {
			report["extract"] = res.Extract
		}
		report["rule"] = res.Rule.Name
		report["cwd"] = res.Rule.Action.Cwd
		report["timeout_ms"] = res.Rule.Action.TimeoutMs
		report["command"] = res.Rule.Action.Command
		report["text"] = res.Text
		report["first_token"] = res.FirstToken
		report["rest"] = res.Rest
		// Render stdin parts so operators can see the shape (and variable
		// expansion) without actually calling Slack. To make
		// include_triggering_message visible we synthesize a single-message
		// "thread" from the event payload when the event has a TS.
		if parts := res.Rule.Action.Stdin; parts != nil {
			report["stdin"] = renderStdinPreview(parts, vars, dispEv, res)
			report["thread_fetch"] = stdinUsesThread(parts)
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
// fetching a real thread. A synthesized one-message thread mirrors the
// triggering event so include_triggering_message:false on a standalone
// mention collapses the part exactly as it would in production.
func renderStdinPreview(parts []config.StdinPart, vars dispatch.TemplateVars, ev dispatch.IncomingEvent, res dispatch.MatchResult) string {
	thread := previewThread(ev)
	var sb strings.Builder
	for _, p := range parts {
		switch p.Kind {
		case config.PartKindText:
			sb.WriteString(dispatch.ExpandTemplate(p.Text, vars))

		case config.PartKindTriggerMessage:
			spec := p.TriggerMessage
			if spec == nil {
				spec = &config.TriggerMessageSpec{}
			}
			msg := previewTriggerMessage(ev, res, spec.Content)
			body := slackthread.RenderTriggerMessage(msg, slackthread.RenderOptions{
				Nonce:             "preview",
				Format:            slackthread.FormatText,
				IncludeTimestamps: spec.IncludeTimestamps,
				Files:             slackthread.FilesMode(spec.Files),
			})
			writePartWithHeading(&sb, spec.Heading, body)

		case config.PartKindThread:
			spec := p.Thread
			if spec == nil {
				spec = &config.ThreadSpec{}
			}
			msgs := thread
			if !spec.IncludeTriggeringMessage && ev.TS != "" {
				msgs = filterOutTS(msgs, ev.TS)
			}
			body := slackthread.RenderThread(msgs, slackthread.RenderOptions{
				Nonce:             "preview",
				Format:            slackthread.Format(spec.Format),
				MaxMessages:       spec.MaxMessages,
				MaxBytes:          spec.MaxBytes,
				IncludeTimestamps: spec.IncludeTimestamps,
				Files:             slackthread.FilesMode(spec.Files),
			})
			writePartWithHeading(&sb, spec.Heading, body)

		case config.PartKindSlackrunHelp:
			sb.WriteString(clidoc.ChildUsage)
		}
	}
	return sb.String()
}

func writePartWithHeading(sb *strings.Builder, heading, body string) {
	if body == "" {
		return
	}
	if heading != "" {
		sb.WriteString(heading)
		if !strings.HasSuffix(heading, "\n") {
			sb.WriteByte('\n')
		}
	}
	sb.WriteString(body)
}

// previewThread synthesizes a one-message thread from the event payload so
// the preview reflects what runMatched would receive when the thread has no
// replies (or when include_triggering_message is applied). Returns nil if
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
		switch {
		case ev.Username != "":
			m.Bot = ev.Username
		case ev.BotProfileName != "":
			m.Bot = ev.BotProfileName
		default:
			m.Bot = ev.BotID
		}
	}
	return []slackthread.Message{m}
}

func previewTriggerMessage(ev dispatch.IncomingEvent, res dispatch.MatchResult, mode config.ContentMode) slackthread.Message {
	msg := slackthread.Message{
		TS:   ev.TS,
		Text: dispatch.MessageBody(ev, res, mode),
	}
	if ev.User != "" {
		msg.Source = slackthread.SourceUser
		msg.User = ev.User
	} else {
		msg.Source = slackthread.SourceBot
		switch {
		case ev.Username != "":
			msg.Bot = ev.Username
		case ev.BotProfileName != "":
			msg.Bot = ev.BotProfileName
		default:
			msg.Bot = ev.BotID
		}
	}
	for _, f := range dispatch.ExtractFiles(ev) {
		msg.Files = append(msg.Files, slackthread.File{Name: f.Name, URL: f.URL})
	}
	return msg
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

func stdinUsesThread(parts []config.StdinPart) bool {
	for _, p := range parts {
		if p.Kind == config.PartKindThread {
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
