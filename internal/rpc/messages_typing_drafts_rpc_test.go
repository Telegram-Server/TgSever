package rpc

import (
	"context"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"
	appbots "telesrv/internal/app/bots"
	appcontacts "telesrv/internal/app/contacts"
	appstories "telesrv/internal/app/stories"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func typingDraftRouter(t *testing.T) (*Router, domain.User, domain.User, *captureSessions) {
	t.Helper()
	ctx := context.Background()
	users := memory.NewUserStore()
	owner, err := users.Create(ctx, domain.User{AccessHash: 201, Phone: "15550005101", FirstName: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	bots := memory.NewBotStore(users)
	bot, _, err := bots.CreateBotAccount(ctx, domain.User{AccessHash: 202, FirstName: "DraftBot", Username: "draft_bot"}, domain.BotProfile{OwnerUserID: owner.ID, TokenSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	contacts := memory.NewContactStore()
	stories := memory.NewStoryStore()
	rpcTestBlocklistStores(contacts, users, stories, memory.NewUpdateEventStore())
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Users:         appusers.NewService(users),
		Bots:          appbots.NewService(users, bots, memory.NewMessageStore()),
		Contacts:      appcontacts.NewService(contacts, users),
		Stories:       appstories.NewService(stories),
		Sessions:      sessions,
		MessageDrafts: memory.NewMessageDraftStore(),
	}, zaptest.NewLogger(t), clock.System)
	return r, bot, owner, sessions
}

func receivedTypingAction(t *testing.T, sessions *captureSessions, senderID, recipientID int64) tg.SendMessageActionClass {
	t.Helper()
	got := sessions.snapshot()
	if got.userID != recipientID {
		t.Fatalf("typing recipient = %d, want %d", got.userID, recipientID)
	}
	short, ok := got.message.(*tg.UpdateShort)
	if !ok {
		t.Fatalf("received update = %T", got.message)
	}
	// Decode the actual outbound wire value, not the inbound request object.
	var wire bin.Buffer
	if err := short.Encode(&wire); err != nil {
		t.Fatal(err)
	}
	var decoded tg.UpdateShort
	if err := decoded.Decode(&wire); err != nil {
		t.Fatal(err)
	}
	typing, ok := decoded.Update.(*tg.UpdateUserTyping)
	if !ok || typing.UserID != senderID {
		t.Fatalf("typing update = %#v", decoded.Update)
	}
	return typing.Action
}

func TestMessagesSetTypingDraftConvertsRichContent(t *testing.T) {
	for _, source := range []struct {
		name  string
		input tg.InputRichMessageClass
	}{
		{"HTML", &tg.InputRichMessageHTML{HTML: "<p>Hello <b>world</b></p>", Noautolink: true}},
		{"Markdown", &tg.InputRichMessageMarkdown{Markdown: "Hello **world**", Noautolink: true}},
	} {
		t.Run(source.name, func(t *testing.T) {
			r, bot, owner, sessions := typingDraftRouter(t)
			ok, err := r.onMessagesSetTyping(WithUserID(context.Background(), bot.ID), &tg.MessagesSetTypingRequest{
				Peer:   inputPeerUser(owner),
				Action: &tg.InputSendMessageRichMessageDraftAction{RandomID: 71, CanStop: true, KeepOnStop: true, RichMessage: source.input},
			})
			if err != nil || !ok {
				t.Fatalf("rich draft = %v, %v", ok, err)
			}
			action, ok := receivedTypingAction(t, sessions, bot.ID, owner.ID).(*tg.SendMessageRichMessageDraftAction)
			if !ok || action.RandomID != 71 || !action.CanStop || !action.KeepOnStop {
				t.Fatalf("received action = %#v", action)
			}
			if len(action.RichMessage.Blocks) != 1 {
				t.Fatalf("rich blocks = %#v", action.RichMessage.Blocks)
			}
			paragraph, ok := action.RichMessage.Blocks[0].(*tg.PageBlockParagraph)
			if !ok {
				t.Fatalf("rich paragraph = %T", action.RichMessage.Blocks[0])
			}
			if text := botAPIRichPlainText(paragraph.Text); text != "Hello world" {
				t.Fatalf("parsed draft text = %q", text)
			}
			projection, err := botAPIRichMessageProjection(action.RichMessage.Blocks, false)
			if err != nil || !strings.Contains(string(projection), `"type":"bold"`) || !strings.Contains(string(projection), `"world"`) {
				t.Fatalf("parsed draft formatting = %s, %v", projection, err)
			}
		})
	}
}

func TestMessagesSetTypingDraftValidatesBeforeDelivery(t *testing.T) {
	var nilBold *tg.MessageEntityBold
	for _, tc := range []struct {
		name   string
		action tg.SendMessageActionClass
		want   string
	}{
		{"zero random id", &tg.SendMessageTextDraftAction{Text: tg.TextWithEntities{Text: "hello"}}, "RANDOM_ID_EMPTY"},
		{"too long", &tg.SendMessageTextDraftAction{RandomID: 1, Text: tg.TextWithEntities{Text: strings.Repeat("界", 4097)}}, "MESSAGE_TOO_LONG"},
		{"invalid utf8", &tg.SendMessageTextDraftAction{RandomID: 1, Text: tg.TextWithEntities{Text: "\xff"}}, "MESSAGE_TOO_LONG"},
		{"bad entity bounds", &tg.SendMessageTextDraftAction{RandomID: 1, Text: tg.TextWithEntities{Text: "hello", Entities: []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 4, Length: 2}}}}, "ENTITY_BOUNDS_INVALID"},
		{"surrogate split", &tg.SendMessageTextDraftAction{RandomID: 1, Text: tg.TextWithEntities{Text: "😀", Entities: []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 1, Length: 1}}}}, "ENTITY_BOUNDS_INVALID"},
		{"nil entity", &tg.SendMessageTextDraftAction{RandomID: 1, Text: tg.TextWithEntities{Text: "hello", Entities: []tg.MessageEntityClass{nilBold}}}, "ENTITY_BOUNDS_INVALID"},
		{"invalid rich", &tg.InputSendMessageRichMessageDraftAction{RandomID: 1, RichMessage: &tg.InputRichMessageHTML{}}, "RICH_MESSAGE_INVALID"},
		{"rich zero random id", &tg.InputSendMessageRichMessageDraftAction{RichMessage: &tg.InputRichMessageHTML{HTML: "<p>Hello</p>"}}, "RANDOM_ID_EMPTY"},
		{"server constructor", &tg.SendMessageRichMessageDraftAction{RandomID: 1}, "INPUT_CONSTRUCTOR_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, bot, owner, sessions := typingDraftRouter(t)
			ok, err := r.onMessagesSetTyping(WithUserID(context.Background(), bot.ID), &tg.MessagesSetTypingRequest{Peer: inputPeerUser(owner), Action: tc.action})
			if ok || !tgerr.Is(err, tc.want) {
				t.Fatalf("invalid draft = %v, %v, want %s", ok, err, tc.want)
			}
			if got := sessions.snapshot().message; got != nil {
				t.Fatalf("invalid draft delivered: %T", got)
			}
		})
	}
}

