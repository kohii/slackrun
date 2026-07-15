package slackapp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kohii/slackrun/internal/clidoc"
	"github.com/kohii/slackrun/internal/config"
	"github.com/kohii/slackrun/internal/dispatch"
	"github.com/kohii/slackrun/internal/logging"
	"github.com/kohii/slackrun/internal/runmgr"
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
	env            config.AppEnv
	rules          []config.Rule
	allowedUserIDs []string
	api            *slack.Client
	sm             *socketmode.Client
	semaphore      *runner.Semaphore
	dedupe         *Dedupe
	selfUserID     string
	selfBotID      string

	// backfillers is keyed by channel and is nil when BACKFILL_INTERVAL_MS=0.
	// Each poller acts as a backstop for Socket Mode losses (silent dead
	// connections, dropped envelopes); Dedupe absorbs the overlap.
	backfillers map[string]*backfiller

	runs *runmgr.Manager
}

// Runs exposes the run manager so the admin HTTP layer (internal/adminapi)
// and CLI wiring can share the same in-flight registry as the dispatcher.
func (a *App) Runs() *runmgr.Manager { return a.runs }

// Options configures a new App. BootTime defaults to time.Now if zero.
type Options struct {
	Env            config.AppEnv
	Rules          []config.Rule
	AllowedUserIDs []string
	BootTime       time.Time
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
		env:            opts.Env,
		rules:          opts.Rules,
		allowedUserIDs: opts.AllowedUserIDs,
		api:            api,
		sm:             sm,
		semaphore:      sem,
		dedupe:         d,
		selfUserID:     authResp.UserID,
		selfBotID:      authResp.BotID,
		runs:           runmgr.New(),
	}
	if opts.Env.BackfillIntervalMs > 0 {
		interval := time.Duration(opts.Env.BackfillIntervalMs) * time.Millisecond
		lookback := time.Duration(opts.Env.BackfillLookbackMs) * time.Millisecond
		channels := uniqueMessageChannels(opts.Rules)
		if len(channels) > 0 {
			app.backfillers = make(map[string]*backfiller, len(channels))
			for _, ch := range channels {
				app.backfillers[ch] = newBackfiller(ch, interval, lookback, api, app.dispatchBackfillMessage)
			}
		}
	}
	logging.Info("bot ready",
		logging.F("team", authResp.Team),
		logging.F("user", authResp.User),
		logging.F("selfUserId", authResp.UserID),
		logging.F("selfBotId", authResp.BotID),
		logging.F("allowedUserIds", opts.AllowedUserIDs),
		logging.F("rules", ruleSummaries(opts.Rules)),
	)
	return app, nil
}

// AdminServer is the tiny surface App.Run needs from an admin listener.
// Kept as an interface so the admin API can live in its own package
// (internal/adminapi) without introducing an import cycle with slackapp.
type AdminServer interface {
	Start() error
	Stop(ctx context.Context)
}

// Run drives the Socket Mode connection and event loop. Blocks until ctx is
// cancelled or the underlying connection fails. In-flight jobs receive a kill
// and their progress messages are overwritten with "⚠️ Bot stopped" before
// Run returns. Pass a non-nil admin to expose the runs/kill HTTP surface.
func (a *App) Run(ctx context.Context, admin AdminServer) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	if admin != nil {
		if err := admin.Start(); err != nil {
			// Admin listener failure is logged but not fatal — the
			// dispatcher itself is unaffected.
			logging.Warn("admin api start failed", logging.F("error", err))
		}
	}

	var loopWG sync.WaitGroup
	loopWG.Add(1)
	go func() {
		defer loopWG.Done()
		a.eventLoop(runCtx)
	}()

	for ch, b := range a.backfillers {
		loopWG.Add(1)
		ch, b := ch, b
		go func() {
			defer loopWG.Done()
			logging.Info("backfill started",
				logging.F("channel", ch),
				logging.F("intervalMs", b.interval.Milliseconds()),
				logging.F("lookbackMs", b.lookback.Milliseconds()))
			b.Run(runCtx)
		}()
	}

	runErr := a.sm.RunContext(runCtx)
	cancel()
	loopWG.Wait()

	// Ordering matters: stop accepting new admin calls before we start
	// tearing down live runs, otherwise an admin kill mid-shutdown races
	// with Manager.Shutdown for the entry's cause. 2s is generous — the
	// only handlers are in-memory reads and a Slack chat.update.
	if admin != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		admin.Stop(stopCtx)
		stopCancel()
	}
	a.runs.Shutdown(context.Background(), "⚠️ Bot stopped", 7*time.Second)
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

