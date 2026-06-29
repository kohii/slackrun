package slackthread

import (
	"context"
	"errors"
	"testing"

	"github.com/slack-go/slack"
)

type fakeReplier struct {
	pages [][]slack.Message
	// hasMore for each page; default false unless set.
	hasMore   []bool
	calls     int
	lastParam slack.GetConversationRepliesParameters
	err       error
}

func (f *fakeReplier) GetConversationRepliesContext(_ context.Context, p *slack.GetConversationRepliesParameters) ([]slack.Message, bool, string, error) {
	if f.err != nil {
		return nil, false, "", f.err
	}
	f.lastParam = *p
	i := f.calls
	f.calls++
	if i >= len(f.pages) {
		return nil, false, "", nil
	}
	more := false
	if i < len(f.hasMore) {
		more = f.hasMore[i]
	}
	next := ""
	if more {
		next = "cursor-" + p.ChannelID
	}
	return f.pages[i], more, next, nil
}

func TestFetch_NormalizesUserAndBotMessages(t *testing.T) {
	t.Parallel()
	api := &fakeReplier{pages: [][]slack.Message{{
		mkUserMsg("100.0", "U1", "hello"),
		mkBotMsg("100.1", "Sentry", "alert!"),
	}}}
	r, err := Fetch(context.Background(), api, FetchOptions{Channel: "C1", ThreadTS: "100.0"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Messages) != 2 {
		t.Fatalf("got %d msgs", len(r.Messages))
	}
	if r.Messages[0].Source != SourceUser || r.Messages[0].User != "U1" {
		t.Errorf("user mismatch: %+v", r.Messages[0])
	}
	if r.Messages[1].Source != SourceBot || r.Messages[1].Bot != "Sentry" {
		t.Errorf("bot mismatch: %+v", r.Messages[1])
	}
}

func TestFetch_TagsSelfMessages(t *testing.T) {
	t.Parallel()
	api := &fakeReplier{pages: [][]slack.Message{{
		mkUserMsg("100.0", "U_OTHER", "user"),
		mkUserMsg("100.1", "U_SELF", "should be self"),
	}}}
	r, err := Fetch(context.Background(), api, FetchOptions{
		Channel: "C1", ThreadTS: "100.0", SelfUserID: "U_SELF",
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Messages[0].Source != SourceUser {
		t.Errorf("expected SourceUser, got %v", r.Messages[0].Source)
	}
	if r.Messages[1].Source != SourceSelf {
		t.Errorf("expected SourceSelf, got %v", r.Messages[1].Source)
	}
}

func TestFetch_PaginationBoundedByMaxPages(t *testing.T) {
	t.Parallel()
	api := &fakeReplier{
		pages: [][]slack.Message{
			{mkUserMsg("1", "U1", "p1")},
			{mkUserMsg("2", "U1", "p2")},
			{mkUserMsg("3", "U1", "p3")},
		},
		hasMore: []bool{true, true, true},
	}
	r, err := Fetch(context.Background(), api, FetchOptions{
		Channel: "C1", ThreadTS: "1", MaxPages: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !r.HasMore {
		t.Errorf("expected HasMore=true (capped by MaxPages)")
	}
	if api.calls != 2 {
		t.Errorf("calls=%d, want 2", api.calls)
	}
	if len(r.Messages) != 2 {
		t.Errorf("messages=%d, want 2", len(r.Messages))
	}
}

func TestFetch_StopsOnHasMoreFalse(t *testing.T) {
	t.Parallel()
	api := &fakeReplier{
		pages:   [][]slack.Message{{mkUserMsg("1", "U1", "only")}},
		hasMore: []bool{false},
	}
	r, err := Fetch(context.Background(), api, FetchOptions{
		Channel: "C1", ThreadTS: "1", MaxPages: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.HasMore {
		t.Error("expected HasMore=false")
	}
	if api.calls != 1 {
		t.Errorf("calls=%d, want 1", api.calls)
	}
}

func TestFetch_PropagatesError(t *testing.T) {
	t.Parallel()
	api := &fakeReplier{err: errors.New("boom")}
	_, err := Fetch(context.Background(), api, FetchOptions{Channel: "C1", ThreadTS: "1"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestFromSlackMessage_BotNamePrecedence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		m    slack.Message
		want string
	}{
		{"username wins", slack.Message{Msg: slack.Msg{Username: "U", BotID: "B", BotProfile: &slack.BotProfile{Name: "P", AppID: "A"}}}, "U"},
		{"name wins over appid", slack.Message{Msg: slack.Msg{BotID: "B", BotProfile: &slack.BotProfile{Name: "P", AppID: "A"}}}, "P"},
		{"appid over botid", slack.Message{Msg: slack.Msg{BotID: "B", BotProfile: &slack.BotProfile{AppID: "A"}}}, "A"},
		{"botid fallback", slack.Message{Msg: slack.Msg{BotID: "B"}}, "B"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := FromSlackMessage(c.m, "", "")
			if got.Source != SourceBot {
				t.Fatalf("source = %v", got.Source)
			}
			if got.Bot != c.want {
				t.Fatalf("bot = %q, want %q", got.Bot, c.want)
			}
		})
	}
}

func mkUserMsg(ts, user, text string) slack.Message {
	return slack.Message{Msg: slack.Msg{Timestamp: ts, User: user, Text: text}}
}
func mkBotMsg(ts, name, text string) slack.Message {
	return slack.Message{Msg: slack.Msg{Timestamp: ts, Username: name, Text: text, BotID: "B" + name}}
}
