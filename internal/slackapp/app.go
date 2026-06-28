package slackapp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kohii/slackrun/internal/config"
	"github.com/kohii/slackrun/internal/dispatch"
	"github.com/kohii/slackrun/internal/logging"
	"github.com/kohii/slackrun/internal/runner"
	"github.com/kohii/slackrun/internal/util"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

// App is the long-lived slackrun process. Construct with New, then call Run.
type App struct {
	env         config.AppEnv
	rules       []config.Rule
	api         *slack.Client
	sm          *socketmode.Client
	semaphore   *runner.Semaphore
	dedupe      *Dedupe
	selfUserID  string
	selfBotID   string

	jobs *jobRegistry
}

// Options configures a new App. BootTime defaults to time.Now if zero.
type Options struct {
	Env      config.AppEnv
	Rules    []config.Rule
	BootTime time.Time
}

// New constructs an App and runs auth.test. Returns an error if auth.test
// fails or returns no user_id (which means the token is wrong).
func New(ctx context.Context, opts Options) (*App, error) {
	api := slack.New(opts.Env.SlackBotToken, slack.OptionAppLevelToken(opts.Env.SlackAppToken))
	authResp, err := api.AuthTestContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth.test failed: %w", err)
	}
	if authResp.UserID == "" {
		return nil, fmt.Errorf("auth.test returned no user_id — check SLACK_BOT_TOKEN")
	}
	if authResp.BotID == "" {
		logging.Warn("auth.test returned no bot_id — self-loop guard via bot_id will be skipped",
			logging.F("hint", "expected when token is not xoxb- (bot token)"))
	}

	sem, err := runner.NewSemaphore(opts.Env.MaxConcurrent)
	if err != nil {
		return nil, err
	}

	boot := opts.BootTime
	if boot.IsZero() {
		boot = time.Now()
	}
	d := NewDedupe(DedupeOptions{
		TTL:            5 * time.Minute,
		BootTime:       boot,
		MinAgeFromBoot: time.Duration(opts.Env.MinEventAgeMsAtBoot) * time.Millisecond,
	})

	sm := socketmode.New(api, socketmode.OptionDebug(opts.Env.LogLevel == "debug"))

	app := &App{
		env:        opts.Env,
		rules:      opts.Rules,
		api:        api,
		sm:         sm,
		semaphore:  sem,
		dedupe:     d,
		selfUserID: authResp.UserID,
		selfBotID:  authResp.BotID,
		jobs:       newJobRegistry(),
	}
	logging.Info("bot ready",
		logging.F("team", authResp.Team),
		logging.F("user", authResp.User),
		logging.F("selfUserId", authResp.UserID),
		logging.F("selfBotId", authResp.BotID),
		logging.F("allowedUserIds", opts.Env.AllowedUserIDs),
		logging.F("rules", ruleSummaries(opts.Rules)),
	)
	return app, nil
}

// Run drives the Socket Mode connection and event loop. Blocks until ctx is
// cancelled or the underlying connection fails. In-flight jobs receive a kill
// and their progress messages are overwritten with "⚠️ Bot stopped" before
// Run returns.
func (a *App) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var loopWG sync.WaitGroup
	loopWG.Add(1)
	go func() {
		defer loopWG.Done()
		a.eventLoop(runCtx)
	}()

	runErr := a.sm.RunContext(runCtx)
	// Stop the dispatcher loop and wait for it to drain.
	cancel()
	loopWG.Wait()

	// Best-effort: kill running jobs and finalize their progress. The 7-second
	// budget is the runner's 5s SIGTERM-grace plus 2s for the Web API write.
	a.jobs.stopAll("⚠️ Bot stopped", 7*time.Second)
	return runErr
}

func (a *App) eventLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-a.sm.Events:
			if !ok {
				return
			}
			a.handleEvent(ctx, evt)
		}
	}
}

func (a *App) handleEvent(ctx context.Context, evt socketmode.Event) {
	switch evt.Type {
	case socketmode.EventTypeConnecting:
		logging.Info("socketmode connecting")
	case socketmode.EventTypeConnected:
		logging.Info("socketmode connected")
	case socketmode.EventTypeDisconnect:
		logging.Warn("socketmode disconnect")
	case socketmode.EventTypeInvalidAuth:
		logging.Error("socketmode invalid auth (check SLACK_APP_TOKEN)")
	case socketmode.EventTypeHello:
		// no-op
	case socketmode.EventTypeEventsAPI:
		eventsAPI, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			logging.Warn("unexpected events-api payload", logging.F("data", fmt.Sprintf("%T", evt.Data)))
			return
		}
		// Ack first so Slack doesn't re-deliver while we work.
		if evt.Request != nil {
			if err := a.sm.Ack(*evt.Request); err != nil {
				logging.Warn("socketmode ack failed", logging.F("error", err))
			}
		}
		a.handleEventsAPI(ctx, eventsAPI)
	}
}