// incomingSource distinguishes a live Socket Mode delivery from a catchup
// dispatch (conversations.history poller). Only the live path enforces the
// boot-time TooOld cutoff; backfill is by definition our willingness to
// process older events.
type incomingSource int

const (
	sourceLive incomingSource = iota
	sourceBackfill
)

func (a *App) handleEventsAPI(ctx context.Context, e slackevents.EventsAPIEvent) {
	switch inner := e.InnerEvent.Data.(type) {
	case *slackevents.AppMentionEvent:
		a.handleIncoming(ctx, fromAppMention(inner), sourceLive)
	case *slackevents.MessageEvent:
		ev := fromMessage(inner)
		if b := a.backfillers[ev.Channel]; b != nil {
			b.Observe(ev.TS)
		}
		a.handleIncoming(ctx, ev, sourceLive)
	default:
		logging.Debug("unhandled inner event", logging.F("type", e.InnerEvent.Type))
	}
}

// dispatchBackfillMessage feeds one history-fetched message through the same
// pipeline as a live Socket Mode delivery. Dedupe rejects anything Socket
// Mode already handled, so double-delivery collapses to one run.
func (a *App) dispatchBackfillMessage(ctx context.Context, m slack.Message, channel string) {
	a.handleIncoming(ctx, IncomingEventFromMessage(m, channel), sourceBackfill)
}

