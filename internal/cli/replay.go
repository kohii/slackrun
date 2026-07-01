package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/kohii/slackrun/internal/config"
	"github.com/kohii/slackrun/internal/dispatch"
	"github.com/kohii/slackrun/internal/runner"
	"github.com/kohii/slackrun/internal/slackapp"
	"github.com/kohii/slackrun/internal/slackthread"
	"github.com/slack-go/slack"
)

// Replay exit codes (kept distinct so scripts can react):
//
//	0 — child exited 0 (or --dry-stdin printed successfully)
//	1 — child ran but exited non-zero, or an internal error (fetch failed, ...)
//	2 — usage error
//	3 — no rule matched (or the specified --rule didn't match)
//	4 — permalink / fetch failure (network, permission, message deleted)
const (
	replayExitOK        = 0
	replayExitChildFail = 1
	replayExitUsage     = 2
	replayExitNoMatch   = 3
	replayExitFetchFail = 4
)

const replayUsage = `usage: slackrun replay <rules.yaml> --permalink <URL> [flags]
       slackrun replay <rules.yaml> --channel <C…> --ts <TS> [flags]

Fetches a specific Slack message via the API, runs it through the same
rule-matching pipeline slackrun uses in the daemon, and (unless --dry-stdin)
spawns the matched rule's command locally. Nothing is posted to Slack by
slackrun itself; the three safety flags (--allow-slack-side-effects,
--expose-token, --real-slack-context) are opt-in for stronger fidelity.

Flags:
  --permalink <URL>            Slack permalink of the message to replay
  --channel <C…> --ts <TS>     alternative to --permalink
  --message-ts <TS>            when a thread permalink is ambiguous, force
                               which message in the thread is the trigger
  --rule <name>                restrict matching to a single rule
  --allowed-user-ids <U…,U…>   overrides ALLOWED_USER_IDS from the env
  --timeout <ms>               override the matched rule's timeout_ms
  --dry-stdin                  print the rendered stdin (and env) and exit;
                               do not spawn the child
  --print-event                print the constructed IncomingEvent as JSON
                               before matching (debug aid)
  --json                       emit report / errors on stdout as JSON
  --allow-slack-side-effects   let the child post via slackrun subcommands
                               (implied by --real-slack-context+--expose-token)
  --real-slack-context         pass the real SLACKRUN_CHANNEL / TS / THREAD_TS
                               to the child (otherwise dummy values)
  --expose-token               forward SLACK_BOT_TOKEN to the child; requires
                               --real-slack-context (dummy ctx + real token
                               would send child writes to unpredictable places)
`

