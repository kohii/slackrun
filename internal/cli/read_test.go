package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/slack-go/slack"
)

// --- history ---

type fakeHistoryClient struct {
	resp *slack.GetConversationHistoryResponse
	err  error
	got  *slack.GetConversationHistoryParameters
}

func (f *fakeHistoryClient) GetConversationHistory(p *slack.GetConversationHistoryParameters) (*slack.GetConversationHistoryResponse, error) {
	f.got = p
	return f.resp, f.err
}

func TestRunHistory_Happy(t *testing.T) {
	t.Parallel()
	fake := &fakeHistoryClient{
		resp: &slack.GetConversationHistoryResponse{
			HasMore: true,
			Messages: []slack.Message{
				{Msg: slack.Msg{Timestamp: "1", Text: "hi"}},
			},
			ResponseMetaData: struct {
				NextCursor string `json:"next_cursor"`
			}{NextCursor: "abc"},
		},
	}
	var out, errBuf bytes.Buffer
	code := runHistoryWith([]string{"--channel", "C01", "--limit", "50"}, &out, &errBuf, fake)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errBuf.String())
	}
	if fake.got == nil || fake.got.ChannelID != "C01" || fake.got.Limit != 50 {
		t.Fatalf("params not forwarded: %+v", fake.got)
	}
	var body map[string]any
	if err := json.Unmarshal(out.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, out.String())
	}
	if body["has_more"] != true || body["next_cursor"] != "abc" {
		t.Errorf("unexpected body: %v", body)
	}
}

func TestRunHistory_MissingChannel_Usage(t *testing.T) {
	t.Setenv("SLACKRUN_CHANNEL", "")
	var out, errBuf bytes.Buffer
	code := runHistoryWith(nil, &out, &errBuf, &fakeHistoryClient{})
	if code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
	if !strings.Contains(errBuf.String(), "--channel") {
		t.Errorf("stderr missing --channel hint: %q", errBuf.String())
	}
}

func TestRunHistory_DefaultsChannelFromEnv(t *testing.T) {
	t.Setenv("SLACKRUN_CHANNEL", "C_ENV")
	fake := &fakeHistoryClient{resp: &slack.GetConversationHistoryResponse{}}
	var out, errBuf bytes.Buffer
	code := runHistoryWith(nil, &out, &errBuf, fake)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errBuf.String())
	}
	if fake.got.ChannelID != "C_ENV" {
		t.Fatalf("channel not defaulted from env: %+v", fake.got)
	}
}

// --- replies ---

type fakeRepliesClient struct {
	msgs    []slack.Message
	hasMore bool
	cursor  string
	err     error
	got     *slack.GetConversationRepliesParameters
}

func (f *fakeRepliesClient) GetConversationReplies(p *slack.GetConversationRepliesParameters) ([]slack.Message, bool, string, error) {
	f.got = p
	return f.msgs, f.hasMore, f.cursor, f.err
}

func TestRunReplies_Happy(t *testing.T) {
	t.Parallel()
	fake := &fakeRepliesClient{
		msgs:    []slack.Message{{Msg: slack.Msg{Timestamp: "1.0"}}, {Msg: slack.Msg{Timestamp: "1.1"}}},
		hasMore: false,
		cursor:  "",
	}
	var out, errBuf bytes.Buffer
	code := runRepliesWith([]string{"--channel", "C1", "--thread-ts", "1.0"}, &out, &errBuf, fake)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errBuf.String())
	}
	if fake.got.ChannelID != "C1" || fake.got.Timestamp != "1.0" {
		t.Fatalf("params not forwarded: %+v", fake.got)
	}
}

func TestRunReplies_MissingThreadTS_Usage(t *testing.T) {
	t.Setenv("SLACKRUN_CHANNEL", "C1")
	t.Setenv("SLACKRUN_THREAD_TS", "")
	var out, errBuf bytes.Buffer
	code := runRepliesWith(nil, &out, &errBuf, &fakeRepliesClient{})
	if code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
	if !strings.Contains(errBuf.String(), "--thread-ts") {
		t.Errorf("stderr missing --thread-ts hint: %q", errBuf.String())
	}
}

