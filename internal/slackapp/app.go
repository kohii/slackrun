package slackapp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kohii/slackrun/internal/config"
	"github.com/kohii/slackrun/internal/dispatch"
	"github.com/kohii/slackrun/internal/logging"
	"github.com/kohii/slackrun/internal/runner"
	"github.com/kohii/slackrun/internal/slackthread"
	"github.com/kohii/slackrun/internal/util"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

// threadFetchTimeout bounds how long we'll wait on conversations.replies
// before falling back. The hot path doesn't tolerate slow Slack: we'd block
// progress reporting and confuse the user.
const threadFetchTimeout = 5 * time.Second

// App is the long-lived slackrun process. Construct with New, then call Run.
type App struct {
	env        config.AppEnv
	rules      []config.Rule
	api        *slack.Client
	sm         *socketmode.Client
	semaphore  *runner.Semaphore
	dedupe     *Dedupe
	selfUserID string
	selfBotID  string

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
	cancel()
	loopWG.Wait()

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
	case socketmode.EventTypeEventsAPI:
		eventsAPI, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			logging.Warn("unexpected events-api payload", logging.F("data", fmt.Sprintf("%T", evt.Data)))
			return
		}
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

	// Resolve thread fetch BEFORE posting any of our own messages
	// (⏸️ Queued, ⏳ Working) — those would otherwise show up in the
	// conversations.replies result and pollute the spawned child's context.
	var fetchedThread []slackthread.Message
	var fetchErr error
	if needsThreadFetch(rule.Action.Stdin) {
		fetchCtx, fetchCancel := context.WithTimeout(ctx, threadFetchTimeout)
		fr, err := slackthread.Fetch(fetchCtx, a.api, slackthread.FetchOptions{
			Channel:    ev.Channel,
			ThreadTS:   threadTS,
			SelfUserID: a.selfUserID,
			SelfBotID:  a.selfBotID,
		})
		fetchCancel()
		if err != nil {
			fetchErr = err
			logging.Warn("conversations.replies failed",
				logging.F("error", err),
				logging.F("rule", rule.Name),
				logging.F("channel", ev.Channel),
				logging.F("threadTs", threadTS),
			)
		} else {
			fetchedThread = fr.Messages
			if fr.HasMore {
				logging.Warn("thread fetch hit pagination cap",
					logging.F("rule", rule.Name),
					logging.F("kept", len(fr.Messages)),
				)
			}
		}
	}

	if fetchErr != nil && threadFetchPolicy(rule.Action.Stdin) == config.OnFetchErrorFail {
		if _, _, perr := a.api.PostMessage(ev.Channel,
			slack.MsgOptionText(failedFetchProgressMessage(), false),
			slack.MsgOptionTS(threadTS),
			slack.MsgOptionDisableMarkdown(),
		); perr != nil {
			logging.Error("post fetch-fail message failed",
				logging.F("error", perr), logging.F("rule", rule.Name))
		}
		return
	}

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
	a.jobs.register(jobID, progress, nil)

	var permalink string
	if needsPermalink(rule.Action.Stdin) {
		permalink = a.resolvePermalink(ctx, ev.Channel, ev.TS)
	}

	vars := dispatch.TemplateVars{
		Permalink: permalink,
		ChannelID: ev.Channel,
		UserID:    ev.User,
		TS:        ev.TS,
		ThreadTS:  ev.ThreadTS,
	}

	nonce := generateNonce()
	timeout := time.Duration(rule.Action.TimeoutMs) * time.Millisecond
	stdinPayload := buildStdinPayload(stdinBuildInput{
		Parts:      rule.Action.Stdin,
		Vars:       vars,
		Event:      ev,
		Match:      res,
		Thread:     fetchedThread,
		Nonce:      nonce,
		SelfUserID: a.selfUserID,
		SelfBotID:  a.selfBotID,
	})
	logging.Info("job start",
		logging.F("rule", rule.Name),
		logging.F("cwd", rule.Action.Cwd),
		logging.F("timeoutMs", rule.Action.TimeoutMs),
		logging.F("hasPermalink", permalink != ""),
		logging.F("threadMessages", len(fetchedThread)),
		logging.F("stdinBytes", len(stdinPayload)),
	)

	childEnv := make(map[string]string, len(rule.Action.Env)+4)
	for k, v := range rule.Action.Env {
		childEnv[k] = v
	}
	childEnv["SLACKRUN_CHANNEL"] = ev.Channel
	childEnv["SLACKRUN_TS"] = ev.TS
	childEnv["SLACKRUN_THREAD_TS"] = threadTS
	childEnv["SLACKRUN_USER"] = ev.User

	runOpts := runner.Options{
		Command:          rule.Action.Command,
		Cwd:              rule.Action.Cwd,
		Env:              childEnv,
		ExposeSlackToken: rule.Action.ExposeSlackToken,
		Timeout:          timeout,
	}
	if stdinPayload != "" {
		runOpts.Stdin = strings.NewReader(stdinPayload)
	}
	handle := runner.Run(runOpts)
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
		Type:        "app_mention",
		Channel:     e.Channel,
		User:        e.User,
		BotID:       e.BotID,
		TS:          e.TimeStamp,
		ThreadTS:    e.ThreadTimeStamp,
		Text:        e.Text,
		Attachments: convertAttachments(e.Attachments),
		Blocks:      convertBlocks(e.Blocks),
		Files:       convertFiles(e.Files),
	}
}

