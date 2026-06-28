// Package cli contains the subcommand implementations exposed by the
// slackrun binary.
package cli

import (
	"fmt"
	"io"

	"github.com/kohii/slackrun/internal/config"
)

// RunCheck validates a rules.yaml and prints a per-rule summary. Returns
// process exit code: 0 on success (warnings allowed), 1 on validation
// errors, 2 on usage errors.
func RunCheck(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: slackrun check <rules.yaml>")
		return 2
	}
	result, err := config.LoadRulesFile(args[0], config.CheckOptions{})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	var errors, warns int
	for _, i := range result.Issues {
		tag := "WARN"
		if i.Level == config.IssueError {
			tag = "ERROR"
			errors++
		} else {
			warns++
		}
		where := ""
		if i.RuleName != "" {
			where = " [" + i.RuleName + "]"
		}
		fmt.Fprintf(stderr, "%s%s: %s\n", tag, where, i.Message)
	}

	fmt.Fprintf(stdout, "%d rule(s) loaded, %d error(s), %d warning(s)\n", len(result.Rules), errors, warns)
	for _, r := range result.Rules {
		trig := ""
		switch r.Trigger.Type {
		case config.TriggerTypeMessage:
			trig = "message channel=" + r.Trigger.Channel
		case config.TriggerTypeAppMention:
			if r.Trigger.Keyword == nil {
				trig = "app_mention keyword=<default>"
			} else {
				trig = "app_mention keyword=" + *r.Trigger.Keyword
			}
		}
		fmt.Fprintf(stdout, "  - %s: %s → %v in %s\n", r.Name, trig, r.Action.Command, r.Action.Cwd)
	}
	if errors > 0 {
		return 1
	}
	return 0
}
