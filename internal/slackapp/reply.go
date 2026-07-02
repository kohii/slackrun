package slackapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/kohii/slackrun/internal/util"
	"github.com/slack-go/slack"
)

// Uploader is the V2 file-upload surface (3-step protocol). *slack.Client
// satisfies it.
type Uploader interface {
	GetUploadURLExternalContext(ctx context.Context, params slack.GetUploadURLExternalParameters) (*slack.GetUploadURLExternalResponse, error)
	UploadToURL(ctx context.Context, params slack.UploadToURLParameters) error
	CompleteUploadExternalContext(ctx context.Context, params slack.CompleteUploadExternalParameters) (*slack.CompleteUploadExternalResponse, error)
}

// ReplyClient is the union of operations PostCompletionReply needs.
type ReplyClient interface {
	Poster
	Uploader
}

// PostCompletionReply pushes the command's stdout back into the thread,
// choosing the smallest delivery shape that fits (chat.update / multi-post /
// file upload). The progress message is overwritten or deleted as a side
// effect; callers must not also Update the handle.
func PostCompletionReply(ctx context.Context, c ReplyClient, progress ProgressHandle, threadTS, rawStdout string) error {
	cleaned := util.SanitizeForSlack(rawStdout)
	if cleaned == "" {
		return progress.Done()
	}
	plan := util.PlanPost(cleaned)
	switch plan.Kind {
	case util.PostKindSingle:
		return progress.Update(plan.Text)
	case util.PostKindMulti:
		if len(plan.Parts) == 0 {
			return progress.Done()
		}
		if err := progress.Update(plan.Parts[0]); err != nil {
			return err
		}
		for _, part := range plan.Parts[1:] {
			if _, _, err := c.PostMessage(progress.Channel(),
				slack.MsgOptionText(part, false),
				slack.MsgOptionTS(threadTS),
				slack.MsgOptionDisableMarkdown(),
			); err != nil {
				return fmt.Errorf("post multi-part: %w", err)
			}
		}
		return nil
	case util.PostKindFile:
		if err := progress.Remove(); err != nil {
			// Non-fatal: a delete failure shouldn't block delivering content.
			// Caller will see the orphan ⏳ message and can clean up manually.
			_ = err
		}
		return uploadAsFile(ctx, c, progress.Channel(), threadTS, plan.Text)
	}
	return fmt.Errorf("unknown plan kind %d", plan.Kind)
}

func uploadAsFile(ctx context.Context, c Uploader, channel, threadTS, content string) error {
	if !strings.HasSuffix(content, "\n") {
		// Files render better with a trailing newline.
		content += "\n"
	}
	urlResp, err := c.GetUploadURLExternalContext(ctx, slack.GetUploadURLExternalParameters{
		FileName: "output.txt",
		FileSize: len(content),
	})
	if err != nil {
		return fmt.Errorf("getUploadURLExternal: %w", err)
	}
	if err := c.UploadToURL(ctx, slack.UploadToURLParameters{
		UploadURL: urlResp.UploadURL,
		Filename:  "output.txt",
		Content:   content,
	}); err != nil {
		return fmt.Errorf("uploadToURL: %w", err)
	}
	if _, err := c.CompleteUploadExternalContext(ctx, slack.CompleteUploadExternalParameters{
		Files:           []slack.FileSummary{{ID: urlResp.FileID, Title: "output.txt"}},
		Channel:         channel,
		ThreadTimestamp: threadTS,
		InitialComment:  "📎 output (too long for inline post)",
	}); err != nil {
		return fmt.Errorf("completeUploadExternal: %w", err)
	}
	return nil
}