func TestMessagesSetTypingDraftBotOnlyAndOrdinaryTyping(t *testing.T) {
	// TDLib Requests.cpp on_request(sendTextMessageDraft/sendRichMessageDraft)
	// both call CHECK_IS_BOT(); stopPendingMessage instead calls CHECK_IS_USER().
	// https://github.com/tdlib/td/blob/master/td/telegram/Requests.cpp
	r, bot, owner, sessions := typingDraftRouter(t)
	ctx := WithUserID(context.Background(), owner.ID)
	for _, action := range []tg.SendMessageActionClass{
		&tg.SendMessageTextDraftAction{RandomID: 9, Text: tg.TextWithEntities{Text: "hello"}},
		&tg.InputSendMessageRichMessageDraftAction{RandomID: 9, RichMessage: &tg.InputRichMessageHTML{HTML: "<p>Hello</p>"}},
	} {
		if ok, err := r.onMessagesSetTyping(ctx, &tg.MessagesSetTypingRequest{Peer: inputPeerUser(bot), Action: action}); ok || !tgerr.Is(err, "USER_BOT_REQUIRED") {
			t.Fatalf("human draft = %v, %v", ok, err)
		}
	}
	if sessions.snapshot().message != nil {
		t.Fatal("human draft delivered")
	}
	if ok, err := r.onMessagesSetTyping(ctx, &tg.MessagesSetTypingRequest{Peer: inputPeerUser(bot), Action: &tg.SendMessageTypingAction{}}); err != nil || !ok {
		t.Fatalf("ordinary typing = %v, %v", ok, err)
	}
	if action := receivedTypingAction(t, sessions, owner.ID, bot.ID); action.TypeID() != tg.SendMessageTypingActionTypeID {
		t.Fatalf("ordinary typing = %T", action)
	}
}

