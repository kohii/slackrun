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
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, clidoc.MainUsage)
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
		fmt.Print(clidoc.MainUsage)
	default:
		fmt.Fprintln(os.Stderr, "unknown subcommand:", os.Args[1])
		fmt.Fprint(os.Stderr, clidoc.MainUsage)
		os.Exit(2)
	}
}
