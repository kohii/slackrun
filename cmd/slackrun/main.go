// Command slackrun receives Slack events over Socket Mode and dispatches
// them to local commands per ~/.config/slackrun/rules.yaml. See README for
// architecture overview and docs/security.md for trust boundaries.
package main

import (
	"fmt"
	"os"

	"github.com/kohii/slackrun/internal/cli"
)

// Version is overwritten at build time via:
//   go build -ldflags="-X main.Version=v1.2.3" ./cmd/slackrun
var Version = "dev"

const usage = `slackrun — dispatch Slack events to local commands

Dispatch:
  slackrun start [<rules.yaml>]                 Run the bot
  slackrun check <rules.yaml>                   Validate the rules file
  slackrun dry-run <rules.yaml> --event <file>  Show what would match (no spawn)

Write (called from spawned children; requires expose_slack_token: true on the rule):
  slackrun post   --channel C... [--thread-ts T] --text TEXT      (use --text - to read stdin)
  slackrun react  --channel C... --ts T --emoji NAME
  slackrun upload --channel C... [--thread-ts T] --file PATH [--title T] [--initial-comment T]

Misc:
  slackrun version                              Print version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "start":
		os.Exit(cli.RunStart(os.Args[2:], os.Stdout, os.Stderr))
	case "check":
		os.Exit(cli.RunCheck(os.Args[2:], os.Stdout, os.Stderr))
	case "dry-run":
		os.Exit(cli.RunDryRun(os.Args[2:], os.Stdout, os.Stderr))
	case "post":
		os.Exit(cli.RunPost(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	case "react":
		os.Exit(cli.RunReact(os.Args[2:], os.Stdout, os.Stderr))
	case "upload":
		os.Exit(cli.RunUpload(os.Args[2:], os.Stdout, os.Stderr))
	case "version", "--version", "-v":
		fmt.Println(Version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintln(os.Stderr, "unknown subcommand:", os.Args[1])
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}
