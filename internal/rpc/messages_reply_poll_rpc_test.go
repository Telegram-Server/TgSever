package rpc

import (
	"bytes"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

func TestNativeReplyTargetsActualPollOption(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	source, err := f.router.BotAPISendPollWithOptions(f.ctx, f.bot.ID, f.chatID, botAPITestPoll("Lunch?", "pizza", "sushi"), nil, "", nil, nil, false, false, 0, domain.BotAPIMessageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	option := source.Media.Poll.Answers[1].Option
	reply := &tg.InputReplyToMessage{ReplyToMsgID: source.ID}
	reply.SetPollOption(option)
	request := &tg.MessagesSendMessageRequest{Peer: &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}, Message: "I mean this choice", RandomID: 47001}
	request.SetReplyTo(reply)
	updates, err := f.router.onMessagesSendMessage(WithUserID(f.ctx, f.member.ID), request)
	if err != nil {
		t.Fatal(err)
	}
	box, ok := updates.(*tg.Updates)
	if !ok {
		t.Fatalf("reply response=%T", updates)
	}
	found := false
	for _, update := range box.Updates {
		created, ok := update.(*tg.UpdateNewChannelMessage)
		if !ok {
			continue
		}
		message, ok := created.Message.(*tg.Message)
		if !ok {
			t.Fatalf("reply message=%T", created.Message)
		}
		header, ok := message.ReplyTo.(*tg.MessageReplyHeader)
		if !ok {
			t.Fatalf("reply header=%T", message.ReplyTo)
		}
		actual, present := header.GetPollOption()
		if !present || !bytes.Equal(actual, option) {
			t.Fatalf("poll option=%x, want %x", actual, option)
		}
		stored, err := f.router.botAPIReloadMessage(f.ctx, f.member.ID, domain.Peer{Type: domain.PeerTypeChannel, ID: f.channel.ID}, message.ID)
		if err != nil || stored.ReplyTo == nil || !bytes.Equal(stored.ReplyTo.PollOption, option) {
			t.Fatalf("stored poll reply=%+v %v", stored, err)
		}
		found = true
	}
	if !found {
		t.Fatal("no channel reply delivered")
	}
	invalidReply := *reply
	invalidReply.SetPollOption(append(append([]byte(nil), option...), 0xff))
	invalid := *request
	invalid.RandomID = 47002
	invalid.SetReplyTo(&invalidReply)
	if _, err := f.router.onMessagesSendMessage(WithUserID(f.ctx, f.member.ID), &invalid); err == nil {
		t.Fatal("unknown option on a real source poll accepted")
	}
}