// fromMessage adapts slack-go's MessageEvent, flattening the .message nesting
// applied to message_replied subtype. Top-level slackevents.MessageEvent
// exposes Blocks only; Attachments and Files surface from the nested
// .message envelope (subtype-wrapped events) via slack.Msg.
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
		Blocks:   convertBlocks(e.Blocks),
	}
	if e.Message != nil {
		ev.Nested = &dispatch.NestedMessage{
			Text:        e.Message.Text,
			User:        e.Message.User,
			BotID:       e.Message.BotID,
			Username:    e.Message.Username,
			Attachments: convertAttachments(e.Message.Attachments),
			Blocks:      convertBlocks(e.Message.Blocks),
			Files:       convertFiles(e.Message.Files),
		}
		if e.Message.BotProfile != nil {
			ev.Nested.BotProfileName = e.Message.BotProfile.Name
			// AppID lives only inside BotProfile in slack-go's Msg shape.
			// Without this the matcher's `trigger.from.app_ids` never fires
			// for bot-authored messages (notably Sentry alerts).
			ev.Nested.AppID = e.Message.BotProfile.AppID
		}
	}
	return ev
}

// convertAttachments / convertBlocks / convertFiles map slack-go types to
// the package-neutral dispatch types so dispatch.MessageBody can flatten
// rich content into the rendered triggering-message body.
func convertAttachments(in []slack.Attachment) []dispatch.Attachment {
	if len(in) == 0 {
		return nil
	}
	out := make([]dispatch.Attachment, 0, len(in))
	for _, a := range in {
		out = append(out, convertAttachment(a))
	}
	return out
}

func convertAttachment(a slack.Attachment) dispatch.Attachment {
	out := dispatch.Attachment{
		Fallback: a.Fallback,
		Title:    a.Title,
		Text:     a.Text,
	}
	for _, f := range a.Fields {
		out.Fields = append(out.Fields, dispatch.AttachmentField{Title: f.Title, Value: f.Value})
	}
	return out
}

func convertBlocks(in slack.Blocks) []dispatch.Block {
	if len(in.BlockSet) == 0 {
		return nil
	}
	var out []dispatch.Block
	for _, b := range in.BlockSet {
		if d := blockToDispatch(b); d != nil {
			out = append(out, *d)
		}
	}
	return out
}