// --- reactions ---

type fakeReactionsClient struct {
	item slack.ReactedItem
	err  error
	gotI slack.ItemRef
	gotP slack.GetReactionsParameters
}

func (f *fakeReactionsClient) GetReactions(item slack.ItemRef, params slack.GetReactionsParameters) (slack.ReactedItem, error) {
	f.gotI = item
	f.gotP = params
	return f.item, f.err
}

func TestRunReactions_Happy(t *testing.T) {
	t.Parallel()
	fake := &fakeReactionsClient{
		item: slack.ReactedItem{
			Reactions: []slack.ItemReaction{
				{Name: "eyes", Count: 2, Users: []string{"U1", "U2"}},
			},
		},
	}
	var out, errBuf bytes.Buffer
	code := runReactionsWith([]string{"--channel", "C1", "--ts", "1.0", "--full"}, &out, &errBuf, fake)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errBuf.String())
	}
	if fake.gotI.Channel != "C1" || fake.gotI.Timestamp != "1.0" || !fake.gotP.Full {
		t.Fatalf("args not forwarded: %+v %+v", fake.gotI, fake.gotP)
	}
	if !strings.Contains(out.String(), `"name":"eyes"`) {
		t.Errorf("expected eyes reaction in body: %s", out.String())
	}
}

func TestRunReactions_APIError_ExitOne(t *testing.T) {
	t.Parallel()
	fake := &fakeReactionsClient{err: errors.New("boom")}
	var out, errBuf bytes.Buffer
	code := runReactionsWith([]string{"--channel", "C1", "--ts", "1.0"}, &out, &errBuf, fake)
	if code != 1 {
		t.Fatalf("exit=%d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "boom") {
		t.Errorf("stderr missing underlying error: %q", errBuf.String())
	}
}

// --- user ---

type fakeUserClient struct {
	user *slack.User
	err  error
	got  string
}

func (f *fakeUserClient) GetUserInfo(user string) (*slack.User, error) {
	f.got = user
	return f.user, f.err
}

func TestRunUser_DefaultsFromEnv(t *testing.T) {
	t.Setenv("SLACKRUN_USER", "U_ENV")
	fake := &fakeUserClient{user: &slack.User{ID: "U_ENV", Name: "kohei"}}
	var out, errBuf bytes.Buffer
	code := runUserWith(nil, &out, &errBuf, fake)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errBuf.String())
	}
	if fake.got != "U_ENV" {
		t.Fatalf("user not defaulted: %q", fake.got)
	}
	if !strings.Contains(out.String(), `"id":"U_ENV"`) {
		t.Errorf("body missing id: %s", out.String())
	}
}

func TestRunUser_MissingUser_Usage(t *testing.T) {
	t.Setenv("SLACKRUN_USER", "")
	var out, errBuf bytes.Buffer
	code := runUserWith(nil, &out, &errBuf, &fakeUserClient{})
	if code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
}

// --- usergroups ---

type fakeUsergroupsClient struct {
	groups []slack.UserGroup
	err    error
	gotN   int
}

func (f *fakeUsergroupsClient) GetUserGroups(options ...slack.GetUserGroupsOption) ([]slack.UserGroup, error) {
	f.gotN = len(options)
	return f.groups, f.err
}

func TestRunUsergroups_Happy(t *testing.T) {
	t.Parallel()
	fake := &fakeUsergroupsClient{
		groups: []slack.UserGroup{{ID: "S1", Name: "eng"}},
	}
	var out, errBuf bytes.Buffer
	code := runUsergroupsWith(nil, &out, &errBuf, fake)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), `"id":"S1"`) {
		t.Errorf("body missing group id: %s", out.String())
	}
	if fake.gotN != 0 {
		t.Errorf("expected 0 options when no flags set, got %d", fake.gotN)
	}
}

