package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/kohii/slackrun/internal/util"
	"github.com/slack-go/slack"
)

// Uploader is the 3-step V2 upload surface. *slack.Client satisfies it.
type Uploader interface {
	GetUploadURLExternalContext(ctx context.Context, params slack.GetUploadURLExternalParameters) (*slack.GetUploadURLExternalResponse, error)
	UploadToURL(ctx context.Context, params slack.UploadToURLParameters) error
	CompleteUploadExternalContext(ctx context.Context, params slack.CompleteUploadExternalParameters) (*slack.CompleteUploadExternalResponse, error)
}

const uploadMaxBytes = 1 << 20 // 1 MiB

// RunUpload uploads a text file to a channel/thread. Exit codes match RunPost.
//
// Usage:
//
//	slackrun upload --channel C... [--thread-ts T] --file PATH [--title TITLE] [--initial-comment TEXT]
//
// Input constraints (v1, text-only):
//   - max 1 MiB
//   - UTF-8 valid
//   - no NUL bytes
//   - control-character ratio (excl. \t \n \r) below 5 %.
func RunUpload(args []string, stdout, stderr io.Writer) int {
	client, err := slackClientFromEnv()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return runUploadWith(args, stdout, stderr, client)
}

func runUploadWith(args []string, stdout, stderr io.Writer, client Uploader) int {
	fs := flag.NewFlagSet("upload", flag.ContinueOnError)
	fs.SetOutput(stderr)
	channel := fs.String("channel", "", "channel ID (required)")
	threadTS := fs.String("thread-ts", "", "thread timestamp (optional)")
	filePath := fs.String("file", "", "path to text file (required)")
	title := fs.String("title", "", "file title (optional)")
	initialComment := fs.String("initial-comment", "", "comment posted alongside the file (optional)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	chanID := resolveFromEnv(*channel, "SLACKRUN_CHANNEL")
	threadID := resolveFromEnv(*threadTS, "SLACKRUN_THREAD_TS")
	switch {
	case chanID == "":
		fmt.Fprintln(stderr, "--channel is required (or set SLACKRUN_CHANNEL)")
		return 2
	case *filePath == "":
		fmt.Fprintln(stderr, "--file is required")
		return 2
	}

	raw, err := os.ReadFile(*filePath)
	if err != nil {
		fmt.Fprintln(stderr, "read file:", err)
		return 1
	}
	if err := validateUploadBody(raw); err != nil {
		fmt.Fprintln(stderr, "file:", err)
		return 1
	}
	content := util.SanitizeForSlack(string(raw))
	cleanTitle := util.SanitizeForSlack(*title)
	cleanComment := util.SanitizeForSlack(*initialComment)
	// Filename is rendered by Slack alongside the file — redact the basename
	// the same way we redact title/comment/content.
	cleanFilename := util.SanitizeForSlack(filepath.Base(*filePath))
	if cleanFilename == "" {
		cleanFilename = "upload.txt"
	}

	ctx := context.Background()
	urlResp, err := client.GetUploadURLExternalContext(ctx, slack.GetUploadURLExternalParameters{
		FileName: cleanFilename,
		FileSize: len(content),
	})
	if err != nil {
		fmt.Fprintln(stderr, "getUploadURLExternal:", err)
		return 1
	}
	if err := client.UploadToURL(ctx, slack.UploadToURLParameters{
		UploadURL: urlResp.UploadURL,
		Filename:  cleanFilename,
		Content:   content,
	}); err != nil {
		fmt.Fprintln(stderr, "uploadToURL:", err)
		return 1
	}
	file := slack.FileSummary{ID: urlResp.FileID, Title: cleanTitle}
	if cleanTitle == "" {
		file.Title = cleanFilename
	}
	resp, err := client.CompleteUploadExternalContext(ctx, slack.CompleteUploadExternalParameters{
		Files:           []slack.FileSummary{file},
		Channel:         chanID,
		ThreadTimestamp: threadID,
		InitialComment:  cleanComment,
	})
	if err != nil {
		fmt.Fprintln(stderr, "completeUploadExternal:", err)
		return 1
	}
	fileID := urlResp.FileID
	if resp != nil && len(resp.Files) > 0 {
		fileID = resp.Files[0].ID
	}
	out, _ := json.Marshal(map[string]string{"file_id": fileID})
	fmt.Fprintln(stdout, string(out))
	return 0
}

// validateUploadBody enforces the text-only constraints. The thresholds are
// chosen to reject mis-typed binary uploads without false-positiving on
// ANSI-coloured CLI output (handled separately by SanitizeForSlack).
func validateUploadBody(body []byte) error {
	if len(body) == 0 {
		return errors.New("file is empty")
	}
	if len(body) > uploadMaxBytes {
		return fmt.Errorf("file size %d exceeds %d-byte limit", len(body), uploadMaxBytes)
	}
	if !utf8.Valid(body) {
		return errors.New("file is not valid UTF-8")
	}
	var controls int
	for _, b := range body {
		if b == 0x00 {
			return errors.New("file contains NUL bytes")
		}
		if b < 0x20 && b != '\t' && b != '\n' && b != '\r' {
			controls++
		}
	}
	if controls*20 > len(body) { // > 5 %
		return errors.New("file looks binary (too many control characters)")
	}
	return nil
}