// blockToDispatch maps a slack.Block to a dispatch.Block with a best-effort
// plain-text extraction. Block types we cannot meaningfully render to text
// (image, divider, action, …) are skipped — the textual body alone is what
// gets piped to the spawned command, and rendering those would just add
// noise.
func blockToDispatch(b slack.Block) *dispatch.Block {
	switch v := b.(type) {
	case *slack.SectionBlock:
		parts := []string{}
		if v.Text != nil && v.Text.Text != "" {
			parts = append(parts, v.Text.Text)
		}
		for _, f := range v.Fields {
			if f != nil && f.Text != "" {
				parts = append(parts, f.Text)
			}
		}
		text := strings.Join(parts, "\n")
		if text == "" {
			return nil
		}
		return &dispatch.Block{Type: "section", Text: text}
	case *slack.HeaderBlock:
		if v.Text == nil || v.Text.Text == "" {
			return nil
		}
		return &dispatch.Block{Type: "header", Text: v.Text.Text}
	case *slack.ContextBlock:
		var parts []string
		for _, el := range v.ContextElements.Elements {
			if t, ok := el.(*slack.TextBlockObject); ok && t != nil && t.Text != "" {
				parts = append(parts, t.Text)
			}
		}
		if len(parts) == 0 {
			return nil
		}
		return &dispatch.Block{Type: "context", Text: strings.Join(parts, " ")}
	}
	// rich_text blocks are intentionally skipped: Slack auto-generates them
	// from the same body that lands in `text`, so flattening would
	// duplicate (and on `app_mention` rules, re-inject the keyword that
	// `command_text` mode just stripped). The text field is the source of
	// truth for human-authored content.
	return nil
}

func convertFiles(in []slack.File) []dispatch.File {
	if len(in) == 0 {
		return nil
	}
	out := make([]dispatch.File, 0, len(in))
	for _, f := range in {
		url := f.URLPrivateDownload
		if url == "" {
			url = f.URLPrivate
		}
		out = append(out, dispatch.File{Name: f.Name, URL: url})
	}
	return out
}

// needsThreadFetch reports whether any part of the rule's stdin spec needs
// the fetched thread. Only `thread:` parts require a fetch.
func needsThreadFetch(parts []config.StdinPart) bool {
	for _, p := range parts {
		if p.Kind == config.PartKindThread {
			return true
		}
	}
	return false
}

// needsPermalink reports whether any `text:` part references {{event.permalink}}.
func needsPermalink(parts []config.StdinPart) bool {
	for _, p := range parts {
		if p.Kind == config.PartKindText && dispatch.TemplateUsesPermalink(p.Text) {
			return true
		}
	}
	return false
}

// threadFetchPolicy returns the `on_fetch_error` policy for the rule's
// (at most one) thread part. Default is OnFetchErrorFail; absence of a
// thread part means no policy is needed but we still return Fail so the
// caller logic stays uniform.
func threadFetchPolicy(parts []config.StdinPart) config.OnFetchErrorPolicy {
	for _, p := range parts {
		if p.Kind == config.PartKindThread && p.Thread != nil {
			if p.Thread.OnFetchError != "" {
				return p.Thread.OnFetchError
			}
			return config.OnFetchErrorFail
		}
	}
	return config.OnFetchErrorFail
}

// stdinBuildInput packs everything buildStdinPayload needs. Wrapped in a
// struct so tests can pin individual fields without long positional argv.
type stdinBuildInput struct {
	Parts      []config.StdinPart
	Vars       dispatch.TemplateVars
	Event      dispatch.IncomingEvent
	Match      dispatch.MatchResult
	Thread     []slackthread.Message
	Nonce      string
	SelfUserID string
	SelfBotID  string
}