func TestRunUsergroups_ForwardsOptions(t *testing.T) {
	t.Parallel()
	fake := &fakeUsergroupsClient{}
	var out, errBuf bytes.Buffer
	code := runUsergroupsWith([]string{"--include-users", "--include-disabled", "--team-id", "T01"}, &out, &errBuf, fake)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errBuf.String())
	}
	if fake.gotN != 3 {
		t.Errorf("expected 3 options, got %d", fake.gotN)
	}
}

// --- user --email lookup ---

func (f *fakeUserClient) GetUserByEmail(email string) (*slack.User, error) {
	f.got = "email:" + email
	return f.user, f.err
}

func TestRunUser_ByEmail(t *testing.T) {
	t.Parallel()
	fake := &fakeUserClient{user: &slack.User{ID: "U9", Profile: slack.UserProfile{Email: "x@y"}}}
	var out, errBuf bytes.Buffer
	code := runUserWith([]string{"--email", "x@y"}, &out, &errBuf, fake)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errBuf.String())
	}
	if fake.got != "email:x@y" {
		t.Fatalf("email lookup not invoked: %q", fake.got)
	}
}

func TestRunUser_UserAndEmailMutuallyExclusive(t *testing.T) {
	t.Parallel()
	var out, errBuf bytes.Buffer
	code := runUserWith([]string{"--user", "U1", "--email", "x@y"}, &out, &errBuf, &fakeUserClient{})
	if code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
	if !strings.Contains(errBuf.String(), "mutually exclusive") {
		t.Errorf("stderr should hint at exclusivity: %q", errBuf.String())
	}
}

// --- channel (info) ---

type fakeChannelClient struct {
	ch  *slack.Channel
	err error
	got *slack.GetConversationInfoInput
}

func (f *fakeChannelClient) GetConversationInfo(input *slack.GetConversationInfoInput) (*slack.Channel, error) {
	f.got = input
	return f.ch, f.err
}

func TestRunChannel_DefaultsFromEnv(t *testing.T) {
	t.Setenv("SLACKRUN_CHANNEL", "C_ENV")
	ch := &slack.Channel{}
	ch.ID = "C_ENV"
	ch.Name = "general"
	fake := &fakeChannelClient{ch: ch}
	var out, errBuf bytes.Buffer
	code := runChannelWith(nil, &out, &errBuf, fake)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errBuf.String())
	}
	if fake.got.ChannelID != "C_ENV" {
		t.Fatalf("channel not defaulted: %+v", fake.got)
	}
	if !strings.Contains(out.String(), `"name":"general"`) {
		t.Errorf("body missing name: %s", out.String())
	}
}

// --- channels (list) ---

type fakeChannelsClient struct {
	channels []slack.Channel
	cursor   string
	err      error
	got      *slack.GetConversationsParameters
}

func (f *fakeChannelsClient) GetConversations(params *slack.GetConversationsParameters) ([]slack.Channel, string, error) {
	f.got = params
	return f.channels, f.cursor, f.err
}

func TestRunChannels_ParsesTypesCSV(t *testing.T) {
	t.Parallel()
	fake := &fakeChannelsClient{}
	var out, errBuf bytes.Buffer
	code := runChannelsWith(
		[]string{"--types", "public_channel,private_channel", "--exclude-archived"},
		&out, &errBuf, fake,
	)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errBuf.String())
	}
	if len(fake.got.Types) != 2 || !fake.got.ExcludeArchived {
		t.Fatalf("params not forwarded: %+v", fake.got)
	}
}

func TestRunChannels_LimitBounds(t *testing.T) {
	t.Parallel()
	var out, errBuf bytes.Buffer
	if code := runChannelsWith([]string{"--limit", "0"}, &out, &errBuf, &fakeChannelsClient{}); code != 2 {
		t.Errorf("--limit 0 should be usage error, got %d", code)
	}
	if code := runChannelsWith([]string{"--limit", "1001"}, &out, &errBuf, &fakeChannelsClient{}); code != 2 {
		t.Errorf("--limit 1001 should be usage error, got %d", code)
	}
}