// RunReplay is the entry point for the `replay` subcommand.
func RunReplay(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprint(stderr, replayUsage)
		return replayExitUsage
	}
	rulesPath := args[0]

	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	fs.SetOutput(stderr)

	permalinkFlag := fs.String("permalink", "", "Slack permalink to the message to replay")
	channelFlag := fs.String("channel", "", "channel ID (with --ts, alternative to --permalink)")
	tsFlag := fs.String("ts", "", "message ts (with --channel, alternative to --permalink)")
	messageTSFlag := fs.String("message-ts", "", "when a thread permalink is ambiguous, force the trigger's ts")
	ruleFlag := fs.String("rule", "", "restrict matching to a single named rule")
	allowedFlag := fs.String("allowed-user-ids", "", "override ALLOWED_USER_IDS from env (comma-separated)")
	timeoutFlag := fs.Int("timeout", 0, "override the matched rule's timeout_ms (0 = use rule value)")
	dryStdin := fs.Bool("dry-stdin", false, "print rendered stdin + env and exit without spawning")
	printEvent := fs.Bool("print-event", false, "print the constructed IncomingEvent as JSON")
	jsonOut := fs.Bool("json", false, "emit the report as JSON on stdout")
	allowSlack := fs.Bool("allow-slack-side-effects", false, "authorize the child to post via slackrun subcommands")
	realCtx := fs.Bool("real-slack-context", false, "pass real SLACKRUN_* env to the child")
	exposeToken := fs.Bool("expose-token", false, "forward SLACK_BOT_TOKEN to the child (requires --real-slack-context)")

	if err := fs.Parse(args[1:]); err != nil {
		return replayExitUsage
	}

	if *permalinkFlag == "" && (*channelFlag == "" || *tsFlag == "") {
		fmt.Fprintln(stderr, "either --permalink or both --channel and --ts are required")
		return replayExitUsage
	}
	if *exposeToken && !*realCtx {
		fmt.Fprintln(stderr, "--expose-token requires --real-slack-context (dummy ctx + real token is unsafe)")
		return replayExitUsage
	}

	// Load rules (with cwd fs checks — same as `start`, to catch typos early).
	loaded, err := config.LoadRulesFile(rulesPath, config.CheckOptions{})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return replayExitFetchFail
	}
	if loaded.HasErrors() {
		for _, is := range loaded.Issues {
			if is.Level == config.IssueError {
				fmt.Fprintf(stderr, "ERROR [%s]: %s\n", is.RuleName, is.Message)
			}
		}
		return replayExitUsage
	}
	rules := loaded.Rules
	if *ruleFlag != "" {
		rules = filterByName(rules, *ruleFlag)
		if len(rules) == 0 {
			fmt.Fprintf(stderr, "no rule named %q in %s\n", *ruleFlag, rulesPath)
			return replayExitUsage
		}
	}

	channel, ts, threadTS := *channelFlag, *tsFlag, ""
	if *permalinkFlag != "" {
		pc, pts, ptt, err := parsePermalink(*permalinkFlag)
		if err != nil {
			fmt.Fprintln(stderr, "parse permalink:", err)
			return replayExitUsage
		}
		channel, ts, threadTS = pc, pts, ptt
	}
	if *messageTSFlag != "" {
		ts = *messageTSFlag
	}

	tok := os.Getenv("SLACK_BOT_TOKEN")
	if tok == "" {
		fmt.Fprintln(stderr, "SLACK_BOT_TOKEN is not set; needed to fetch the message. Load ~/.config/slackrun/.env first.")
		return replayExitFetchFail
	}
	client := slack.New(tok)

	msg, err := fetchMessage(client, channel, ts, threadTS)
	if err != nil {
		fmt.Fprintln(stderr, "fetch message:", err)
		return replayExitFetchFail
	}
	ev := slackapp.IncomingEventFromMessage(msg, channel)

	if *printEvent {
		enc := json.NewEncoder(stderr)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"incoming_event": ev})
	}

	allowedIDs := envAllowedUserIDs()
	if *allowedFlag != "" {
		allowedIDs = splitCSV(*allowedFlag)
	}
	res := dispatch.Match(ev, rules, dispatch.MatcherContext{
		AllowedUserIDs: allowedIDs,
	})
	if res.Kind != dispatch.MatchKindMatched {
		report := map[string]any{
			"kind":   matchKindName(res.Kind),
			"reason": res.Reason,
		}
		emitReport(*jsonOut, stdout, stderr, report)
		return replayExitNoMatch
	}
	rule := res.Rule

	// Optionally fetch the thread if the rule needs it.
	var thread []slackthread.Message
	if needsThreadFetch(rule.Action.Stdin) {
		parentTS := ev.ThreadTS
		if parentTS == "" {
			parentTS = ev.TS
		}
		thread, err = fetchThread(client, channel, parentTS)
		if err != nil {
			fmt.Fprintln(stderr, "fetch thread:", err)
			return replayExitFetchFail
		}
	}

	permalink := ""
	if *permalinkFlag != "" {
		permalink = *permalinkFlag
	} else if usesPermalink(rule.Action.Stdin) {
		if link, err := client.GetPermalink(&slack.PermalinkParameters{Channel: channel, Ts: ts}); err == nil {
			permalink = link
		}
	}

	vars := dispatch.TemplateVars{
		Permalink: permalink,
		ChannelID: ev.Channel,
		UserID:    ev.User,
		TS:        ev.TS,
		ThreadTS:  ev.ThreadTS,
		Extract:   res.Extract,
	}
	stdinPayload := slackapp.BuildStdinPayload(slackapp.StdinBuildInput{
		Parts:  rule.Action.Stdin,
		Vars:   vars,
		Event:  ev,
		Match:  res,
		Thread: thread,
		Nonce:  "REPLAYNC",
	})

	// SLACKRUN_* env: real or dummy per --real-slack-context.
	childEnv := make(map[string]string, len(rule.Action.Env)+4)
	for k, v := range rule.Action.Env {
		childEnv[k] = v
	}
	if *realCtx {
		childEnv["SLACKRUN_CHANNEL"] = ev.Channel
		childEnv["SLACKRUN_TS"] = ev.TS
		childEnv["SLACKRUN_THREAD_TS"] = firstNonEmpty(ev.ThreadTS, ev.TS)
		childEnv["SLACKRUN_USER"] = ev.User
	} else {
		childEnv["SLACKRUN_CHANNEL"] = "C_REPLAY_DUMMY"
		childEnv["SLACKRUN_TS"] = "0.0"
		childEnv["SLACKRUN_THREAD_TS"] = "0.0"
		childEnv["SLACKRUN_USER"] = "U_REPLAY_DUMMY"
	}

	timeout := time.Duration(rule.Action.TimeoutMs) * time.Millisecond
	if *timeoutFlag > 0 {
		timeout = time.Duration(*timeoutFlag) * time.Millisecond
	}

	// One-line banner so an operator can't miss what's about to happen.
	fmt.Fprintf(stderr, "replay: rule=%s cmd=%v slack-writes=%s token=%s ctx=%s\n",
		rule.Name, rule.Action.Command,
		onOff(*allowSlack), onOff(*exposeToken), ctxLabel(*realCtx))

	if *dryStdin {
		fmt.Fprintln(stdout, "--- STDIN (bytes:", len(stdinPayload), ") ---")
		fmt.Fprintln(stdout, stdinPayload)
		fmt.Fprintln(stdout, "--- ENV (child overrides) ---")
		for k, v := range childEnv {
			fmt.Fprintf(stdout, "%s=%s\n", k, v)
		}
		return replayExitOK
	}

	handle := runner.Run(runner.Options{
		Command:          rule.Action.Command,
		Cwd:              rule.Action.Cwd,
		Env:              childEnv,
		ExposeSlackToken: *exposeToken,
		Timeout:          timeout,
		Stdin:            strings.NewReader(stdinPayload),
		Stdout:           stdout,
		Stderr:           stderr,
	})
	// Cancel on SIGINT so Ctrl+C reliably stops the child.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-ctx.Done()
		handle.Kill()
	}()
	result := <-handle.Done
	if result.StartErr != nil {
		fmt.Fprintln(stderr, "spawn:", result.StartErr)
		return replayExitChildFail
	}
	if result.TimedOut {
		fmt.Fprintln(stderr, "child timed out after", timeout)
		return replayExitChildFail
	}
	if result.ExitCode != 0 {
		return replayExitChildFail
	}
	return replayExitOK
}