// buildStdinPayload concatenates the rule's stdin parts into a single byte
// stream suitable for piping to the child. Slack-derived parts that resolve
// to empty (e.g. a thread part on a standalone mention with
// IncludeTriggeringMessage:false) contribute nothing — their `heading:`
// disappears with them.
func buildStdinPayload(in stdinBuildInput) string {
	var sb strings.Builder
	for _, p := range in.Parts {
		switch p.Kind {
		case config.PartKindText:
			sb.WriteString(dispatch.ExpandTemplate(p.Text, in.Vars))

		case config.PartKindTriggerMessage:
			spec := p.TriggerMessage
			if spec == nil {
				spec = &config.TriggerMessageSpec{}
			}
			msg := buildTriggerMessage(in.Event, in.Match, spec.Content, in.SelfUserID, in.SelfBotID)
			body := slackthread.RenderTriggerMessage(msg, slackthread.RenderOptions{
				Nonce:             in.Nonce,
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
			msgs := in.Thread
			if !spec.IncludeTriggeringMessage && in.Event.TS != "" {
				msgs = excludeByTS(msgs, in.Event.TS)
			}
			body := slackthread.RenderThread(msgs, slackthread.RenderOptions{
				Nonce:             in.Nonce,
				Format:            slackthread.Format(spec.Format),
				MaxMessages:       spec.MaxMessages,
				MaxBytes:          spec.MaxBytes,
				IncludeTimestamps: spec.IncludeTimestamps,
				Files:             slackthread.FilesMode(spec.Files),
			})
			writePartWithHeading(&sb, spec.Heading, body)
		}
	}
	return sb.String()
}

// writePartWithHeading writes `heading\nbody` when body is non-empty; the
// heading vanishes alongside an empty body so an absent thread does not
// leave its label orphaned in stdin.
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

// buildTriggerMessage assembles a slackthread.Message representing the
// triggering Slack event for the trigger_message stdin part. Source is
// resolved the same way the thread fetcher does it so the speaker tag is
// consistent across parts.
func buildTriggerMessage(ev dispatch.IncomingEvent, res dispatch.MatchResult, mode config.ContentMode, selfUserID, selfBotID string) slackthread.Message {
	user := ev.User
	botID := ev.BotID
	if ev.Nested != nil {
		if user == "" {
			user = ev.Nested.User
		}
		if botID == "" {
			botID = ev.Nested.BotID
		}
	}

	msg := slackthread.Message{
		TS:   ev.TS,
		Text: dispatch.MessageBody(ev, res, mode),
	}
	for _, f := range dispatch.ExtractFiles(ev) {
		msg.Files = append(msg.Files, slackthread.File{Name: f.Name, URL: f.URL})
	}

	switch {
	case (selfUserID != "" && user == selfUserID) || (selfBotID != "" && botID == selfBotID):
		msg.Source = slackthread.SourceSelf
	case user != "":
		msg.Source = slackthread.SourceUser
		msg.User = user
	default:
		msg.Source = slackthread.SourceBot
		name := firstNonEmpty(ev.Username, ev.BotProfileName)
		if name == "" && ev.Nested != nil {
			name = firstNonEmpty(ev.Nested.Username, ev.Nested.BotProfileName, ev.Nested.AppID)
		}
		if name == "" {
			name = botID
		}
		msg.Bot = name
	}
	return msg
}

// excludeByTS returns msgs with any element whose TS equals ts removed.
// Returns msgs unchanged when ts is empty or no match exists.
func excludeByTS(msgs []slackthread.Message, ts string) []slackthread.Message {
	if ts == "" {
		return msgs
	}
	hit := -1
	for i, m := range msgs {
		if m.TS == ts {
			hit = i
			break
		}
	}
	if hit < 0 {
		return msgs
	}
	out := make([]slackthread.Message, 0, len(msgs)-1)
	out = append(out, msgs[:hit]...)
	out = append(out, msgs[hit+1:]...)
	return out
}

func failedFetchProgressMessage() string {
	return "❌ Thread fetch failed (see logs)"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// generateNonce returns 8 hex chars (4 random bytes). Used as the suffix on
// <UNTRUSTED_SLACK_*> tags so a Slack body cannot forge a closing tag.
func generateNonce() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is exotic enough that we'd rather emit a
		// constant suffix than panic the spawn. The marker still differs
		// from the bare base tag name.
		return "fallback"
	}
	return hex.EncodeToString(b)
}