func (a *App) handleEventsAPI(ctx context.Context, e slackevents.EventsAPIEvent) {
	switch inner := e.InnerEvent.Data.(type) {
	case *slackevents.AppMentionEvent:
		a.handleIncoming(ctx, fromAppMention(inner))
	case *slackevents.MessageEvent:
		a.handleIncoming(ctx, fromMessage(inner))
	default:
		logging.Debug("unhandled inner event", logging.F("type", e.InnerEvent.Type))
	}
}

func (a *App) handleIncoming(ctx context.Context, ev dispatch.IncomingEvent) {
	res := dispatch.Match(ev, a.rules, dispatch.MatcherContext{
		SelfUserID:     a.selfUserID,
		SelfBotID:      a.selfBotID,
		AllowedUserIDs: a.env.AllowedUserIDs,
	})
	switch res.Kind {
	case dispatch.MatchKindSkip:
		logging.Debug("dispatcher skip", logging.F("reason", res.Reason))
		return
	case dispatch.MatchKindUnauthorized:
		logging.Warn("dispatcher unauthorized", logging.F("reason", res.Reason))
		return
	case dispatch.MatchKindNoMatch:
		logging.Info("dispatcher no-match", logging.F("type", ev.Type))
		return
	}

	// Dedupe is gated after match so we don't burn keys on events that match
	// no rule — Slack double-fires app_mention + message and only one of
	// those typically matches.
	if ev.Channel != "" && ev.TS != "" {
		switch a.dedupe.Decide(ev.Channel, ev.TS) {
		case DedupeDuplicate:
			logging.Info("dispatcher dedupe",
				logging.F("kind", "duplicate"),
				logging.F("channel", ev.Channel),
				logging.F("ts", ev.TS),
				logging.F("rule", res.Rule.Name))
			return
		case DedupeTooOld:
			logging.Info("dispatcher dedupe",
				logging.F("kind", "too-old"),
				logging.F("channel", ev.Channel),
				logging.F("ts", ev.TS),
				logging.F("rule", res.Rule.Name))
			return
		}
	}

	go a.runMatched(ctx, ev, res)
}

func (a *App) runMatched(ctx context.Context, ev dispatch.IncomingEvent, res dispatch.MatchResult) {
	rule := res.Rule
	if ev.Channel == "" || ev.TS == "" {
		logging.Warn("matched event lacks channel/ts; skipping", logging.F("rule", rule.Name))
		return
	}
	threadTS := ev.ThreadTS
	if threadTS == "" {
		threadTS = ev.TS
	}

	waitPos, release := a.semaphore.Acquire()
	defer release()

	if waitPos > 0 {
		if _, _, err := a.api.PostMessage(ev.Channel,
			slack.MsgOptionText(fmt.Sprintf("⏸️ Queued (#%d)", waitPos), false),
			slack.MsgOptionTS(threadTS),
			slack.MsgOptionDisableMarkdown(),
		); err != nil {
			logging.Warn("queued message failed", logging.F("error", err))
		}
	}

	progress, err := StartProgress(ctx, a.api, ev.Channel, threadTS)
	if err != nil {
		logging.Error("failed to start progress message", logging.F("error", err), logging.F("rule", rule.Name))
		return
	}

	jobID := fmt.Sprintf("%s:%s:%s", ev.Channel, ev.TS, rule.Name)
	a.jobs.register(jobID, progress, nil) // exec handle is filled in below

	// Permalink is only resolved when a rule references it — avoids an extra
	// API call per event for mention flows that do not use it.
	var permalink string
	needsPermalink := false
	for _, arg := range rule.Action.Command {
		if dispatch.TemplateUsesPermalink(arg) {
			needsPermalink = true
			break
		}
	}
	if needsPermalink {
		permalink = a.resolvePermalink(ctx, ev.Channel, ev.TS)
	}

	vars := dispatch.TemplateVars{
		Permalink: permalink,
		Text:      res.Text,
		Rest:      res.Rest,
		Channel:   ev.Channel,
		User:      ev.User,
	}
	argv := make([]string, len(rule.Action.Command))
	for i, arg := range rule.Action.Command {
		argv[i] = dispatch.ExpandTemplate(arg, vars)
	}

	timeout := time.Duration(rule.Action.TimeoutMs) * time.Millisecond
	logging.Info("job start",
		logging.F("rule", rule.Name),
		logging.F("cwd", rule.Action.Cwd),
		logging.F("timeoutMs", rule.Action.TimeoutMs),
		logging.F("hasPermalink", permalink != ""),
	)

	handle := runner.Run(runner.Options{
		Command: argv,
		Cwd:     rule.Action.Cwd,
		Env:     rule.Action.Env,
		Timeout: timeout,
	})
	a.jobs.updateExec(jobID, &handle)
	defer a.jobs.unregister(jobID)

	result := <-handle.Done

	logging.Info("job done",
		logging.F("rule", rule.Name),
		logging.F("exitCode", result.ExitCode),
		logging.F("timedOut", result.TimedOut),
		logging.F("notFound", result.NotFound),
		logging.F("stdoutLen", len(result.Stdout)),
		logging.F("stderrLen", len(result.Stderr)),
	)

	switch {
	case result.TimedOut:
		_ = progress.Update("⏱️ Timed out (" + util.FormatDuration(timeout) + ")")
	case result.NotFound:
		_ = progress.Update("❌ Command not found: " + rule.Action.Command[0])
	case result.ExitCode != 0:
		_ = progress.Update(failureMessage(result))
	default:
		if err := PostCompletionReply(ctx, a.api, progress, threadTS, result.Stdout); err != nil {
			logging.Error("post completion reply failed", logging.F("error", err), logging.F("rule", rule.Name))
			_ = progress.Update("❌ Reply failed (see logs)")
		}
	}
}