func TestMessagesSetTypingDraftTextBoundaryAndBlocklist(t *testing.T) {
	r, bot, owner, sessions := typingDraftRouter(t)
	ctx := WithUserID(context.Background(), bot.ID)
	text := "😀" + strings.Repeat("界", 4095)
	request := &tg.MessagesSetTypingRequest{Peer: inputPeerUser(owner), Action: &tg.SendMessageTextDraftAction{
		RandomID: 91, CanStop: true, KeepOnStop: true,
		Text: tg.TextWithEntities{Text: text, Entities: []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 0, Length: 2}}},
	}}
	if ok, err := r.onMessagesSetTyping(ctx, request); err != nil || !ok {
		t.Fatalf("valid text draft = %v, %v", ok, err)
	}
	draft, ok := receivedTypingAction(t, sessions, bot.ID, owner.ID).(*tg.SendMessageTextDraftAction)
	if !ok || draft.RandomID != 91 || draft.Text.Text != text || !draft.CanStop || !draft.KeepOnStop || len(draft.Text.Entities) != 1 {
		t.Fatalf("received draft = %#v", draft)
	}
	if bold, ok := draft.Text.Entities[0].(*tg.MessageEntityBold); !ok || bold.Offset != 0 || bold.Length != 2 {
		t.Fatalf("received entity = %#v", draft.Text.Entities[0])
	}
	// A sender blocking the recipient must not suppress the recipient's updates;
	// the delivery guard checks the recipient's block list, not the sender's.
	if ok, err := r.onContactsBlock(ctx, &tg.ContactsBlockRequest{ID: inputPeerUser(owner)}); err != nil || !ok {
		t.Fatalf("sender block = %v, %v", ok, err)
	}
	sessions.clearMessages()
	if ok, err := r.onMessagesSetTyping(ctx, request); err != nil || !ok {
		t.Fatalf("draft after sender block = %v, %v", ok, err)
	}
	receivedTypingAction(t, sessions, bot.ID, owner.ID)
	if ok, err := r.onContactsBlock(WithUserID(context.Background(), owner.ID), &tg.ContactsBlockRequest{ID: inputPeerUser(bot)}); err != nil || !ok {
		t.Fatalf("recipient block = %v, %v", ok, err)
	}
	for _, action := range []tg.SendMessageActionClass{
		request.Action,
		&tg.InputSendMessageRichMessageDraftAction{RandomID: 92, RichMessage: &tg.InputRichMessageHTML{HTML: "<p>Hidden</p>"}},
		&tg.SendMessageTypingAction{},
	} {
		sessions.clearMessages()
		ok, err := r.onMessagesSetTyping(ctx, &tg.MessagesSetTypingRequest{Peer: request.Peer, Action: action})
		_, ordinary := action.(*tg.SendMessageTypingAction)
		if ordinary && (err != nil || !ok) {
			t.Fatalf("blocked ordinary typing = %v, %v", ok, err)
		}
		if !ordinary && (ok || !tgerr.Is(err, "USER_IS_BLOCKED")) {
			t.Fatalf("blocked draft = %v, %v", ok, err)
		}
		if got := sessions.snapshot().message; got != nil {
			t.Fatalf("blocked typing delivered: %T", got)
		}
	}
}
