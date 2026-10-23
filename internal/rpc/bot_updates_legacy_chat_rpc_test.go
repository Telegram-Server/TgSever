package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"
)

// 本服务端没有独立的普通群（legacy chat）实体：messages.createChat 直接建超级群，
// messages.addChatUser / editChatAdmin / deleteChatUser 转发到对应的 channels.* 处理器，
// messages.migrateChat 只回当前状态（#147）。所以普通群入口上的 bot 成员变化走超级群的
// qts 路径：bot 收到的是带 qts 的 updateChannelParticipant（TDLib 照样转成 my_chat_member），
// 不会收到以普通群 chat_id 为键的 updateChatParticipant——那会让 TDLib 凭空多出一个
// basic group。"升级"前后是同一个频道，后续变动不重复、不遗漏。
func TestBotQtsLegacyChatEntrypointsUseChannelParticipantPath(t *testing.T) {
	f := newBotQtsFixture(t)
	chatID := f.channel.ID
	inputBot := &tg.InputUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash}
	owner := f.ownerCtx()

	steps := []struct {
		name string
		run  func() error
	}{
		{"addChatUser", func() error {
			_, err := f.router.onMessagesAddChatUser(owner, &tg.MessagesAddChatUserRequest{ChatID: chatID, UserID: inputBot})
			return err
		}},
		{"editChatAdmin promote", func() error {
			_, err := f.router.onMessagesEditChatAdmin(owner, &tg.MessagesEditChatAdminRequest{ChatID: chatID, UserID: inputBot, IsAdmin: true})
			return err
		}},
		{"editChatAdmin demote", func() error {
			_, err := f.router.onMessagesEditChatAdmin(owner, &tg.MessagesEditChatAdminRequest{ChatID: chatID, UserID: inputBot, IsAdmin: false})
			return err
		}},
		{"deleteChatUser", func() error {
			_, err := f.router.onMessagesDeleteChatUser(owner, &tg.MessagesDeleteChatUserRequest{ChatID: chatID, UserID: inputBot})
			return err
		}},
		{"migrateChat", func() error {
			_, err := f.router.onMessagesMigrateChat(owner, chatID)
			return err
		}},
		{"addChatUser after migrate", func() error {
			_, err := f.router.onMessagesAddChatUser(owner, &tg.MessagesAddChatUserRequest{ChatID: chatID, UserID: inputBot})
			return err
		}},
		{"bot leaves via deleteChatUser", func() error {
			_, err := f.router.onMessagesDeleteChatUser(f.botCtx(1), &tg.MessagesDeleteChatUserRequest{ChatID: chatID, UserID: &tg.InputUserSelf{}})
			return err
		}},
	}
	for _, step := range steps {
		if err := step.run(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
	}

	// 推送：每次成员变化恰好一条带 qts 的 updateChannelParticipant，migrateChat 不产生。
	pushed := f.pushedBotParticipants(t)
	wantNew := []string{"self", "admin", "self", "banned", "self", "left"}
	if len(pushed) != len(wantNew) {
		t.Fatalf("bot received %d updateChannelParticipant, want %d: %+v", len(pushed), len(wantNew), pushed)
	}
	for i, p := range pushed {
		if p.Qts != i+1 || p.ChannelID != chatID {
			t.Fatalf("push %d = qts %d channel %d, want qts %d channel %d", i, p.Qts, p.ChannelID, i+1, chatID)
		}
		np, _ := p.GetNewParticipant()
		if got := participantKind(np); got != wantNew[i] {
			t.Fatalf("push %d new participant = %T (%s), want %s", i, np, got, wantNew[i])
		}
	}
	for _, rec := range f.sessions.records() {
		if rec.userID != f.bot.ID {
			continue
		}
		if updates, ok := rec.msg.(*tg.Updates); ok {
			for _, update := range updates.Updates {
				switch update.(type) {
				case *tg.UpdateChatParticipant, *tg.UpdateChatParticipantAdd, *tg.UpdateChatParticipantDelete,
					*tg.UpdateChatParticipantAdmin, *tg.UpdateChatParticipants:
					t.Fatalf("bot received a basic-group participant update %T", update)
				}
			}
		}
	}

	// 离线补回：从 0 补回全部 6 条；从升级前的 qts 补回只有升级后的 2 条。
	for _, tc := range []struct {
		from int
		want []int
	}{{0, []int{1, 2, 3, 4, 5, 6}}, {4, []int{5, 6}}} {
		diff, err := f.router.onUpdatesGetDifference(f.botCtx(2), &tg.UpdatesGetDifferenceRequest{Qts: tc.from, Date: 1})
		if err != nil {
			t.Fatalf("getDifference from qts %d: %v", tc.from, err)
		}
		full, ok := diff.(*tg.UpdatesDifference)
		if !ok {
			t.Fatalf("getDifference from qts %d = %T, want updates.difference", tc.from, diff)
		}
		var got []int
		for _, update := range full.OtherUpdates {
			switch u := update.(type) {
			case *tg.UpdateChannelParticipant:
				got = append(got, u.Qts)
			case *tg.UpdateChatParticipant:
				t.Fatalf("difference carries basic-group updateChatParticipant %+v", u)
			}
		}
		if len(got) != len(tc.want) || full.State.Qts != 6 {
			t.Fatalf("difference from qts %d = %v state %d, want %v state 6", tc.from, got, full.State.Qts, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("difference from qts %d = %v, want %v", tc.from, got, tc.want)
			}
		}
	}
}

