package storetest

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// EditWebPageFixture 是新建的一对用户与对应的存储；每个子测试各要一份。
type EditWebPageFixture struct {
	Messages         store.MessageStore
	Channels         store.ChannelStore
	Owner, Recipient int64
}

const editWebPageRandomBase = 7_800_000

func editWebPageMedia(url, title string) *domain.MessageMedia {
	return &domain.MessageMedia{Kind: domain.MessageMediaKindWebPage, WebPage: &domain.MessageWebPage{
		State: domain.MessageWebPageStateDone, ID: domain.WebPageURLHash(url), URL: url, DisplayURL: "example.com", Title: title,
	}}
}

func webPageURLOf(media *domain.MessageMedia) string {
	if media.IsZero() || media.Kind != domain.MessageMediaKindWebPage || media.WebPage == nil {
		return ""
	}
	return media.WebPage.URL
}

// EditWebPage 校验两种实现对编辑请求 SetWebPage 的同一契约：置位时用 Media 替换消息的
// 链接预览，Media 为 nil 即清除；未置位且 Media 为 nil 时预览原样保留。私聊两端副本一致，
// 频道消息落库后可读回。
func EditWebPage(t *testing.T, newFixture func(t *testing.T) EditWebPageFixture) {
	t.Helper()
	ctx := context.Background()
	now := baseNow()
	const oldURL = "https://example.com/old"
	const newURL = "https://example.com/new"

	sendPrivate := func(t *testing.T, f EditWebPageFixture, random int64) domain.SendPrivateTextResult {
		t.Helper()
		sent, err := f.Messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
			SenderUserID:    f.Owner,
			RecipientUserID: f.Recipient,
			RandomID:        random,
			Message:         "see " + oldURL,
			Media:           editWebPageMedia(oldURL, "Old"),
			Date:            now,
		})
		if err != nil {
			t.Fatalf("SendPrivateText: %v", err)
		}
		return sent
	}
	editPrivate := func(t *testing.T, f EditWebPageFixture, sent domain.SendPrivateTextResult, body string, set bool, media *domain.MessageMedia) domain.EditMessageResult {
		t.Helper()
		res, err := f.Messages.EditMessage(ctx, domain.EditMessageRequest{
			OwnerUserID: f.Owner,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: f.Recipient},
			ID:          sent.SenderMessage.ID,
			Message:     body,
			EditDate:    now + 1,
			SetWebPage:  set,
			Media:       media,
		})
		if err != nil {
			t.Fatalf("EditMessage: %v", err)
		}
		return res
	}
	storedPrivate := func(t *testing.T, f EditWebPageFixture, owner int64, id int) domain.Message {
		t.Helper()
		list, err := f.Messages.GetByIDs(ctx, owner, []int{id})
		if err != nil || len(list.Messages) != 1 {
			t.Fatalf("GetByIDs(%d, %d) = %+v, %v", owner, id, list.Messages, err)
		}
		return list.Messages[0]
	}

	t.Run("private: SetWebPage with nil media clears the preview on both copies", func(t *testing.T) {
		f := newFixture(t)
		sent := sendPrivate(t, f, editWebPageRandomBase+1)
		res := editPrivate(t, f, sent, "no link anymore", true, nil)
		if len(res.Edited) != 2 {
			t.Fatalf("edited copies = %d, want 2", len(res.Edited))
		}
		for _, item := range res.Edited {
			if !item.Message.Media.IsZero() {
				t.Fatalf("edit result for %d still has media %+v", item.UserID, item.Message.Media)
			}
		}
		for owner, id := range map[int64]int{f.Owner: sent.SenderMessage.ID, f.Recipient: sent.RecipientMessage.ID} {
			got := storedPrivate(t, f, owner, id)
			if !got.Media.IsZero() || got.Body != "no link anymore" {
				t.Fatalf("stored copy of %d = body %q media %+v, want edited body without media", owner, got.Body, got.Media)
			}
		}
	})

	t.Run("private: SetWebPage with media replaces the preview", func(t *testing.T) {
		f := newFixture(t)
		sent := sendPrivate(t, f, editWebPageRandomBase+2)
		editPrivate(t, f, sent, "see "+newURL, true, editWebPageMedia(newURL, "New"))
		for owner, id := range map[int64]int{f.Owner: sent.SenderMessage.ID, f.Recipient: sent.RecipientMessage.ID} {
			if got := webPageURLOf(storedPrivate(t, f, owner, id).Media); got != newURL {
				t.Fatalf("stored preview url of %d = %q, want %q", owner, got, newURL)
			}
		}
	})

	t.Run("private: without SetWebPage the preview is kept", func(t *testing.T) {
		f := newFixture(t)
		sent := sendPrivate(t, f, editWebPageRandomBase+3)
		editPrivate(t, f, sent, "still "+oldURL, false, nil)
		if got := webPageURLOf(storedPrivate(t, f, f.Recipient, sent.RecipientMessage.ID).Media); got != oldURL {
			t.Fatalf("stored preview url = %q, want kept %q", got, oldURL)
		}
	})

	t.Run("private: clearing the preview with unchanged text is still an edit", func(t *testing.T) {
		f := newFixture(t)
		sent := sendPrivate(t, f, editWebPageRandomBase+4)
		editPrivate(t, f, sent, sent.SenderMessage.Body, true, nil)
		if got := storedPrivate(t, f, f.Owner, sent.SenderMessage.ID); !got.Media.IsZero() {
			t.Fatalf("stored media = %+v, want cleared", got.Media)
		}
	})

	sendChannel := func(t *testing.T, f EditWebPageFixture, random int64) domain.ChannelMessage {
		t.Helper()
		created, err := f.Channels.CreateChannel(ctx, domain.CreateChannelRequest{
			CreatorUserID: f.Owner,
			Title:         "Edit web page contract",
			Megagroup:     true,
			MemberUserIDs: []int64{f.Recipient},
			Date:          now,
		})
		if err != nil {
			t.Fatalf("CreateChannel: %v", err)
		}
		res, err := f.Channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
			UserID:    f.Owner,
			ChannelID: created.Channel.ID,
			RandomID:  random,
			Message:   "see " + oldURL,
			Media:     editWebPageMedia(oldURL, "Old"),
			Date:      now,
		})
		if err != nil {
			t.Fatalf("SendChannelMessage: %v", err)
		}
		return res.Message
	}
	editChannel := func(t *testing.T, f EditWebPageFixture, msg domain.ChannelMessage, body string, set bool, media *domain.MessageMedia) domain.EditChannelMessageResult {
		t.Helper()
		res, err := f.Channels.EditChannelMessage(ctx, domain.EditChannelMessageRequest{
			UserID:     f.Owner,
			ChannelID:  msg.ChannelID,
			ID:         msg.ID,
			Message:    body,
			EditDate:   now + 1,
			SetWebPage: set,
			Media:      media,
		})
		if err != nil {
			t.Fatalf("EditChannelMessage: %v", err)
		}
		return res
	}
	storedChannel := func(t *testing.T, f EditWebPageFixture, msg domain.ChannelMessage) domain.ChannelMessage {
		t.Helper()
		history, err := f.Channels.GetChannelMessages(ctx, f.Recipient, msg.ChannelID, []int{msg.ID})
		if err != nil || len(history.Messages) != 1 {
			t.Fatalf("GetChannelMessages = %+v, %v", history.Messages, err)
		}
		return history.Messages[0]
	}

	t.Run("channel: SetWebPage with nil media clears the preview", func(t *testing.T) {
		f := newFixture(t)
		msg := sendChannel(t, f, editWebPageRandomBase+11)
		res := editChannel(t, f, msg, "no link anymore", true, nil)
		if !res.Message.Media.IsZero() {
			t.Fatalf("edit result media = %+v, want cleared", res.Message.Media)
		}
		if got := storedChannel(t, f, msg); !got.Media.IsZero() || got.Body != "no link anymore" {
			t.Fatalf("stored = body %q media %+v, want edited body without media", got.Body, got.Media)
		}
	})

	t.Run("channel: SetWebPage with media replaces the preview", func(t *testing.T) {
		f := newFixture(t)
		msg := sendChannel(t, f, editWebPageRandomBase+12)
		editChannel(t, f, msg, "see "+newURL, true, editWebPageMedia(newURL, "New"))
		if got := webPageURLOf(storedChannel(t, f, msg).Media); got != newURL {
			t.Fatalf("stored preview url = %q, want %q", got, newURL)
		}
	})

	t.Run("channel: without SetWebPage the preview is kept", func(t *testing.T) {
		f := newFixture(t)
		msg := sendChannel(t, f, editWebPageRandomBase+13)
		editChannel(t, f, msg, "still "+oldURL, false, nil)
		if got := webPageURLOf(storedChannel(t, f, msg).Media); got != oldURL {
			t.Fatalf("stored preview url = %q, want kept %q", got, oldURL)
		}
	})

	t.Run("channel: clearing the preview with unchanged text is still an edit", func(t *testing.T) {
		f := newFixture(t)
		msg := sendChannel(t, f, editWebPageRandomBase+14)
		editChannel(t, f, msg, msg.Body, true, nil)
		if got := storedChannel(t, f, msg); !got.Media.IsZero() {
			t.Fatalf("stored media = %+v, want cleared", got.Media)
		}
	})
}
