package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// TestDecideEditedWebPage 锁定编辑后链接预览的决策矩阵：保留 / 清除 / 换成新链接。
func TestDecideEditedWebPage(t *testing.T) {
	const oldURL = "https://example.com/old"
	const newURL = "https://example.com/new"
	donePreview := func(url string) *domain.MessageMedia {
		return &domain.MessageMedia{Kind: domain.MessageMediaKindWebPage, WebPage: &domain.MessageWebPage{
			State: domain.MessageWebPageStateDone, ID: domain.WebPageURLHash(url), URL: url, Title: "Old",
		}}
	}
	pendingPreview := func(url string) *domain.MessageMedia {
		return &domain.MessageMedia{Kind: domain.MessageMediaKindWebPage, WebPage: &domain.MessageWebPage{
			State: domain.MessageWebPageStatePending, ID: domain.WebPageURLHash(url), URL: url,
		}}
	}
	photo := &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 1}}

	tests := []struct {
		name string
		in   editWebPageInput
		want editWebPageDecision
	}{
		{
			name: "caption edit of a real media message keeps the media",
			in:   editWebPageInput{Original: photo, TextEdited: true, Message: "no link"},
			want: editWebPageDecision{Action: editWebPageKeep},
		},
		{
			name: "real media is kept even when no_webpage is set",
			in:   editWebPageInput{Original: photo, TextEdited: true, Message: "see " + newURL, NoWebpage: true},
			want: editWebPageDecision{Action: editWebPageKeep},
		},
		{
			name: "markup-only edit keeps the preview",
			in:   editWebPageInput{Original: donePreview(oldURL), TextEdited: false},
			want: editWebPageDecision{Action: editWebPageKeep},
		},
		{
			name: "no_webpage clears the preview even when the text still links",
			in:   editWebPageInput{Original: donePreview(oldURL), TextEdited: true, Message: "see " + oldURL, NoWebpage: true},
			want: editWebPageDecision{Action: editWebPageClear},
		},
		{
			name: "explicit InputMediaWebPage replaces with the requested url and size preference",
			in: editWebPageInput{
				Original: donePreview(oldURL), TextEdited: true, Message: "see " + oldURL,
				WebPage: &tg.InputMediaWebPage{URL: newURL, ForceLargeMedia: true},
			},
			want: editWebPageDecision{Action: editWebPageReplace, URL: newURL, ForceLarge: true},
		},
		{
			name: "text without a previewable link clears the preview",
			in:   editWebPageInput{Original: donePreview(oldURL), TextEdited: true, Message: "plain text"},
			want: editWebPageDecision{Action: editWebPageClear},
		},
		{
			name: "same first link as the current preview keeps it",
			in:   editWebPageInput{Original: donePreview(oldURL), TextEdited: true, Message: "still " + oldURL + " here"},
			want: editWebPageDecision{Action: editWebPageKeep},
		},
		{
			name: "same first link as a pending preview keeps it",
			in:   editWebPageInput{Original: pendingPreview(oldURL), TextEdited: true, Message: "again " + oldURL},
			want: editWebPageDecision{Action: editWebPageKeep},
		},
		{
			name: "a changed first link replaces the preview",
			in: editWebPageInput{
				Original: donePreview(oldURL), TextEdited: true, Message: "now " + newURL,
				Entities: []tg.MessageEntityClass{&tg.MessageEntityURL{Offset: 4, Length: len(newURL)}},
			},
			want: editWebPageDecision{Action: editWebPageReplace, URL: newURL},
		},
		{
			name: "a text message gaining a link gets a preview",
			in:   editWebPageInput{TextEdited: true, Message: "now " + newURL},
			want: editWebPageDecision{Action: editWebPageReplace, URL: newURL},
		},
		{
			name: "a text message staying link-free is cleared (a no-op on an empty media)",
			in:   editWebPageInput{TextEdited: true, Message: "plain"},
			want: editWebPageDecision{Action: editWebPageClear},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decideEditedWebPage(tt.in); got != tt.want {
				t.Fatalf("decideEditedWebPage = %+v, want %+v", got, tt.want)
			}
		})
	}
}