func (a *App) handleIncoming(ctx context.Context, ev dispatch.IncomingEvent, source incomingSource) {
	res := dispatch.Match(ev, a.rules, dispatch.MatcherContext{
		SelfUserID:     a.selfUserID,
		SelfBotID:      a.selfBotID,
		AllowedUserIDs: a.allowedUserIDs,
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
		var decision DedupeDecision
		if source == sourceBackfill {
			decision = a.dedupe.DecideCatchup(ev.Channel, ev.TS)
		} else {
			decision = a.dedupe.Decide(ev.Channel, ev.TS)
		}
		switch decision {
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

	var progress ProgressHandle
	var err error
	if rule.Action.ProgressStyleResolved() == config.ProgressStyleAssistantStatus {
		progress, err = StartAssistantStatusProgress(ctx, a.api, a.api, ev.Channel, threadTS)
	} else {
		progress, err = StartMessageProgress(ctx, a.api, ev.Channel, threadTS)
	}
	if err != nil {
		logging.Error("failed to start progress indicator", logging.F("error", err), logging.F("rule", rule.Name))
		return
	}

	fullID := fmt.Sprintf("%s:%s:%s", ev.Channel, ev.TS, rule.Name)
	runID, err := a.runs.Register(runmgr.Meta{
		FullID:    fullID,
		RuleName:  rule.Name,
		ChannelID: ev.Channel,
		UserID:    ev.User,
		ThreadTS:  threadTS,
		StartedAt: time.Now(),
	}, progress)
	if err != nil {
		logging.Error("run register failed", logging.F("error", err), logging.F("rule", rule.Name))
		_ = progress.Update("❌ Internal error (see logs)")
		return
	}

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
		Extract:   res.Extract,
	}

	nonce := generateNonce()
	timeout := time.Duration(rule.Action.TimeoutMs) * time.Millisecond
	stdinPayload := BuildStdinPayload(StdinBuildInput{
		Parts:                 rule.Action.Stdin,
		Vars:                  vars,
		Event:                 ev,
		Match:                 res,
		Thread:                fetchedThread,
		Nonce:                 nonce,
		SelfUserID:            a.selfUserID,
		SelfBotID:             a.selfBotID,
		TriggerMessageTrusted: TriggerMessageTrusted(rule.Trigger, a.allowedUserIDs),
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
	if err := a.runs.AttachHandle(runID, runnerHandleAdapter{h: handle}); err != nil {
		if errors.Is(err, runmgr.ErrCancelled) {
			// Shutdown flipped this run to Cancelled while we were still
			// preparing (permalink lookup / stdin build). We already spawned
			// — kill the orphan immediately so Shutdown's Wait actually
			// converges instead of stalling on a live child.
			logging.Info("run cancelled during preparation; killing orphan child",
				logging.F("rule", rule.Name), logging.F("runId", runID))
			handle.Kill()
		} else {
			logging.Warn("run attach handle failed", logging.F("error", err), logging.F("rule", rule.Name))
		}
	}

	result := <-handle.Done

	finalCause, killReason := a.runs.Complete(runID, resultToCause(result), result.ExitCode)

	logging.Info("job done",
		logging.F("rule", rule.Name),
		logging.F("runId", runID),
		logging.F("exitCode", result.ExitCode),
		logging.F("cause", finalCause.String()),
		logging.F("stdoutLen", len(result.Stdout)),
		logging.F("stderrLen", len(result.Stderr)),
	)

	switch decideCompletion(finalCause, result.ExitCode, rule.Action.ReplyWithStdoutEnabled()) {
	case completionSkip:
		// Kill / Shutdown paths posted the terminal message themselves;
		// nothing more to do here. Any leftover ⏳ ticker has already been
		// halted by progress.Update's sync.Once.
		_ = killReason
	case completionTimeout:
		_ = progress.Update("⏱️ Timed out (" + util.FormatDuration(timeout) + ")")
	case completionNotFound:
		_ = progress.Update("❌ Command not found: " + rule.Action.Command[0])
	case completionFailed:
		_ = progress.Update(failureMessage(result))
	case completionMarkDone:
		// The child has already posted its own replies (or chose to stay
		// silent). Settle the progress indicator so no `⏳ Working…` is
		// left orphaned — the assistant_status backend does this without
		// posting anything new.
		_ = progress.Done()
	case completionPostStdout:
		if err := PostCompletionReply(ctx, a.api, progress, threadTS, result.Stdout); err != nil {
			logging.Error("post completion reply failed", logging.F("error", err), logging.F("rule", rule.Name))
			_ = progress.Update("❌ Reply failed (see logs)")
		}
	}
}

// completionAction names what runMatched does with the progress message
// when the child exits. Pure decision; the side effects (Slack write,
// stdout posting) live in runMatched itself.
type completionAction int

const (
	completionPostStdout completionAction = iota
	completionMarkDone
	completionTimeout
	completionNotFound
	completionFailed
	// completionSkip: another path (admin kill, shutdown) has already
	// written the terminal progress message. Do nothing.
	completionSkip
)

func decideCompletion(cause runmgr.ExitCause, exitCode int, replyWithStdout bool) completionAction {
	switch cause {
	case runmgr.CauseKilled, runmgr.CauseShutdown:
		return completionSkip
	case runmgr.CauseTimedOut:
		return completionTimeout
	case runmgr.CauseNotFound:
		return completionNotFound
	case runmgr.CauseStartError:
		return completionFailed
	}
	if exitCode != 0 {
		return completionFailed
	}
	if !replyWithStdout {
		return completionMarkDone
	}
	return completionPostStdout
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

// IncomingEventFromMessage adapts a plain slack.Message (as returned by
// conversations.history / conversations.replies) to our dispatcher input.
// Used by tools that construct an event from an API fetch rather than from a
// Socket Mode envelope (e.g. `slackrun replay`). Fields mirror what fromMessage
// extracts from the socket-mode wrapper.
func IncomingEventFromMessage(m slack.Message, channel string) dispatch.IncomingEvent {
	ev := dispatch.IncomingEvent{
		Type:        "message",
		Subtype:     m.SubType,
		Channel:     channel,
		User:        m.User,
		BotID:       m.BotID,
		Username:    m.Username,
		TS:          m.Timestamp,
		ThreadTS:    m.ThreadTimestamp,
		Text:        m.Text,
		Attachments: convertAttachments(m.Attachments),
		Blocks:      convertBlocks(m.Blocks),
		Files:       convertFiles(m.Files),
	}
	if m.BotProfile != nil {
		ev.BotProfileName = m.BotProfile.Name
		if ev.AppID == "" {
			ev.AppID = m.BotProfile.AppID
		}
	}
	// Bot posts from conversations.history sometimes arrive with subtype
	// empty even though they carry BotID — the socket-mode path sees
	// subtype=bot_message. Normalize so matchMessage's allow-list still hits.
	if ev.Subtype == "" && (ev.BotID != "" || ev.BotProfileName != "") {
		ev.Subtype = "bot_message"
	}
	return ev
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

// TriggerMessageTrusted reports whether the trigger sender is authorized at
// the rule level, and therefore whether the `trigger_message:` part should
// be rendered inside <slack_message_…> instead of the untrusted wrapper.
//
// Only `app_mention` triggers with a populated top-level allowed_user_ids
// list qualify. Two rules apply:
//
//   - allowed_user_ids gates `app_mention` senders. schema validation
//     requires it to be non-empty whenever any `app_mention` rule exists,
//     and dispatch drops mentions from unlisted users before this function
//     is called. So on the runtime path, len(allowedUserIDs) > 0 is
//     effectively guaranteed for `app_mention` — the check here is
//     defense-in-depth, and also lets replay/dryrun call sites reuse the
//     helper without repeating the invariant.
//   - `type: message` is never trusted, even when narrowed by
//     `trigger.from.user_ids`. Message events flow from a mix of humans,
//     integrations, and webhooks whose identity we do not verify the same
//     way `app_mention` does; the conservative default is to keep them
//     wrapped as untrusted data. If you want a `type: message` sender to
//     drive prompts as trusted context, route it through an `app_mention`
//     rule instead.
func TriggerMessageTrusted(trig config.Trigger, allowedUserIDs []string) bool {
	return trig.Type == config.TriggerTypeAppMention && len(allowedUserIDs) > 0
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

// StdinBuildInput packs everything BuildStdinPayload needs. Wrapped in a
// struct so tests can pin individual fields without long positional argv.
type StdinBuildInput struct {
	Parts      []config.StdinPart
	Vars       dispatch.TemplateVars
	Event      dispatch.IncomingEvent
	Match      dispatch.MatchResult
	Thread     []slackthread.Message
	Nonce      string
	SelfUserID string
	SelfBotID  string
	// TriggerMessageTrusted flags whether the triggering sender is authorized
	// at the rule level. When true, the `trigger_message:` part is emitted
	// inside <slack_message_…> (no untrusted marker); when false the
	// <untrusted_slack_message_… note="…"> wrapper is used instead. Threads
	// are unconditionally untrusted and ignore this field. Callers compute
	// the flag at the trust boundary (`app_mention` gated by a non-empty
	// allowed_user_ids) so `renderStdinPart` stays policy-free.
	TriggerMessageTrusted bool
}

// BuildStdinPayload concatenates the rule's stdin parts into a single byte
// stream suitable for piping to the child. Slack-derived parts that resolve
// to empty (e.g. a thread part on a standalone mention with
// IncludeTriggeringMessage:false) contribute nothing — their `heading:`
// disappears with them. A single '\n' is inserted between consecutive
// non-empty parts when the previous part's output does not already end in
// one, so an inline `text: "hi"` does not butt up against the next part.
func BuildStdinPayload(in StdinBuildInput) string {
	var sb strings.Builder
	for _, p := range in.Parts {
		chunk := renderStdinPart(p, in)
		appendPart(&sb, chunk)
	}
	return sb.String()
}

func renderStdinPart(p config.StdinPart, in StdinBuildInput) string {
	switch p.Kind {
	case config.PartKindText:
		return dispatch.ExpandTemplate(p.Text, in.Vars)

	case config.PartKindTriggerMessage:
		spec := p.TriggerMessage
		if spec == nil {
			spec = &config.TriggerMessageSpec{}
		}
		msg := buildTriggerMessage(in.Event, in.Match, spec.Content, in.SelfUserID, in.SelfBotID)
		body := slackthread.RenderTriggerMessage(msg, slackthread.TriggerRenderOptions{
			Nonce:             in.Nonce,
			IncludeTimestamps: spec.IncludeTimestamps,
			Files:             slackthread.FilesMode(spec.Files),
			Trusted:           in.TriggerMessageTrusted,
		})
		return renderPartWithHeading(spec.Heading, body)

	case config.PartKindThread:
		spec := p.Thread
		if spec == nil {
			spec = &config.ThreadSpec{}
		}
		msgs := in.Thread
		if !spec.IncludeTriggeringMessage && in.Event.TS != "" {
			msgs = excludeByTS(msgs, in.Event.TS)
		}
		body := slackthread.RenderThread(msgs, slackthread.ThreadRenderOptions{
			Nonce:             in.Nonce,
			Format:            slackthread.Format(spec.Format),
			MaxMessages:       spec.MaxMessages,
			MaxBytes:          spec.MaxBytes,
			IncludeTimestamps: spec.IncludeTimestamps,
			Files:             slackthread.FilesMode(spec.Files),
		})
		return renderPartWithHeading(spec.Heading, body)

	case config.PartKindSlackrunHelp:
		return clidoc.ChildUsage
	}
	return ""
}

// renderPartWithHeading returns `heading\nbody` when body is non-empty; the
// heading vanishes alongside an empty body so an absent thread does not
// leave its label orphaned in stdin.
func renderPartWithHeading(heading, body string) string {
	if body == "" {
		return ""
	}
	if heading == "" {
		return body
	}
	var sb strings.Builder
	sb.WriteString(heading)
	if !strings.HasSuffix(heading, "\n") {
		sb.WriteByte('\n')
	}
	sb.WriteString(body)
	return sb.String()
}

// appendPart writes chunk into sb, ensuring at least one '\n' sits between
// this chunk and whatever was already there. Empty chunks are skipped so
// elided parts contribute nothing (no stray separator).
func appendPart(sb *strings.Builder, chunk string) {
	if chunk == "" {
		return
	}
	if sb.Len() > 0 {
		s := sb.String()
		if s[len(s)-1] != '\n' {
			sb.WriteByte('\n')
		}
	}
	sb.WriteString(chunk)
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
// the slack wrapper tags so a Slack body cannot forge a closing tag.
//
// crypto/rand.Read essentially never fails in practice, but if it does we
// still need *some* per-spawn variation so a Slack body cannot preload a
// known suffix. Falling back to nanoseconds + PID mixed with a fixed
// constant gives dynamic (though non-crypto) bytes without panicking the
// spawn.
func generateNonce() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err == nil {
		return hex.EncodeToString(b)
	}
	h := uint64(time.Now().UnixNano())
	h ^= h >> 32
	h ^= uint64(os.Getpid()) * 0x9e3779b97f4a7c15
	b[0], b[1], b[2], b[3] = byte(h>>24), byte(h>>16), byte(h>>8), byte(h)
	return hex.EncodeToString(b)
}