// --- users (list) ---

type fakeUsersClient struct {
	users []slack.User
	err   error
	nOpts int
}

func (f *fakeUsersClient) GetUsers(options ...slack.GetUsersOption) ([]slack.User, error) {
	f.nOpts = len(options)
	return f.users, f.err
}

func TestRunUsers_Happy(t *testing.T) {
	t.Parallel()
	fake := &fakeUsersClient{users: []slack.User{{ID: "U1"}, {ID: "U2"}}}
	var out, errBuf bytes.Buffer
	code := runUsersWith([]string{"--presence"}, &out, &errBuf, fake)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errBuf.String())
	}
	if fake.nOpts != 1 {
		t.Errorf("expected 1 option (presence), got %d", fake.nOpts)
	}
	if !strings.Contains(out.String(), `"id":"U1"`) {
		t.Errorf("body missing users: %s", out.String())
	}
}

// --- file (info + download) ---

type fakeFileClient struct {
	info *slack.File
	err  error
	gotF string
}

func (f *fakeFileClient) GetFileInfo(fileID string, count, page int) (*slack.File, []slack.Comment, *slack.Paging, error) {
	f.gotF = fileID
	return f.info, nil, nil, f.err
}

type fakeDownloader struct {
	body     string
	gotURL   string
	gotToken string
	err      error
}

func (f *fakeDownloader) Download(_ context.Context, url, token string, w io.Writer) error {
	f.gotURL = url
	f.gotToken = token
	if f.err != nil {
		return f.err
	}
	_, err := w.Write([]byte(f.body))
	return err
}

func TestRunFile_Info(t *testing.T) {
	t.Parallel()
	fake := &fakeFileClient{info: &slack.File{ID: "F1", Name: "report.pdf"}}
	var out, errBuf bytes.Buffer
	code := runFileWith(
		[]string{"--file", "F1"},
		&out, &errBuf, fake, &fakeDownloader{}, "xoxb-fake",
	)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), `"name":"report.pdf"`) {
		t.Errorf("metadata missing name: %s", out.String())
	}
}

func TestRunFile_DownloadToStdout(t *testing.T) {
	t.Parallel()
	fake := &fakeFileClient{info: &slack.File{
		ID: "F1", URLPrivateDownload: "https://files.slack/perma",
	}}
	dl := &fakeDownloader{body: "PDF bytes"}
	var out, errBuf bytes.Buffer
	code := runFileWith(
		[]string{"--file", "F1", "--output", "-"},
		&out, &errBuf, fake, dl, "xoxb-token",
	)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errBuf.String())
	}
	if dl.gotToken != "xoxb-token" {
		t.Errorf("token not forwarded: %q", dl.gotToken)
	}
	if dl.gotURL != "https://files.slack/perma" {
		t.Errorf("URL not forwarded: %q", dl.gotURL)
	}
	if out.String() != "PDF bytes" {
		t.Errorf("body not streamed: %q", out.String())
	}
}

func TestRunFile_MissingFile_Usage(t *testing.T) {
	t.Parallel()
	var out, errBuf bytes.Buffer
	code := runFileWith(nil, &out, &errBuf, &fakeFileClient{}, &fakeDownloader{}, "x")
	if code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
}

func TestRunFile_DownloadToPath_AtomicOnFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := dir + "/existing.pdf"
	// Pre-populate the target so we can verify the failed download did not
	// clobber it.
	original := []byte("pre-existing content")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &fakeFileClient{info: &slack.File{ID: "F1", URLPrivateDownload: "https://x"}}
	dl := &fakeDownloader{err: errors.New("net")}
	var out, errBuf bytes.Buffer
	code := runFileWith(
		[]string{"--file", "F1", "--output", path},
		&out, &errBuf, fake, dl, "x",
	)
	if code != 1 {
		t.Fatalf("exit=%d, want 1", code)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Errorf("existing file was clobbered on failure: got %q", got)
	}
}
