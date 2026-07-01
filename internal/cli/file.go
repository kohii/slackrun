package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/slack-go/slack"
)

// fileDownloadTimeout bounds a single `slackrun file --output ...` call.
// Slack file URLs occasionally stall; without this bound the child would
// hang until its parent-side runner timeout fires.
const fileDownloadTimeout = 60 * time.Second

// FileClient is the subset of *slack.Client RunFile needs for metadata.
type FileClient interface {
	GetFileInfo(fileID string, count, page int) (*slack.File, []slack.Comment, *slack.Paging, error)
}

// FileDownloader downloads a Slack-hosted file. Split out so tests do not
// require a real HTTP round-trip.
type FileDownloader interface {
	Download(ctx context.Context, url, token string, w io.Writer) error
}

// RunFile fetches file metadata (files.info) and, when --output is set,
// streams the file body to disk / stdout. Slack file URLs are
// token-gated, so downloading here is more convenient than curl for a
// child that already has the token forwarded.
//
// Usage:
//
//	slackrun file --file F... [--output PATH]
//
// Without --output: prints the slack.File struct as JSON.
// With --output: writes the file body to PATH (or stdout when PATH is "-").
// Requires files:read on the app.
func RunFile(args []string, stdout, stderr io.Writer) int {
	tok := os.Getenv("SLACK_BOT_TOKEN")
	if tok == "" {
		fmt.Fprintln(stderr, errNoSlackToken)
		return 1
	}
	return runFileWith(args, stdout, stderr, slack.New(tok), httpFileDownloader{}, tok)
}

func runFileWith(args []string, stdout, stderr io.Writer, client FileClient, dl FileDownloader, token string) int {
	fs := flag.NewFlagSet("file", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fileID := fs.String("file", "", "Slack file ID (required)")
	output := fs.String("output", "", `download destination path; "-" for stdout. Omit to print metadata as JSON.`)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *fileID == "" {
		fmt.Fprintln(stderr, "--file is required")
		return 2
	}
	info, _, _, err := client.GetFileInfo(*fileID, 0, 0)
	if err != nil {
		fmt.Fprintln(stderr, "file failed:", err)
		return 1
	}
	if *output == "" {
		out, _ := json.Marshal(info)
		fmt.Fprintln(stdout, string(out))
		return 0
	}
	url := info.URLPrivateDownload
	if url == "" {
		url = info.URLPrivate
	}
	if url == "" {
		fmt.Fprintln(stderr, "file has no downloadable URL (external / deleted?)")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), fileDownloadTimeout)
	defer cancel()

	if *output == "-" {
		if err := dl.Download(ctx, url, token, stdout); err != nil {
			fmt.Fprintln(stderr, "download failed:", err)
			return 1
		}
		return 0
	}
	// Write to a sibling temp file first so a mid-download failure does not
	// leave a truncated file at the caller's path.
	dir := filepath.Dir(*output)
	tmp, err := os.CreateTemp(dir, ".slackrun-file-*")
	if err != nil {
		fmt.Fprintln(stderr, "create temp file:", err)
		return 1
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op if the rename succeeded first
	if err := dl.Download(ctx, url, token, tmp); err != nil {
		tmp.Close()
		fmt.Fprintln(stderr, "download failed:", err)
		return 1
	}
	if err := tmp.Close(); err != nil {
		fmt.Fprintln(stderr, "close temp file:", err)
		return 1
	}
	if err := os.Rename(tmpPath, *output); err != nil {
		fmt.Fprintln(stderr, "rename output:", err)
		return 1
	}
	return 0
}

// httpFileDownloader is the production FileDownloader. Uses a dedicated
// http.Client (not DefaultClient — cross-call state pollution risk) with a
// Bearer auth header, matching what `curl -H "Authorization: Bearer …"`
// would send.
type httpFileDownloader struct{}

var fileHTTPClient = &http.Client{Timeout: fileDownloadTimeout}

func (httpFileDownloader) Download(ctx context.Context, url, token string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := fileHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}
