// Command slackrun receives Slack events over Socket Mode and dispatches
// them to local commands per ~/.config/slackrun/rules.yaml. See README for
// architecture overview and docs/security.md for trust boundaries.
package main

import (
	"fmt"
	"os"

	"github.com/kohii/slackrun/internal/cli"
	"github.com/kohii/slackrun/internal/clidoc"
)

// Version is overwritten at build time via:
//   go build -ldflags="-X main.Version=v1.2.3" ./cmd/slackrun
var Version = "dev"

func main() {
	cli.SetVersion(Version)
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, clidoc.MainUsage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "start":
		os.Exit(cli.RunStart(os.Args[2:], os.Stdout, os.Stderr))
	case "runs", "ps":
		os.Exit(cli.RunRuns(os.Args[2:], os.Stdout, os.Stderr))
	case "kill":
		os.Exit(cli.RunKill(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	case "check":
		os.Exit(cli.RunCheck(os.Args[2:], os.Stdout, os.Stderr))
	case "dry-run":
		os.Exit(cli.RunDryRun(os.Args[2:], os.Stdout, os.Stderr))
	case "replay":
		os.Exit(cli.RunReplay(os.Args[2:], os.Stdout, os.Stderr))
	case "post":
		os.Exit(cli.RunPost(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	case "react":
		os.Exit(cli.RunReact(os.Args[2:], os.Stdout, os.Stderr))
	case "upload":
		os.Exit(cli.RunUpload(os.Args[2:], os.Stdout, os.Stderr))
	case "history":
		os.Exit(cli.RunHistory(os.Args[2:], os.Stdout, os.Stderr))
	case "replies":
		os.Exit(cli.RunReplies(os.Args[2:], os.Stdout, os.Stderr))
	case "reactions":
		os.Exit(cli.RunReactions(os.Args[2:], os.Stdout, os.Stderr))
	case "user":
		os.Exit(cli.RunUser(os.Args[2:], os.Stdout, os.Stderr))
	case "usergroups":
		os.Exit(cli.RunUsergroups(os.Args[2:], os.Stdout, os.Stderr))
	case "update":
		os.Exit(cli.RunUpdate(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	case "ephemeral":
		os.Exit(cli.RunEphemeral(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	case "unreact":
		os.Exit(cli.RunUnreact(os.Args[2:], os.Stdout, os.Stderr))
	case "me":
		os.Exit(cli.RunMe(os.Args[2:], os.Stdout, os.Stderr))
	case "channel":
		os.Exit(cli.RunChannel(os.Args[2:], os.Stdout, os.Stderr))
	case "channels":
		os.Exit(cli.RunChannels(os.Args[2:], os.Stdout, os.Stderr))
	case "users":
		os.Exit(cli.RunUsers(os.Args[2:], os.Stdout, os.Stderr))
	case "file":
		os.Exit(cli.RunFile(os.Args[2:], os.Stdout, os.Stderr))
	case "version", "--version", "-v":
		fmt.Println(Version)
	case "help", "--help", "-h":
		fmt.Print(clidoc.MainUsage)
	default:
		fmt.Fprintln(os.Stderr, "unknown subcommand:", os.Args[1])
		fmt.Fprint(os.Stderr, clidoc.MainUsage)
		os.Exit(2)
	}
}