func (a *App) resolvePermalink(ctx context.Context, channel, ts string) string {
	link, err := a.api.GetPermalinkContext(ctx, &slack.PermalinkParameters{Channel: channel, Ts: ts})
	if err != nil {
		logging.Warn("chat.getPermalink failed", logging.F("error", err))
		return ""
	}
	return link
}

// failureMessage builds the user-visible "❌ Failed" line. We surface a tiny
// tail of stderr (PII-redacted) so the user gets a hint without needing the
// log file.
func failureMessage(r runner.Result) string {
	tail := lastN(util.SanitizeForSlack(r.Stderr), 400)
	msg := fmt.Sprintf("❌ Failed: exit %d", r.ExitCode)
	if tail != "" {
		msg += "\n```\n" + tail + "\n```"
	}
	return msg
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

func ruleSummaries(rules []config.Rule) []map[string]any {
	out := make([]map[string]any, 0, len(rules))
	for _, r := range rules {
		var trig string
		switch r.Trigger.Type {
		case config.TriggerTypeMessage:
			trig = "message channel=" + r.Trigger.Channel
		case config.TriggerTypeAppMention:
			if r.Trigger.Keyword == nil {
				trig = "app_mention keyword=<default>"
			} else {
				trig = "app_mention keyword=" + *r.Trigger.Keyword
			}
		}
		out = append(out, map[string]any{
			"name":    r.Name,
			"trigger": trig,
			"cwd":     r.Action.Cwd,
			"command": r.Action.Command,
		})
	}
	return out
}

// fromAppMention adapts slack-go's AppMentionEvent to our dispatcher input.
func fromAppMention(e *slackevents.AppMentionEvent) dispatch.IncomingEvent {
	return dispatch.IncomingEvent{
		Type:     "app_mention",
		Channel:  e.Channel,
		User:     e.User,
		BotID:    e.BotID,
		TS:       e.TimeStamp,
		ThreadTS: e.ThreadTimeStamp,
		Text:     e.Text,
	}
}

// fromMessage adapts slack-go's MessageEvent, flattening the .message nesting
// applied to message_replied subtype.
func fromMessage(e *slackevents.MessageEvent) dispatch.IncomingEvent {
	ev := dispatch.IncomingEvent{
		Type:     "message",
		Subtype:  e.SubType,
		Channel:  e.Channel,
		User:     e.User,
		BotID:    e.BotID,
		Username: e.Username,
		TS:       e.TimeStamp,
		ThreadTS: e.ThreadTimeStamp,
		Text:     e.Text,
	}
	if e.Message != nil {
		ev.Nested = &dispatch.NestedMessage{
			Text:     e.Message.Text,
			User:     e.Message.User,
			BotID:    e.Message.BotID,
			Username: e.Message.Username,
		}
		if e.Message.BotProfile != nil {
			ev.Nested.BotProfileName = e.Message.BotProfile.Name
		}
	}
	return ev
}
