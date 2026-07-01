package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/slack-go/slack"
)

// --- update ---

type fakeUpdater struct {
	err      error
	gotChan  string
	gotTS    string
	gotOpts  int
	respTS   string
	respChan string
}

func (f *fakeUpdater) UpdateMessage(channelID, timestamp string, options ...slack.MsgOption) (string, string, string, error) {
	f.gotChan = channelID
	f.gotTS = timestamp
	f.gotOpts = len(options)
	if f.respChan == "" {
		f.respChan = channelID
	}
	if f.respTS == "" {
		f.respTS = timestamp
	}
	return f.respChan, f.respTS, "", f.err
}

func TestRunUpdate_Happy(t *testing.T) {
	t.Parallel()
	fake := &fakeUpdater{}
	var out, errBuf bytes.Buffer
	code := runUpdateWith(
		[]string{"--channel", "C1", "--ts", "9.9", "--text", "revised"},
		strings.NewReader(""), &out, &errBuf, fake,
	)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errBuf.String())
	}
	if fake.gotChan != "C1" || fake.gotTS != "9.9" {
		t.Fatalf("params not forwarded: %+v", fake)
	}
	if !strings.Contains(out.String(), `"ts":"9.9"`) {
		t.Errorf("body missing echoed ts: %s", out.String())
	}
}

func TestRunUpdate_MissingTS_Usage(t *testing.T) {
	t.Parallel()
	var out, errBuf bytes.Buffer
	code := runUpdateWith(
		[]string{"--channel", "C1", "--text", "x"},
		strings.NewReader(""), &out, &errBuf, &fakeUpdater{},
	)
	if code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
	if !strings.Contains(errBuf.String(), "--ts") {
		t.Errorf("stderr missing --ts hint: %q", errBuf.String())
	}
}

func TestRunUpdate_ReadsBodyFromStdin(t *testing.T) {
	t.Parallel()
	fake := &fakeUpdater{}
	var out, errBuf bytes.Buffer
	code := runUpdateWith(
		[]string{"--channel", "C1", "--ts", "1.0", "--text", "-"},
		strings.NewReader("piped body"), &out, &errBuf, fake,
	)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errBuf.String())
	}
	if fake.gotOpts < 1 {
		t.Fatal("expected at least the text MsgOption to be forwarded")
	}
}

// --- ephemeral ---

type fakeEphemeral struct {
	err      error
	gotChan  string
	gotUser  string
	respTS   string
	numOpts  int
}

func (f *fakeEphemeral) PostEphemeral(channelID, userID string, options ...slack.MsgOption) (string, error) {
	f.gotChan = channelID
	f.gotUser = userID
	f.numOpts = len(options)
	if f.respTS == "" {
		f.respTS = "1000.1"
	}
	return f.respTS, f.err
}

func TestRunEphemeral_DefaultsUserFromEnv(t *testing.T) {
	t.Setenv("SLACKRUN_CHANNEL", "C_ENV")
	t.Setenv("SLACKRUN_USER", "U_ENV")
	fake := &fakeEphemeral{}
	var out, errBuf bytes.Buffer
	code := runEphemeralWith(
		[]string{"--text", "hi"},
		strings.NewReader(""), &out, &errBuf, fake,
	)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errBuf.String())
	}
	if fake.gotChan != "C_ENV" || fake.gotUser != "U_ENV" {
		t.Fatalf("env fallback failed: %+v", fake)
	}
	var body map[string]any
	_ = json.Unmarshal(out.Bytes(), &body)
	if body["message_ts"] == "" {
		t.Errorf("expected message_ts in body: %v", body)
	}
}

// --- unreact ---

type fakeUnreacter struct {
	err     error
	gotName string
	gotItem slack.ItemRef
}

func (f *fakeUnreacter) RemoveReaction(name string, item slack.ItemRef) error {
	f.gotName = name
	f.gotItem = item
	return f.err
}

func TestRunUnreact_StripsColonsFromEmoji(t *testing.T) {
	t.Parallel()
	fake := &fakeUnreacter{}
	var out, errBuf bytes.Buffer
	code := runUnreactWith(
		[]string{"--channel", "C1", "--ts", "1.0", "--emoji", ":eyes:"},
		&out, &errBuf, fake,
	)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errBuf.String())
	}
	if fake.gotName != "eyes" {
		t.Fatalf("emoji not stripped: %q", fake.gotName)
	}
}

func TestRunUnreact_APIError_ExitOne(t *testing.T) {
	t.Parallel()
	fake := &fakeUnreacter{err: errors.New("no_reaction")}
	var out, errBuf bytes.Buffer
	code := runUnreactWith(
		[]string{"--channel", "C1", "--ts", "1.0", "--emoji", "eyes"},
		&out, &errBuf, fake,
	)
	if code != 1 {
		t.Fatalf("exit=%d, want 1", code)
	}
}

// --- me ---

type fakeAuth struct {
	resp *slack.AuthTestResponse
	err  error
}

func (f *fakeAuth) AuthTest() (*slack.AuthTestResponse, error) {
	return f.resp, f.err
}

func TestRunMe_HappyPrintsIdentity(t *testing.T) {
	t.Parallel()
	fake := &fakeAuth{resp: &slack.AuthTestResponse{
		UserID: "U_BOT", BotID: "B_BOT", Team: "acme",
	}}
	var out, errBuf bytes.Buffer
	code := runMeWith(nil, &out, &errBuf, fake)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errBuf.String())
	}
	for _, want := range []string{`"user_id":"U_BOT"`, `"bot_id":"B_BOT"`, `"team":"acme"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("body missing %q: %s", want, out.String())
		}
	}
}

func TestRunMe_RejectsExtraArgs(t *testing.T) {
	t.Parallel()
	var out, errBuf bytes.Buffer
	code := runMeWith([]string{"--extra"}, &out, &errBuf, &fakeAuth{})
	if code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
}
