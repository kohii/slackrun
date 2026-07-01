package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/slack-go/slack"
)

// UsergroupsClient is the subset of *slack.Client RunUsergroups needs.
type UsergroupsClient interface {
	GetUserGroups(options ...slack.GetUserGroupsOption) ([]slack.UserGroup, error)
}

// RunUsergroups lists all usergroups in the workspace via usergroups.list.
// Exit codes: 0 success, 1 API error, 2 usage.
//
// Usage:
//
//	slackrun usergroups [--include-users] [--include-disabled] [--team-id T]
//
// Output (stdout, one line of JSON) is `{"usergroups": [...]}`. With
// `--include-users`, each usergroup carries the list of member user IDs.
func RunUsergroups(args []string, stdout, stderr io.Writer) int {
	client, err := slackClientFromEnv()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return runUsergroupsWith(args, stdout, stderr, client)
}

func runUsergroupsWith(args []string, stdout, stderr io.Writer, client UsergroupsClient) int {
	fs := flag.NewFlagSet("usergroups", flag.ContinueOnError)
	fs.SetOutput(stderr)
	includeUsers := fs.Bool("include-users", false, "populate each usergroup's users[] with member IDs")
	includeDisabled := fs.Bool("include-disabled", false, "include disabled usergroups")
	teamID := fs.String("team-id", "", "restrict to a specific team (Enterprise Grid)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var opts []slack.GetUserGroupsOption
	if *includeUsers {
		opts = append(opts, slack.GetUserGroupsOptionIncludeUsers(true))
	}
	if *includeDisabled {
		opts = append(opts, slack.GetUserGroupsOptionIncludeDisabled(true))
	}
	if *teamID != "" {
		opts = append(opts, slack.GetUserGroupsOptionTeamID(*teamID))
	}
	groups, err := client.GetUserGroups(opts...)
	if err != nil {
		fmt.Fprintln(stderr, "usergroups failed:", err)
		return 1
	}
	out, _ := json.Marshal(map[string]any{"usergroups": groups})
	fmt.Fprintln(stdout, string(out))
	return 0
}
