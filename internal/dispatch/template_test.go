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
		Text:      "hello world",
		Rest:      "world",
		Channel:   "C01",
		User:      "U01",
	}
	in := "go {{permalink}} ({{channel}}/{{user}}): text={{text}} rest={{rest}}"
	want := "go https://slack/perma (C01/U01): text=hello world rest=world"
	if got := ExpandTemplate(in, vars); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestExpandTemplate_UnknownLeftAlone(t *testing.T) {
	t.Parallel()
	got := ExpandTemplate("hi {{nope}} {{text}}", TemplateVars{Text: "x"})
	if got != "hi {{nope}} x" {
		t.Fatalf("got %q", got)
	}
}

func TestTemplateVarsUsed(t *testing.T) {
	t.Parallel()
	got := TemplateVarsUsed("a {{text}} b {{permalink}} c {{text}}")
	sort.Strings(got)
	want := []string{"permalink", "text"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestTemplateUsesPermalink(t *testing.T) {
	t.Parallel()
	if !TemplateUsesPermalink("see {{permalink}}") {
		t.Fatal("expected true")
	}
	if TemplateUsesPermalink("see {{text}}") {
		t.Fatal("expected false")
	}
}
