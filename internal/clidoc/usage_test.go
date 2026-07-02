package clidoc

import (
	"strings"
	"testing"
)

// The child-facing help is injected verbatim into a spawned child's prompt.
// It must be written for the child (second person, no rule-author noise).
func TestChildUsage_IsAgentFacing(t *testing.T) {
	t.Parallel()
	// Rule-author gating is invisible to the child — mentioning it in the
	// prompt only adds confusion.
	if strings.Contains(ChildUsage, "expose_slack_token") {
		t.Errorf("ChildUsage must not mention expose_slack_token — that belongs in MainUsage:\n%s", ChildUsage)
	}
	// Guard against regressing to the previous third-person rule-author
	// framing.
	if strings.Contains(ChildUsage, "callable from spawned children") {
		t.Errorf("ChildUsage regressed to third-person framing:\n%s", ChildUsage)
	}
	// SLACKRUN_* injection mechanics are host-facing — the child just needs
	// to know that the flags default sensibly.
	if strings.Contains(ChildUsage, "SLACKRUN_") {
		t.Errorf("ChildUsage should not surface SLACKRUN_* env details — the child cares about flag defaults, not injection mechanics:\n%s", ChildUsage)
	}
	// The full subcommand surface must be present so the child sees both
	// halves (write + read).
	for _, want := range []string{"slackrun post", "slackrun history", "slackrun me"} {
		if !strings.Contains(ChildUsage, want) {
			t.Errorf("ChildUsage missing %q:\n%s", want, ChildUsage)
		}
	}
}

// MainUsage is the `slackrun -h` block; the operator needs to know that
// child-side subcommands require the token to be forwarded.
func TestMainUsage_MentionsExposeSlackToken(t *testing.T) {
	t.Parallel()
	if !strings.Contains(MainUsage, "expose_slack_token") {
		t.Errorf("MainUsage must document expose_slack_token gating for the operator:\n%s", MainUsage)
	}
	// Dispatch section is what `slackrun -h` is primarily about; the
	// child-side reference is a subsection.
	for _, want := range []string{"slackrun start", "slackrun check", "slackrun dry-run", "slackrun replay", "slackrun version"} {
		if !strings.Contains(MainUsage, want) {
			t.Errorf("MainUsage missing dispatch subcommand %q:\n%s", want, MainUsage)
		}
	}
}
