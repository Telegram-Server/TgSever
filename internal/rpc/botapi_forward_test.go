package rpc

import (
	"strings"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// memberPostsInGroup 让 member 在群里发一条文本，返回其频道消息 id。
func (f botAPIBridgeFixture) memberPostsInGroup(t *testing.T, text string, randomID int64) int {
	t.Helper()
	updates, err := f.router.onMessagesSendMessage(WithUserID(f.ctx, f.member.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		Message:  text,
		RandomID: randomID,
	})
	if err != nil {
		t.Fatalf("member send: %v", err)
	}
	ids := botAPIUpdateMessageIDs(domain.Peer{Type: domain.PeerTypeChannel, ID: f.channel.ID}, updates)
	if len(ids) != 1 {
		t.Fatalf("member send ids = %v", ids)
	}
	return ids[0]
}

// memberMessagesBot 让 member 私聊机器人，返回这条消息在机器人消息盒里的 id。
func (f botAPIBridgeFixture) memberMessagesBot(t *testing.T, text string, randomID int64) int {
	t.Helper()
	if _, err := f.router.onMessagesSendMessage(WithUserID(f.ctx, f.member.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash},
		Message:  text,
		RandomID: randomID,
	}); err != nil {
		t.Fatalf("member -> bot: %v", err)
	}
	list, err := f.messages.GetHistory(f.ctx, f.bot.ID, domain.MessageFilter{HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.member.ID}})
	if err != nil {
		t.Fatalf("bot history: %v", err)
	}
	for _, msg := range list.Messages {
		if msg.Body == text {
			return msg.ID
		}
	}
	t.Fatalf("bot history %+v lacks %q", list.Messages, text)
	return 0
}

func TestBotAPIForwardMessagesFromPrivateChatIntoGroup(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	src := f.memberMessagesBot(t, "from dm", 9202)

	msgs, err := f.router.BotAPIForwardMessages(f.ctx, f.bot.ID, f.chatID, f.member.ID, []int{src}, false, false)
	if err != nil {
		t.Fatalf("BotAPIForwardMessages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Body != "from dm" || msgs[0].Peer != (domain.Peer{Type: domain.PeerTypeChannel, ID: f.channel.ID}) {
		t.Fatalf("messages = %+v", msgs)
	}
	if fwd := msgs[0].Forward; fwd == nil || fwd.From != (domain.Peer{Type: domain.PeerTypeUser, ID: f.member.ID}) {
		t.Fatalf("forward = %+v", msgs[0].Forward)
	}
	// 同一 id 换成 owner 的私聊：机器人盒里这条消息不属于该会话，必须拒绝。
	if _, err := f.router.BotAPIForwardMessages(f.ctx, f.bot.ID, f.chatID, f.owner.ID, []int{src}, false, false); err == nil {
		t.Fatal("forwarded a message from a chat it does not belong to")
	}
}

func TestBotAPIForwardMessagesSkipsMissingIDs(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	first := f.memberPostsInGroup(t, "one", 9203)
	second := f.memberPostsInGroup(t, "two", 9204)

	msgs, err := f.router.BotAPIForwardMessages(f.ctx, f.bot.ID, f.chatID, f.chatID, []int{first, 99999, second}, false, false)
	if err != nil {
		t.Fatalf("BotAPIForwardMessages: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Body != "one" || msgs[1].Body != "two" {
		t.Fatalf("messages = %+v, want the two existing messages in order", msgs)
	}
	if _, err := f.router.BotAPIForwardMessages(f.ctx, f.bot.ID, f.chatID, f.chatID, []int{99999}, false, false); err == nil ||
		!strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("forward of only missing ids err = %v, want MESSAGE_ID_INVALID", err)
	}
}

// groupServiceMessageID 返回群里机器人可见的一条服务消息（建群/拉人）的 id。
func (f botAPIBridgeFixture) groupServiceMessageID(t *testing.T) int {
	t.Helper()
	history, err := f.channels.GetMessages(f.ctx, f.bot.ID, f.channel.ID, []int{1, 2, 3, 4, 5})
	if err != nil {
		t.Fatalf("group history: %v", err)
	}
	for _, msg := range history.Messages {
		if msg.Action != nil {
			return msg.ID
		}
	}
	t.Skip("fixture group has no visible service message")
	return 0
}

func TestBotAPIForwardMessagesRejectsServiceAndProtectedMessages(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	service := f.groupServiceMessageID(t)
	if _, err := f.router.BotAPIForwardMessages(f.ctx, f.bot.ID, f.member.ID, f.chatID, []int{service}, false, false); err == nil ||
		!strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("forward service message err = %v, want MESSAGE_ID_INVALID", err)
	}

	updates, err := f.router.onMessagesSendMessage(WithUserID(f.ctx, f.owner.ID), &tg.MessagesSendMessageRequest{
		Peer:       &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		Message:    "secret",
		RandomID:   9205,
		Noforwards: true,
	})
	if err != nil {
		t.Fatalf("owner protected send: %v", err)
	}
	ids := botAPIUpdateMessageIDs(domain.Peer{Type: domain.PeerTypeChannel, ID: f.channel.ID}, updates)
	if _, err := f.router.BotAPIForwardMessages(f.ctx, f.bot.ID, f.chatID, f.chatID, ids, false, false); err == nil ||
		!strings.Contains(err.Error(), "CHAT_FORWARDS_RESTRICTED") {
		t.Fatalf("forward protected message err = %v, want CHAT_FORWARDS_RESTRICTED", err)
	}
}

