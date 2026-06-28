package util

import (
	"strings"
	"testing"
)

func TestStripANSI(t *testing.T) {
	t.Parallel()
	in := "\x1b[31mred\x1b[0m and \x1b[1;32mgreenbold\x1b[39m"
	got := StripANSI(in)
	if got != "red and greenbold" {
		t.Fatalf("got %q", got)
	}
}

func TestStripSystemTags(t *testing.T) {
	t.Parallel()
	in := "before <system-reminder>internal</system-reminder> middle <command-args>x</command-args> end"
	got := StripSystemTags(in)
	if strings.Contains(got, "system-reminder") || strings.Contains(got, "command-args") {
		t.Fatalf("tags survived: %q", got)
	}
	if !strings.Contains(got, "before") || !strings.Contains(got, "middle") || !strings.Contains(got, "end") {
		t.Fatalf("content lost: %q", got)
	}
}

func TestSanitizeForSlack_FullPipeline(t *testing.T) {
	t.Parallel()
	in := "\x1b[31m<system-reminder>x</system-reminder>contact alice@example.com\x1b[0m   "
	got := SanitizeForSlack(in)
	if strings.Contains(got, "\x1b") {
		t.Fatalf("ANSI survived: %q", got)
	}
	if strings.Contains(got, "system-reminder") {
		t.Fatalf("system tag survived: %q", got)
	}
	if strings.Contains(got, "alice@example.com") {
		t.Fatalf("email survived: %q", got)
	}
	if strings.HasPrefix(got, " ") || strings.HasSuffix(got, " ") {
		t.Fatalf("not trimmed: %q", got)
	}
}
