package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/slack-go/slack"
)

type fakeReacter struct {
	lastName string
	lastItem slack.ItemRef
	returnErr error
}

func (f *fakeReacter) AddReaction(name string, item slack.ItemRef) error {
	f.lastName = name
	f.lastItem = item
	return f.returnErr
}

func TestRunReact_StripsColons(t *testing.T) {
	t.Parallel()
	fake := &fakeReacter{}
	var stdout, stderr bytes.Buffer
	code := runReactWith([]string{"--channel", "C01", "--ts", "123.456", "--emoji", ":eyes:"}, &stdout, &stderr, fake)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if fake.lastName != "eyes" {
		t.Fatalf("name=%q", fake.lastName)
	}
	if fake.lastItem.Channel != "C01" || fake.lastItem.Timestamp != "123.456" {
		t.Fatalf("item=%+v", fake.lastItem)
	}
}

func TestRunReact_RequiredFlags(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
	}{
		{"no-channel", []string{"--ts", "1", "--emoji", "eyes"}},
		{"no-ts", []string{"--channel", "C01", "--emoji", "eyes"}},
		{"no-emoji", []string{"--channel", "C01", "--ts", "1"}},
		{"empty-emoji-after-colons", []string{"--channel", "C01", "--ts", "1", "--emoji", "::"}},
		{"embedded-colon", []string{"--channel", "C01", "--ts", "1", "--emoji", "a:b"}},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			code := runReactWith(c.args, &stdout, &stderr, &fakeReacter{})
			if code != 2 {
				t.Fatalf("expected 2, got %d (stderr=%q)", code, stderr.String())
			}
		})
	}
}

func TestRunReact_APIErrorExits1(t *testing.T) {
	t.Parallel()
	fake := &fakeReacter{returnErr: errors.New("already_reacted")}
	var stdout, stderr bytes.Buffer
	code := runReactWith([]string{"--channel", "C01", "--ts", "1", "--emoji", "eyes"}, &stdout, &stderr, fake)
	if code != 1 {
		t.Fatalf("expected 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "already_reacted") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}