func TestBotAPICopyMessagesDropsForwardHeader(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	src := f.memberPostsInGroup(t, "copy me", 9206)

	msgs, err := f.router.BotAPICopyMessages(f.ctx, f.bot.ID, f.chatID, f.chatID, []int{src}, false, false, false)
	if err != nil {
		t.Fatalf("BotAPICopyMessages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Body != "copy me" || msgs[0].Forward != nil ||
		msgs[0].From != (domain.Peer{Type: domain.PeerTypeUser, ID: f.bot.ID}) {
		t.Fatalf("messages = %+v, want a header-less copy sent by the bot", msgs)
	}
}

// memberPostsPhoto 直接经 channels 服务写一条带说明文字的图片消息（夹具没有文件服务）。
func (f botAPIBridgeFixture) memberPostsPhoto(t *testing.T, caption string, randomID int64) int {
	t.Helper()
	res, err := f.channels.SendMessage(f.ctx, f.member.ID, domain.SendChannelMessageRequest{
		UserID:    f.member.ID,
		ChannelID: f.channel.ID,
		RandomID:  randomID,
		Message:   caption,
		Entities:  []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 0, Length: 1}},
		Media:     &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 7701, AccessHash: 7702}},
		Date:      30,
	})
	if err != nil {
		t.Fatalf("member photo: %v", err)
	}
	return res.Message.ID
}

func TestBotAPICopyMessagesRemoveCaptionKeepsMediaAndText(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	photo := f.memberPostsPhoto(t, "pic caption", 9207)
	text := f.memberPostsInGroup(t, "plain text", 9208)

	msgs, err := f.router.BotAPICopyMessages(f.ctx, f.bot.ID, f.chatID, f.chatID, []int{photo, text}, true, false, false)
	if err != nil {
		t.Fatalf("BotAPICopyMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %+v", msgs)
	}
	if msgs[0].Body != "" || len(msgs[0].Entities) != 0 || msgs[0].Media == nil || msgs[0].Media.Photo == nil ||
		msgs[0].Media.Photo.ID != 7701 || msgs[0].Forward != nil {
		t.Fatalf("photo copy = %+v, want the photo without caption or header", msgs[0])
	}
	if msgs[1].Body != "plain text" || msgs[1].Forward != nil {
		t.Fatalf("text copy = %+v, want the text kept", msgs[1])
	}
}

// remove_caption 走 drop_author+drop_media_captions 转发：相册不被拆开，共享新的 grouped_id。
func TestBotAPICopyMessagesRemoveCaptionKeepsAlbumGrouped(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	ids := make([]int, 0, 2)
	for i, caption := range []string{"first caption", ""} {
		res, err := f.channels.SendMessage(f.ctx, f.member.ID, domain.SendChannelMessageRequest{
			UserID:    f.member.ID,
			ChannelID: f.channel.ID,
			RandomID:  int64(9220 + i),
			Message:   caption,
			Media:     &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: int64(7710 + i), AccessHash: 1}},
			GroupedID: 5501,
			Date:      32,
		})
		if err != nil {
			t.Fatalf("member album item: %v", err)
		}
		ids = append(ids, res.Message.ID)
	}

	msgs, err := f.router.BotAPICopyMessages(f.ctx, f.bot.ID, f.member.ID, f.chatID, ids, true, false, false)
	if err != nil {
		t.Fatalf("BotAPICopyMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %+v", msgs)
	}
	if msgs[0].GroupedID == 0 || msgs[0].GroupedID != msgs[1].GroupedID {
		t.Fatalf("grouped ids = %d/%d, want one shared album id", msgs[0].GroupedID, msgs[1].GroupedID)
	}
	for _, msg := range msgs {
		if msg.Body != "" || msg.Forward != nil || msg.Media == nil || msg.Media.Photo == nil {
			t.Fatalf("copy = %+v, want captionless photo without header", msg)
		}
	}
}

