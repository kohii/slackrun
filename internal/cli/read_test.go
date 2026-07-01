package cli

import (
	"bytes"
	"encoding/json"
	"errors"
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
