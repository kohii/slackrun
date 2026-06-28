package cli

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/slack-go/slack"
)

type fakePoster struct {
	lastChannel string
	lastOptions []slack.MsgOption
	returnTS    string
	returnErr   error
}

func (f *fakePoster) PostMessage(channelID string, opts ...slack.MsgOption) (string, string, error) {
	f.lastChannel = channelID
	f.lastOptions = opts
	if f.returnErr != nil {
		return "", "", f.returnErr
	}
	ts := f.returnTS
	if ts == "" {
		ts = "1234.5678"
	}
	return channelID, ts, nil
}

func TestRunPost_RedactsEmailInBody(t *testing.T) {
	t.Parallel()
	fake := &fakePoster{}
	var stdout, stderr bytes.Buffer
	code := runPostWith(
		[]string{"--channel", "C01", "--text", "ping alice@example.com"},
		strings.NewReader(""),
		&stdout, &stderr,
		fake,
	)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	body := readBodyOption(t, fake.lastOptions)
	if strings.Contains(body, "alice@example.com") {
		t.Fatalf("email leaked: %q", body)
	}
	if !strings.Contains(body, "[REDACTED_EMAIL]") {
		t.Fatalf("redact marker missing: %q", body)
	}
	if !strings.Contains(stdout.String(), `"ts"`) || !strings.Contains(stdout.String(), `"channel"`) {
		t.Fatalf("expected JSON {channel, ts}: %q", stdout.String())
	}
}

func TestRunPost_ReadsStdinWhenTextDashed(t *testing.T) {
	t.Parallel()
	fake := &fakePoster{}
	var stdout, stderr bytes.Buffer
	code := runPostWith(
		[]string{"--channel", "C01", "--text", "-"},
		strings.NewReader("hello from stdin"),
		&stdout, &stderr,
		fake,
	)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if got := readBodyOption(t, fake.lastOptions); got != "hello from stdin" {
		t.Fatalf("body=%q", got)
	}
}

func TestRunPost_MissingChannelIsUsageError(t *testing.T) {
	// Both flag and SLACKRUN_CHANNEL absent → usage error.
	t.Setenv("SLACKRUN_CHANNEL", "")
	os.Unsetenv("SLACKRUN_CHANNEL")

	var stdout, stderr bytes.Buffer
	code := runPostWith([]string{"--text", "hi"}, strings.NewReader(""), &stdout, &stderr, &fakePoster{})
	if code != 2 {
		t.Fatalf("expected 2, got %d", code)
	}
}

func TestRunPost_ChannelAndThreadFromEnv(t *testing.T) {
	// `slackrun start` injects these on every spawn. The CLI must accept
	// them as fallbacks when --channel / --thread-ts are omitted.
	t.Setenv("SLACKRUN_CHANNEL", "C01ENV")
	t.Setenv("SLACKRUN_THREAD_TS", "9999.0001")

	fake := &fakePoster{}
	var stdout, stderr bytes.Buffer
	code := runPostWith([]string{"--text", "hi"}, strings.NewReader(""), &stdout, &stderr, fake)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if fake.lastChannel != "C01ENV" {
		t.Fatalf("channel=%q", fake.lastChannel)
	}
	_, values, err := slack.UnsafeApplyMsgOptions("token", fake.lastChannel, "https://slack.com/api/", fake.lastOptions...)
	if err != nil {
		t.Fatal(err)
	}
	if values.Get("thread_ts") != "9999.0001" {
		t.Fatalf("thread_ts=%q", values.Get("thread_ts"))
	}
}

func TestRunPost_FlagOverridesEnv(t *testing.T) {
	t.Setenv("SLACKRUN_CHANNEL", "C01ENV")
	t.Setenv("SLACKRUN_THREAD_TS", "9999.0001")

	fake := &fakePoster{}
	var stdout, stderr bytes.Buffer
	code := runPostWith(
		[]string{"--channel", "C01FLAG", "--thread-ts", "1.1", "--text", "hi"},
		strings.NewReader(""),
		&stdout, &stderr, fake,
	)
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	if fake.lastChannel != "C01FLAG" {
		t.Fatalf("flag should win: %q", fake.lastChannel)
	}
}

func TestRunPost_MissingTextIsUsageError(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := runPostWith([]string{"--channel", "C01"}, strings.NewReader(""), &stdout, &stderr, &fakePoster{})
	if code != 2 {
		t.Fatalf("expected 2, got %d", code)
	}
}

func TestRunPost_WhitespaceOnlyBodyIsUsageError(t *testing.T) {
	t.Parallel()
	// Sanitize trims whitespace, leaving "" — the check after Sanitize must
	// catch this and return 2 (not let it through to Slack as no_text).
	var stdout, stderr bytes.Buffer
	code := runPostWith([]string{"--channel", "C01", "--text", "   "}, strings.NewReader(""), &stdout, &stderr, &fakePoster{})
	if code != 2 {
		t.Fatalf("expected 2, got %d (stderr=%q)", code, stderr.String())
	}
}

func TestRunPost_DisableMarkdownFlag(t *testing.T) {
	t.Parallel()
	fake := &fakePoster{}
	var stdout, stderr bytes.Buffer
	code := runPostWith(
		[]string{"--channel", "C01", "--text", "*bold*", "--disable-markdown"},
		strings.NewReader(""),
		&stdout, &stderr,
		fake,
	)
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	_, values, err := slack.UnsafeApplyMsgOptions("token", "C01", "https://slack.com/api/", fake.lastOptions...)
	if err != nil {
		t.Fatal(err)
	}
	if values.Get("mrkdwn") != "false" {
		t.Fatalf("mrkdwn=%q, want false", values.Get("mrkdwn"))
	}
}

func TestRunPost_APIErrorExits1(t *testing.T) {
	t.Parallel()
	fake := &fakePoster{returnErr: errors.New("rate_limited")}
	var stdout, stderr bytes.Buffer
	code := runPostWith([]string{"--channel", "C01", "--text", "hi"}, strings.NewReader(""), &stdout, &stderr, fake)
	if code != 1 {
		t.Fatalf("expected 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "rate_limited") {
		t.Fatalf("stderr missing api error: %q", stderr.String())
	}
}

func readBodyOption(t *testing.T, opts []slack.MsgOption) string {
	t.Helper()
	_, values, err := slack.UnsafeApplyMsgOptions("token", "C01", "https://slack.com/api/", opts...)
	if err != nil {
		t.Fatalf("ApplyMsgOptions: %v", err)
	}
	return values.Get("text")
}
