package dispatch

import (
	"reflect"
	"sort"
	"testing"
)

func TestExpandTemplate(t *testing.T) {
	t.Parallel()
	vars := TemplateVars{
		Permalink: "https://slack/perma",
		ChannelID: "C01",
		UserID:    "U01",
		TS:        "1234567890.0001",
		ThreadTS:  "1234567880.0001",
	}
	in := "go {{event.permalink}} ({{event.channel_id}}/{{event.user_id}}): ts={{event.ts}} thread_ts={{event.thread_ts}}"
	want := "go https://slack/perma (C01/U01): ts=1234567890.0001 thread_ts=1234567880.0001"
	if got := ExpandTemplate(in, vars); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestExpandTemplate_UnknownLeftAlone(t *testing.T) {
	t.Parallel()
	got := ExpandTemplate("hi {{nope}} {{event.user_id}}", TemplateVars{UserID: "U01"})
	if got != "hi {{nope}} U01" {
		t.Fatalf("got %q", got)
	}
}

func TestTemplateVarsUsed(t *testing.T) {
	t.Parallel()
	got := TemplateVarsUsed("a {{event.user_id}} b {{event.permalink}} c {{event.user_id}}")
	sort.Strings(got)
	want := []string{"event.permalink", "event.user_id"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestTemplateUsesPermalink(t *testing.T) {
	t.Parallel()
	if !TemplateUsesPermalink("see {{event.permalink}}") {
		t.Fatal("expected true")
	}
	if TemplateUsesPermalink("see {{event.user_id}}") {
		t.Fatal("expected false")
	}
}
