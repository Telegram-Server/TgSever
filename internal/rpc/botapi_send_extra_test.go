package rpc

import (
	"strconv"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func containsErr(err error, code string) bool {
	return err != nil && strings.Contains(err.Error(), code)
}

func TestBotAPISendChatActionPushesTypingToPrivatePeer(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	ok, err := f.router.BotAPISendChatAction(f.ctx, f.bot.ID, f.member.ID, "upload_photo", 0)
	if err != nil || !ok {
		t.Fatalf("BotAPISendChatAction = %v, %v", ok, err)
	}
	short, isShort := f.sessions.lastUserPush().(*tg.UpdateShort)
	if !isShort {
		t.Fatalf("push = %#v, want UpdateShort", f.sessions.lastUserPush())
	}
	typing, isTyping := short.Update.(*tg.UpdateUserTyping)
	if !isTyping || typing.UserID != f.bot.ID {
		t.Fatalf("update = %#v", short.Update)
	}
	if _, isUpload := typing.Action.(*tg.SendMessageUploadPhotoAction); !isUpload {
		t.Fatalf("action = %#v", typing.Action)
	}
}

func TestBotAPISendChatActionInGroupAndUnknownAction(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	if ok, err := f.router.BotAPISendChatAction(f.ctx, f.bot.ID, f.chatID, "typing", 0); err != nil || !ok {
		t.Fatalf("group typing = %v, %v", ok, err)
	}
	if _, err := f.router.BotAPISendChatAction(f.ctx, f.bot.ID, f.chatID, "dancing", 0); !containsErr(err, "ACTION_INVALID") {
		t.Fatalf("err = %v, want ACTION_INVALID", err)
	}
}

func botAPITestPoll(question string, answers ...string) domain.MessagePoll {
	poll := domain.MessagePoll{Question: question}
	for _, text := range answers {
		poll.Answers = append(poll.Answers, domain.MessagePollAnswer{Text: text})
	}
	return poll
}

func TestBotAPISendPollQuizInGroup(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	poll := botAPITestPoll("2+2?", "3", "4", "5")
	poll.Quiz, poll.PublicVoters, poll.ClosePeriod = true, true, 60

	msg, err := f.router.BotAPISendPoll(f.ctx, f.bot.ID, f.chatID, poll, []int{1}, "basic math", nil, nil, false, false, 0)
	if err != nil {
		t.Fatalf("BotAPISendPoll: %v", err)
	}
	if msg.ID == 0 || msg.Media == nil || msg.Media.Poll == nil {
		t.Fatalf("message = %+v", msg)
	}
	got := msg.Media.Poll
	if got.Question != "2+2?" || !got.Quiz || !got.PublicVoters || got.ClosePeriod != 60 || got.CloseDate == 0 ||
		len(got.Answers) != 3 || got.Answers[1].Text != "4" || got.ID == 0 || got.Closed {
		t.Fatalf("poll = %+v", got)
	}
}

func TestBotAPISendMediaGroupSharesGroupedIDInGroup(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	stored, err := f.files.CreatePhotoFromBytes(f.ctx, []byte("stored"))
	if err != nil {
		t.Fatalf("seed photo: %v", err)
	}
	items := []BotAPIInputMediaItem{
		{Kind: "photo", File: domain.BotAPIFileInput{Bytes: []byte("png"), FileName: "a.png"}, Caption: "first"},
		{Kind: "video", File: domain.BotAPIFileInput{Bytes: []byte("mp4"), MimeType: "video/mp4", Width: 640, Height: 360, Duration: 3}},
		// 已存储的照片以 file_id 引用：InputMediaPhoto 只带 id，没有 file_reference。
		{Kind: "photo", File: domain.BotAPIFileInput{LocationKey: "photo:" + strconv.FormatInt(stored.ID, 10) + ":x"}},
	}

	msgs, err := f.router.BotAPISendMediaGroup(f.ctx, f.bot.ID, f.chatID, items, false, false, 0)
	if err != nil {
		t.Fatalf("BotAPISendMediaGroup: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("messages = %+v, want 3", msgs)
	}
	for i, msg := range msgs {
		if msg.GroupedID == 0 || msg.GroupedID != msgs[0].GroupedID || !msg.Out || msg.Media == nil {
			t.Fatalf("message %d = %+v, want one album", i, msg)
		}
	}
	if msgs[0].Body != "first" || msgs[0].Media.Photo == nil || msgs[1].Media.Document == nil || !msgs[1].Media.Video ||
		msgs[2].Media.Photo == nil || msgs[2].Media.Photo.ID != stored.ID {
		t.Fatalf("album = %+v / %+v / %+v", msgs[0].Media, msgs[1].Media, msgs[2].Media)
	}
	if msgs[0].ID >= msgs[1].ID || msgs[1].ID >= msgs[2].ID {
		t.Fatalf("ids = %d %d %d, want send order", msgs[0].ID, msgs[1].ID, msgs[2].ID)
	}
}

func TestBotAPISendMediaGroupRejectsUnknownFileID(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	items := []BotAPIInputMediaItem{
		{Kind: "document", File: domain.BotAPIFileInput{Bytes: []byte("a"), FileName: "a.txt", MimeType: "text/plain"}},
		{Kind: "document", File: domain.BotAPIFileInput{LocationKey: "doc:987654"}},
	}
	if _, err := f.router.BotAPISendMediaGroup(f.ctx, f.bot.ID, f.member.ID, items, false, false, 0); !containsErr(err, "FILE_ID_INVALID") {
		t.Fatalf("err = %v, want FILE_ID_INVALID", err)
	}
}

func TestBotAPISendDiceInGroup(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	msg, err := f.router.BotAPISendDice(f.ctx, f.bot.ID, f.chatID, "🎯", nil, false, false, 0)
	if err != nil {
		t.Fatalf("BotAPISendDice: %v", err)
	}
	if msg.ID == 0 || msg.Peer != (domain.Peer{Type: domain.PeerTypeChannel, ID: f.channel.ID}) || !msg.Out ||
		msg.Media == nil || msg.Media.Dice == nil || msg.Media.Dice.Emoticon != "🎯" ||
		msg.Media.Dice.Value < 1 || msg.Media.Dice.Value > 6 {
		t.Fatalf("message = %+v media = %+v", msg, msg.Media)
	}
}