// permalinkRe captures channel + digits from
// https://<workspace>.slack.com/archives/<CHAN>/p<TS_no_dot>[?...]
var permalinkRe = regexp.MustCompile(`^https?://[^/]+/archives/([^/]+)/p(\d{10})(\d{6})(?:\?.*)?$`)

// parsePermalink extracts channel, ts, and (for thread replies) the parent
// thread_ts from a Slack permalink URL. When the URL carries no thread_ts
// query, threadTS is returned empty.
func parsePermalink(raw string) (channel, ts, threadTS string, err error) {
	m := permalinkRe.FindStringSubmatch(raw)
	if m == nil {
		return "", "", "", fmt.Errorf("not a Slack permalink: %q", raw)
	}
	channel = m[1]
	ts = m[2] + "." + m[3]
	if u, perr := url.Parse(raw); perr == nil {
		if t := u.Query().Get("thread_ts"); t != "" {
			threadTS = t
		}
	}
	return channel, ts, threadTS, nil
}

// fetchMessage returns the single message identified by (channel, ts). When
// threadTS is set (permalink pointed at a reply), it searches the thread; else
// it uses conversations.history with an inclusive latest/oldest window.
func fetchMessage(client *slack.Client, channel, ts, threadTS string) (slack.Message, error) {
	if threadTS != "" {
		msgs, _, _, err := client.GetConversationReplies(&slack.GetConversationRepliesParameters{
			ChannelID: channel,
			Timestamp: threadTS,
			Limit:     200,
		})
		if err != nil {
			return slack.Message{}, err
		}
		for _, m := range msgs {
			if m.Timestamp == ts {
				return m, nil
			}
		}
		return slack.Message{}, fmt.Errorf("message ts=%s not found in thread %s", ts, threadTS)
	}
	hist, err := client.GetConversationHistory(&slack.GetConversationHistoryParameters{
		ChannelID: channel,
		Latest:    ts,
		Oldest:    ts,
		Inclusive: true,
		Limit:     1,
	})
	if err != nil {
		return slack.Message{}, err
	}
	if len(hist.Messages) == 0 {
		// Fall back to conversations.replies with ts as parent: covers the
		// "thread parent, no thread_ts in permalink" case where history's
		// oldest=latest window misses parents that only appear in the thread.
		msgs, _, _, err := client.GetConversationReplies(&slack.GetConversationRepliesParameters{
			ChannelID: channel,
			Timestamp: ts,
			Limit:     1,
		})
		if err != nil {
			return slack.Message{}, err
		}
		if len(msgs) == 0 {
			return slack.Message{}, fmt.Errorf("message ts=%s not found in channel %s", ts, channel)
		}
		return msgs[0], nil
	}
	return hist.Messages[0], nil
}

