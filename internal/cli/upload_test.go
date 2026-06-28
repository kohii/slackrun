package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slack-go/slack"
)

type fakeUploader struct {
	getCalled      bool
	uploadCalled   bool
	completeCalled bool
	uploadParams   slack.UploadToURLParameters
	completeParams slack.CompleteUploadExternalParameters
}

func (f *fakeUploader) GetUploadURLExternalContext(_ context.Context, params slack.GetUploadURLExternalParameters) (*slack.GetUploadURLExternalResponse, error) {
	f.getCalled = true
	return &slack.GetUploadURLExternalResponse{
		UploadURL: "https://files.slack.test/upload",
		FileID:    "F123",
	}, nil
}

func (f *fakeUploader) UploadToURL(_ context.Context, params slack.UploadToURLParameters) error {
	f.uploadCalled = true
	f.uploadParams = params
	return nil
}

func (f *fakeUploader) CompleteUploadExternalContext(_ context.Context, params slack.CompleteUploadExternalParameters) (*slack.CompleteUploadExternalResponse, error) {
	f.completeCalled = true
	f.completeParams = params
	return &slack.CompleteUploadExternalResponse{
		Files: []slack.FileSummary{{ID: "F123"}},
	}, nil
}

func writeTempFile(t *testing.T, content []byte) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "slackrun-upload-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return f.Name()
}

func TestRunUpload_RedactsContentAndComment(t *testing.T) {
	t.Parallel()
	path := writeTempFile(t, []byte("send to alice@example.com please"))
	fake := &fakeUploader{}
	var stdout, stderr bytes.Buffer
	code := runUploadWith(
		[]string{
			"--channel", "C01",
			"--file", path,
			"--initial-comment", "see attachment: bob@example.com",
			"--title", "report",
		},
		&stdout, &stderr,
		fake,
	)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if strings.Contains(fake.uploadParams.Content, "alice@example.com") {
		t.Fatalf("content leaked email: %q", fake.uploadParams.Content)
	}
	if strings.Contains(fake.completeParams.InitialComment, "bob@example.com") {
		t.Fatalf("comment leaked email: %q", fake.completeParams.InitialComment)
	}
	if fake.completeParams.Files[0].Title != "report" {
		t.Fatalf("title=%q", fake.completeParams.Files[0].Title)
	}
	if !strings.Contains(stdout.String(), `"file_id"`) {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestRunUpload_PassesThreadTS(t *testing.T) {
	t.Parallel()
	path := writeTempFile(t, []byte("hi"))
	fake := &fakeUploader{}
	var stdout, stderr bytes.Buffer
	code := runUploadWith(
		[]string{"--channel", "C01", "--thread-ts", "123.456", "--file", path},
		&stdout, &stderr, fake,
	)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if fake.completeParams.ThreadTimestamp != "123.456" {
		t.Fatalf("thread_ts=%q", fake.completeParams.ThreadTimestamp)
	}
}

func TestRunUpload_RedactsFilename(t *testing.T) {
	t.Parallel()
	// Filename with an email in it must reach Slack with the email masked.
	dir := t.TempDir()
	path := filepath.Join(dir, "report-alice@example.com.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &fakeUploader{}
	var stdout, stderr bytes.Buffer
	code := runUploadWith([]string{"--channel", "C01", "--file", path}, &stdout, &stderr, fake)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if strings.Contains(fake.uploadParams.Filename, "alice@example.com") {
		t.Fatalf("filename leaked email: %q", fake.uploadParams.Filename)
	}
	if strings.Contains(fake.completeParams.Files[0].Title, "alice@example.com") {
		t.Fatalf("title leaked email: %q", fake.completeParams.Files[0].Title)
	}
}

func TestRunUpload_DefaultsTitleToBasename(t *testing.T) {
	t.Parallel()
	path := writeTempFile(t, []byte("hello\n"))
	fake := &fakeUploader{}
	var stdout, stderr bytes.Buffer
	code := runUploadWith([]string{"--channel", "C01", "--file", path}, &stdout, &stderr, fake)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if got := fake.completeParams.Files[0].Title; got != filepath.Base(path) {
		t.Fatalf("title=%q", got)
	}
}

func TestValidateUploadBody(t *testing.T) {
	t.Parallel()
	ok := []byte("hello\nworld\twith\ttabs\nand\rreturns")
	if err := validateUploadBody(ok); err != nil {
		t.Fatalf("expected ok, got %v", err)
	}

	cases := []struct {
		name string
		body []byte
		want string
	}{
		{"empty", []byte{}, "empty"},
		{"nul", []byte("hello\x00world"), "NUL"},
		{"non-utf8", []byte{0xff, 0xfe, 0xfd}, "UTF-8"},
		{"too-many-controls", append(bytes.Repeat([]byte{0x01}, 6), bytes.Repeat([]byte{'a'}, 10)...), "control"},
		{"too-large", bytes.Repeat([]byte{'a'}, uploadMaxBytes+1), "exceeds"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := validateUploadBody(c.body)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err=%q, want substr %q", err, c.want)
			}
		})
	}
}

func TestRunUpload_RejectsBinaryFile(t *testing.T) {
	t.Parallel()
	path := writeTempFile(t, []byte{0x00, 0x01, 0x02, 0x03})
	fake := &fakeUploader{}
	var stdout, stderr bytes.Buffer
	code := runUploadWith([]string{"--channel", "C01", "--file", path}, &stdout, &stderr, fake)
	if code != 1 {
		t.Fatalf("expected 1, got %d (stderr=%q)", code, stderr.String())
	}
	if fake.uploadCalled {
		t.Fatal("upload should not have been attempted")
	}
}

func TestRunUpload_RequiredFlags(t *testing.T) {
	t.Parallel()
	cases := [][]string{
		{},
		{"--channel", "C01"},
		{"--file", "/dev/null"},
	}
	for _, args := range cases {
		var stdout, stderr bytes.Buffer
		code := runUploadWith(args, &stdout, &stderr, &fakeUploader{})
		if code != 2 {
			t.Fatalf("args=%v expected 2, got %d", args, code)
		}
	}
}