func participantKind(p tg.ChannelParticipantClass) string {
	switch p.(type) {
	case *tg.ChannelParticipantSelf, *tg.ChannelParticipant:
		return "self"
	case *tg.ChannelParticipantAdmin:
		return "admin"
	case *tg.ChannelParticipantBanned:
		return "banned"
	case *tg.ChannelParticipantLeft:
		return "left"
	default:
		return "other"
	}
}

// 普通群入口上其他成员的变动（#146）：messages.addChatUser / editChatAdmin / deleteChatUser
// 走超级群路径，群里的管理员 bot 每次收到一条带 qts 的 updateChannelParticipant（user_id 是
// 变动的成员）；不是管理员的 bot 收不到任何 updateChannelParticipant。
func TestAdminBotReceivesOtherMemberChangesViaLegacyChatEntrypoints(t *testing.T) {
	fx := newAdminBotAudienceFixture(t)
	owner, adminBot, plainBot, human := fx.newUser(t, 8301, false), fx.newUser(t, 8302, true), fx.newUser(t, 8303, true), fx.newUser(t, 8304, false)
	ownerCtx := fx.userCtx(owner)
	created, err := fx.router.onMessagesCreateChat(ownerCtx, &tg.MessagesCreateChatRequest{
		Title: "legacy audience",
		Users: []tg.InputUserClass{&tg.InputUser{UserID: adminBot.ID, AccessHash: adminBot.AccessHash}, &tg.InputUser{UserID: plainBot.ID, AccessHash: plainBot.AccessHash}},
	})
	if err != nil {
		t.Fatalf("createChat: %v", err)
	}
	var chatID int64
	for _, chat := range created.Updates.(*tg.Updates).Chats {
		if ch, ok := chat.(*tg.Channel); ok {
			chatID = ch.ID
		}
	}
	if chatID == 0 {
		t.Fatalf("createChat returned no chat: %+v", created)
	}
	inputHuman := &tg.InputUser{UserID: human.ID, AccessHash: human.AccessHash}
	if _, err := fx.router.onMessagesEditChatAdmin(ownerCtx, &tg.MessagesEditChatAdminRequest{ChatID: chatID, UserID: &tg.InputUser{UserID: adminBot.ID, AccessHash: adminBot.AccessHash}, IsAdmin: true}); err != nil {
		t.Fatalf("promote bot: %v", err)
	}
	before := len(fx.pushedParticipants(adminBot.ID))
	plainBefore := len(fx.pushedParticipants(plainBot.ID))

	if _, err := fx.router.onMessagesAddChatUser(ownerCtx, &tg.MessagesAddChatUserRequest{ChatID: chatID, UserID: inputHuman}); err != nil {
		t.Fatalf("addChatUser: %v", err)
	}
	if _, err := fx.router.onMessagesEditChatAdmin(ownerCtx, &tg.MessagesEditChatAdminRequest{ChatID: chatID, UserID: inputHuman, IsAdmin: true}); err != nil {
		t.Fatalf("editChatAdmin promote: %v", err)
	}
	if _, err := fx.router.onMessagesEditChatAdmin(ownerCtx, &tg.MessagesEditChatAdminRequest{ChatID: chatID, UserID: inputHuman, IsAdmin: false}); err != nil {
		t.Fatalf("editChatAdmin demote: %v", err)
	}
	if _, err := fx.router.onMessagesDeleteChatUser(ownerCtx, &tg.MessagesDeleteChatUserRequest{ChatID: chatID, UserID: inputHuman}); err != nil {
		t.Fatalf("deleteChatUser: %v", err)
	}

	got := fx.pushedParticipants(adminBot.ID)[before:]
	wantNew := []string{"self", "admin", "self", "banned"}
	if len(got) != len(wantNew) {
		t.Fatalf("admin bot received %d updateChannelParticipant about the member, want %d: %+v", len(got), len(wantNew), got)
	}
	for i, p := range got {
		np, _ := p.GetNewParticipant()
		if p.UserID != human.ID || p.ChannelID != chatID || p.Qts == 0 || participantKind(np) != wantNew[i] {
			t.Fatalf("push %d = %+v, want a qts update about the member with a %s participant", i, p, wantNew[i])
		}
	}
	if extra := fx.pushedParticipants(plainBot.ID)[plainBefore:]; len(extra) != 0 {
		t.Fatalf("non-admin bot received other members' changes: %+v", extra)
	}
}