// fetchThread pulls the whole thread as slackthread.Message values, matching
// what the daemon builds from a thread part.
func fetchThread(client *slack.Client, channel, parentTS string) ([]slackthread.Message, error) {
	msgs, _, _, err := client.GetConversationReplies(&slack.GetConversationRepliesParameters{
		ChannelID: channel,
		Timestamp: parentTS,
		Limit:     200,
	})
	if err != nil {
		return nil, err
	}
	out := make([]slackthread.Message, 0, len(msgs))
	for _, m := range msgs {
		tm := slackthread.Message{TS: m.Timestamp, Text: m.Text}
		for _, f := range m.Files {
			url := f.URLPrivateDownload
			if url == "" {
				url = f.URLPrivate
			}
			tm.Files = append(tm.Files, slackthread.File{Name: f.Name, URL: url})
		}
		out = append(out, tm)
	}
	return out, nil
}

func needsThreadFetch(parts []config.StdinPart) bool {
	for _, p := range parts {
		if p.Kind == config.PartKindThread {
			return true
		}
	}
	return false
}

func usesPermalink(parts []config.StdinPart) bool {
	for _, p := range parts {
		if p.Kind == config.PartKindText && dispatch.TemplateUsesPermalink(p.Text) {
			return true
		}
	}
	return false
}

func filterByName(rules []config.Rule, name string) []config.Rule {
	for i := range rules {
		if rules[i].Name == name {
			return []config.Rule{rules[i]}
		}
	}
	return nil
}

func envAllowedUserIDs() []string {
	return splitCSV(os.Getenv("ALLOWED_USER_IDS"))
}

func onOff(b bool) string {
	if b {
		return "ON"
	}
	return "OFF"
}

func ctxLabel(real bool) string {
	if real {
		return "real"
	}
	return "dummy"
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func emitReport(jsonOut bool, stdout, stderr io.Writer, report map[string]any) {
	if jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
		return
	}
	fmt.Fprintf(stderr, "no rule matched (kind=%s reason=%s)\n", report["kind"], report["reason"])
}

