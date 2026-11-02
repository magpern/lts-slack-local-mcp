package handler

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/magpern/lts-slack-local-mcp/pkg/provider"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

// fileSlack serves file metadata and a scripted download.
type fileSlack struct {
	recordingSlack
	file       slack.File
	downloaded []string
	stream     func(w io.Writer) error
}

func (f *fileSlack) GetFileInfoContext(context.Context, string, int, int) (*slack.File, []slack.Comment, *slack.Paging, error) {
	fl := f.file
	return &fl, nil, nil, nil
}

func (f *fileSlack) GetFileContext(_ context.Context, url string, w io.Writer) error {
	f.downloaded = append(f.downloaded, url)
	return f.stream(w)
}

func attachmentCall(t *testing.T, f *fileSlack) error {
	t.Helper()
	SetPolicy(Policy{Getenv: envFrom(map[string]string{"SLACK_MCP_ATTACHMENT_TOOL": "true"})})
	p := provider.NewWithClient(f, zap.NewNop())
	h := NewConversationsHandler(p, zap.NewNop())
	_, err := h.FilesGetHandler(context.Background(), req(map[string]any{"file_id": "F1"}))
	return err
}

func TestUnitAttachmentDownloadRefusesNonSlackHosts(t *testing.T) {
	for _, u := range []string{
		"https://evil.example.com/steal",
		"http://files.slack.com/x", // not HTTPS
		"https://slack.com.evil.example/x",
		"https://notslack.com/x",
		"file:///etc/passwd",
	} {
		f := &fileSlack{file: slack.File{ID: "F1", Size: 10, URLPrivateDownload: u, Mimetype: "text/plain"},
			stream: func(io.Writer) error { return nil }}
		if err := attachmentCall(t, f); err == nil {
			t.Errorf("%s: expected a refusal", u)
		}
		if len(f.downloaded) != 0 {
			t.Errorf("%s: download was attempted", u)
		}
	}
}

func TestUnitAttachmentDownloadIsBoundedByBytesReceived(t *testing.T) {
	f := &fileSlack{
		// Slack reports a small size; the stream is far larger.
		file: slack.File{ID: "F1", Size: 10, URLPrivateDownload: "https://files.slack.com/x", Mimetype: "text/plain"},
		stream: func(w io.Writer) error {
			chunk := []byte(strings.Repeat("a", 1<<20))
			for i := 0; i < 64; i++ {
				if _, err := w.Write(chunk); err != nil {
					return err
				}
			}
			return nil
		},
	}
	if err := attachmentCall(t, f); err == nil || !strings.Contains(err.Error(), "maximum allowed size") {
		t.Fatalf("expected the size bound to stop the download, got %v", err)
	}
}

func TestUnitAttachmentDownloadSucceedsForSlackHost(t *testing.T) {
	f := &fileSlack{
		file:   slack.File{ID: "F1", Size: 5, URLPrivateDownload: "https://files.slack.com/x", Mimetype: "text/plain", Name: "a.txt"},
		stream: func(w io.Writer) error { _, err := w.Write([]byte("hello")); return err },
	}
	if err := attachmentCall(t, f); err != nil {
		t.Fatalf("legitimate Slack download failed: %v", err)
	}
}
