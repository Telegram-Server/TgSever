package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// 编辑路径的链接预览接线：规则矩阵在 TestDecideEditedWebPage，这里只证明三个入口接上了。

func cachedDoneWebPage(u string) (domain.MessageWebPage, bool) {
	return domain.MessageWebPage{State: domain.MessageWebPageStateDone, ID: domain.WebPageURLHash(u), URL: u, DisplayURL: "example.com", Title: "Cached"}, true
}

func editedChannelMessageFromUpdates(t *testing.T, updates tg.UpdatesClass) *tg.Message {
	t.Helper()
	upd, ok := updates.(*tg.Updates)
	if !ok {
		t.Fatalf("expected *tg.Updates, got %T", updates)
	}
	for _, u := range upd.Updates {
		if em, ok := u.(*tg.UpdateEditChannelMessage); ok {
			msg, ok := em.Message.(*tg.Message)
			if !ok {
				t.Fatalf("expected *tg.Message, got %T", em.Message)
			}
			return msg
		}
	}
	t.Fatal("no updateEditChannelMessage found")
	return nil
}

func TestEditMessageRemovingLinkDropsWebPage(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	f := r.deps.Files.(*fakeFiles)
	f.webPagePreviewOn = true
	f.lookupWebPageFn = cachedDoneWebPage
	peer := &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash}

	sent, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer: peer, Message: wpTestMessage, Entities: wpURLEntities(), RandomID: 5301,
	})
	if err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	msg := newMessageFromUpdates(t, sent)
	if _, ok := msg.Media.(*tg.MessageMediaWebPage); !ok {
		t.Fatalf("sent media = %T, want web page preview", msg.Media)
	}

	edited, err := r.onMessagesEditMessage(WithUserID(ctx, owner.ID), editTextRequest(peer, msg.ID, "no link anymore"))
	if err != nil {
		t.Fatalf("editMessage: %v", err)
	}
	if got := editedMessageFromUpdates(t, edited); got.Media != nil {
		t.Fatalf("edited media = %T, want no web page preview", got.Media)
	}
}

func TestEditChannelMessageRemovingLinkDropsWebPage(t *testing.T) {
	ctx := context.Background()
	r, owner, channel := newRichChannelTestRouter(t)
	f := r.deps.Files.(*fakeFiles)
	f.webPagePreviewOn = true
	f.lookupWebPageFn = cachedDoneWebPage
	peer := &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}

	sent, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer: peer, Message: wpTestMessage, Entities: wpURLEntities(), RandomID: 5302,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	msg := newMessageFromUpdates(t, sent)
	if _, ok := msg.Media.(*tg.MessageMediaWebPage); !ok {
		t.Fatalf("sent media = %T, want web page preview", msg.Media)
	}

	edited, err := r.onMessagesEditMessage(WithUserID(ctx, owner.ID), editTextRequest(peer, msg.ID, "no link anymore"))
	if err != nil {
		t.Fatalf("edit channel message: %v", err)
	}
	if got := editedChannelMessageFromUpdates(t, edited); got.Media != nil {
		t.Fatalf("edited media = %T, want no web page preview", got.Media)
	}
}

func TestEditMessageChangingLinkResolvesNewWebPage(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	f := r.deps.Files.(*fakeFiles)
	f.webPagePreviewOn = true
	f.lookupWebPageFn = cachedDoneWebPage
	peer := &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash}

	sent, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer: peer, Message: wpTestMessage, Entities: wpURLEntities(), RandomID: 5303,
	})
	if err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	msg := newMessageFromUpdates(t, sent)

	// 新链接不在缓存里：编辑挂 pending，异步解析回写 done。
	const newURL = "https://example.org/fresh"
	f.lookupWebPageFn = nil
	f.resolveWebPageFn = func(u string) (domain.MessageWebPage, error) {
		return domain.MessageWebPage{State: domain.MessageWebPageStateDone, ID: domain.WebPageURLHash(u), URL: u, DisplayURL: "example.org", Title: "Fresh"}, nil
	}
	if _, err := r.onMessagesEditMessage(WithUserID(ctx, owner.ID), editTextRequest(peer, msg.ID, "now "+newURL)); err != nil {
		t.Fatalf("editMessage: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		got, found, err := r.lookupOwnerMessage(ctx, owner.ID, msg.ID)
		if err == nil && found && got.Media != nil && got.Media.WebPage != nil &&
			got.Media.WebPage.State == domain.MessageWebPageStateDone && got.Media.WebPage.URL == newURL {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("edited message preview = %+v, want resolved card for %s", got.Media, newURL)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func editTextRequest(peer tg.InputPeerClass, id int, message string) *tg.MessagesEditMessageRequest {
	req := &tg.MessagesEditMessageRequest{Peer: peer, ID: id}
	req.SetMessage(message)
	return req
}