func TestBotAPICopyMessageReplacesCaptionAndAddsButtons(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	photo := f.memberPostsPhoto(t, "old caption", 9209)
	markup := &domain.MessageReplyMarkup{Type: domain.MessageReplyMarkupInline, Inline: [][]domain.MarkupButton{{{
		Type: domain.MarkupButtonCallback, Text: "go", Data: []byte("x"),
	}}}}

	msg, err := f.router.BotAPICopyMessage(f.ctx, f.bot.ID, f.member.ID, f.chatID, photo, true, "new caption",
		[]domain.MessageEntity{{Type: domain.MessageEntityItalic, Offset: 0, Length: 3}}, markup, false, false, 0)
	if err != nil {
		t.Fatalf("BotAPICopyMessage: %v", err)
	}
	if msg.Peer != (domain.Peer{Type: domain.PeerTypeUser, ID: f.member.ID}) || msg.Body != "new caption" || msg.Forward != nil ||
		len(msg.Entities) != 1 || msg.Entities[0].Type != domain.MessageEntityItalic ||
		msg.Media == nil || msg.Media.Photo == nil || msg.Media.Photo.ID != 7701 {
		t.Fatalf("copy = %+v", msg)
	}
	if msg.ReplyMarkup == nil || len(msg.ReplyMarkup.Inline) != 1 {
		t.Fatalf("reply markup = %+v", msg.ReplyMarkup)
	}
}

func TestBotAPICopyMessagePlainCopyRepliesWithoutHeader(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	anchor := f.memberPostsInGroup(t, "anchor", 9210)
	src := f.memberMessagesBot(t, "dm text", 9211)

	msg, err := f.router.BotAPICopyMessage(f.ctx, f.bot.ID, f.chatID, f.member.ID, src, false, "", nil, nil, false, false, anchor)
	if err != nil {
		t.Fatalf("BotAPICopyMessage: %v", err)
	}
	if msg.Body != "dm text" || msg.Forward != nil || msg.ReplyTo == nil || msg.ReplyTo.MessageID != anchor {
		t.Fatalf("copy = %+v reply=%+v", msg, msg.ReplyTo)
	}
}

func TestBotAPICopyMessageRefusesUncopyableMessages(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	service := f.groupServiceMessageID(t)
	if _, err := f.router.BotAPICopyMessage(f.ctx, f.bot.ID, f.member.ID, f.chatID, service, false, "", nil, nil, false, false, 0); err == nil ||
		!strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("copy service message err = %v", err)
	}

	res, err := f.channels.SendMessage(f.ctx, f.owner.ID, domain.SendChannelMessageRequest{
		UserID: f.owner.ID, ChannelID: f.channel.ID, RandomID: 9212, Message: "secret photo", NoForwards: true,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 7703, AccessHash: 1}}, Date: 31,
	})
	if err != nil {
		t.Fatalf("owner protected photo: %v", err)
	}
	// 改说明文字走重新发送路径，同样不能绕过 no-forwards。
	if _, err := f.router.BotAPICopyMessage(f.ctx, f.bot.ID, f.member.ID, f.chatID, res.Message.ID, true, "leak", nil, nil, false, false, 0); err == nil ||
		!strings.Contains(err.Error(), "CHAT_FORWARDS_RESTRICTED") {
		t.Fatalf("copy protected message err = %v", err)
	}
}

func TestBotAPISourceCopyableRejectsInvoicesAndGiveaways(t *testing.T) {
	for _, src := range []botAPISource{
		{service: true},
		{media: &domain.MessageMedia{Kind: domain.MessageMediaKindInvoice}},
		{media: &domain.MessageMedia{Kind: domain.MessageMediaKindGiveaway}},
	} {
		if botAPISourceCopyable(src) {
			t.Fatalf("%+v reported copyable", src)
		}
	}
	if !botAPISourceCopyable(botAPISource{media: &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto}}) {
		t.Fatal("photo reported uncopyable")
	}
}

func TestBotAPIForwardMessagesInGroupCarriesForwardHeader(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	src := f.memberPostsInGroup(t, "original", 9201)

	msgs, err := f.router.BotAPIForwardMessages(f.ctx, f.bot.ID, f.chatID, f.chatID, []int{src}, false, false)
	if err != nil {
		t.Fatalf("BotAPIForwardMessages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].ID == src || msgs[0].Body != "original" || !msgs[0].Out {
		t.Fatalf("messages = %+v", msgs)
	}
	if fwd := msgs[0].Forward; fwd == nil || fwd.From != (domain.Peer{Type: domain.PeerTypeUser, ID: f.member.ID}) {
		t.Fatalf("forward = %+v, want the member as origin", msgs[0].Forward)
	}
}
