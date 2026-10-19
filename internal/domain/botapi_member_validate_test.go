package domain

import (
	"errors"
	"testing"
)

// validChatMemberUpdate 是一条可以作为 chat_member 投给机器人 99 的载荷：用户 42 从 left 变为 active。
func validChatMemberUpdate() BotChatMemberUpdated {
	return BotChatMemberUpdated{
		Chat: Peer{Type: PeerTypeChannel, ID: 5}, ActorUserID: 7, Date: 10,
		Old: BotChatMemberSnapshot{UserID: 42, Role: ChannelRoleMember, Status: ChannelMemberLeft},
		New: BotChatMemberSnapshot{UserID: 42, Role: ChannelRoleMember, Status: ChannelMemberActive},
	}
}

func TestBotChatMemberUpdatedValidateFor(t *testing.T) {
	const bot = 99
	for _, tc := range []struct {
		name    string
		bot     int64
		kind    BotAPIUpdateKind
		mutate  func(*BotChatMemberUpdated)
		wantErr bool
	}{
		{"chat_member about another user", bot, BotAPIUpdateChatMember, func(*BotChatMemberUpdated) {}, false},
		{"my_chat_member about the bot", bot, BotAPIUpdateMyChatMember, func(u *BotChatMemberUpdated) { u.Old.UserID, u.New.UserID = bot, bot }, false},
		{"viewer is the recipient bot", bot, BotAPIUpdateChatMember, func(u *BotChatMemberUpdated) { u.Viewer.UserID = bot }, false},
		{"viewer is another bot", bot, BotAPIUpdateChatMember, func(u *BotChatMemberUpdated) { u.Viewer.UserID = 98 }, true},
		{"chat_member about the bot itself", bot, BotAPIUpdateChatMember, func(u *BotChatMemberUpdated) { u.Old.UserID, u.New.UserID = bot, bot }, true},
		{"my_chat_member about another user", bot, BotAPIUpdateMyChatMember, func(*BotChatMemberUpdated) {}, true},
		{"old and new subjects differ", bot, BotAPIUpdateChatMember, func(u *BotChatMemberUpdated) { u.Old.UserID = 43 }, true},
		{"unchanged state", bot, BotAPIUpdateChatMember, func(u *BotChatMemberUpdated) { u.Old = u.New }, true},
		{"missing subject", bot, BotAPIUpdateChatMember, func(u *BotChatMemberUpdated) { u.Old.UserID, u.New.UserID = 0, 0 }, true},
		{"chat is not a channel", bot, BotAPIUpdateChatMember, func(u *BotChatMemberUpdated) { u.Chat.Type = PeerTypeUser }, true},
		{"missing chat id", bot, BotAPIUpdateChatMember, func(u *BotChatMemberUpdated) { u.Chat.ID = 0 }, true},
		{"missing actor", bot, BotAPIUpdateChatMember, func(u *BotChatMemberUpdated) { u.ActorUserID = 0 }, true},
		{"missing date", bot, BotAPIUpdateChatMember, func(u *BotChatMemberUpdated) { u.Date = 0 }, true},
		{"missing bot", 0, BotAPIUpdateChatMember, func(*BotChatMemberUpdated) {}, true},
		{"non-member kind", bot, BotAPIUpdateMessage, func(*BotChatMemberUpdated) {}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := validChatMemberUpdate()
			tc.mutate(&u)
			err := u.ValidateFor(tc.bot, tc.kind)
			if tc.wantErr && !errors.Is(err, ErrBotChatMemberUpdateInvalid) {
				t.Fatalf("ValidateFor = %v, want ErrBotChatMemberUpdateInvalid", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateFor = %v, want nil", err)
			}
		})
	}
}

func TestValidateBotAPIChatMemberRequest(t *testing.T) {
	chat := Peer{Type: PeerTypeChannel, ID: 5}
	for _, tc := range []struct {
		name    string
		mutate  func(*EnqueueBotAPIUpdateRequest)
		wantErr bool
	}{
		{"valid", func(*EnqueueBotAPIUpdateRequest) {}, false},
		{"payload chat differs from the row peer", func(r *EnqueueBotAPIUpdateRequest) { r.Peer = Peer{Type: PeerTypeChannel, ID: 6} }, true},
		{"member kind without payload", func(r *EnqueueBotAPIUpdateRequest) { r.ChatMember = nil }, true},
		{"member row with a message", func(r *EnqueueBotAPIUpdateRequest) { r.MessageID = 3 }, true},
		{"invalid payload", func(r *EnqueueBotAPIUpdateRequest) { r.ChatMember.Date = 0 }, true},
		{"message row carrying a member payload", func(r *EnqueueBotAPIUpdateRequest) { r.Kind = BotAPIUpdateMessage }, true},
		{"message row without payload", func(r *EnqueueBotAPIUpdateRequest) { r.Kind, r.ChatMember = BotAPIUpdateMessage, nil }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := validChatMemberUpdate()
			req := EnqueueBotAPIUpdateRequest{BotUserID: 99, Kind: BotAPIUpdateChatMember, Peer: chat, ChatMember: &payload}
			tc.mutate(&req)
			err := ValidateBotAPIChatMemberRequest(req)
			if tc.wantErr && !errors.Is(err, ErrBotChatMemberUpdateInvalid) {
				t.Fatalf("ValidateBotAPIChatMemberRequest = %v, want ErrBotChatMemberUpdateInvalid", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateBotAPIChatMemberRequest = %v, want nil", err)
			}
		})
	}
}

func TestCanEditChannelAdmin(t *testing.T) {
	const viewerID, targetID = 1, 2
	admin := func(userID, inviter int64) ChannelMember {
		return ChannelMember{UserID: userID, InviterUserID: inviter, Role: ChannelRoleAdmin, Status: ChannelMemberActive}
	}
	promoter := func(addAdmins bool) ChannelMember {
		m := admin(viewerID, 0)
		m.AdminRights.AddAdmins = addAdmins
		return m
	}
	creator := ChannelMember{UserID: viewerID, Role: ChannelRoleCreator, Status: ChannelMemberActive}
	for _, tc := range []struct {
		name           string
		viewer, target ChannelMember
		want           bool
	}{
		{"creator edits any admin", creator, admin(targetID, 77), true},
		{"promoter with add_admins", promoter(true), admin(targetID, viewerID), true},
		{"promoter without add_admins", promoter(false), admin(targetID, viewerID), false},
		{"admin promoted by someone else", promoter(true), admin(targetID, 77), false},
		{"target is the viewer", creator, admin(viewerID, 0), false},
		{"target has left", creator, ChannelMember{UserID: targetID, Role: ChannelRoleAdmin, Status: ChannelMemberLeft}, false},
		{"target is not an admin", creator, ChannelMember{UserID: targetID, Role: ChannelRoleMember, Status: ChannelMemberActive}, false},
		{"viewer is not active", ChannelMember{UserID: viewerID, Role: ChannelRoleCreator, Status: ChannelMemberLeft}, admin(targetID, 77), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanEditChannelAdmin(tc.viewer, tc.target); got != tc.want {
				t.Fatalf("CanEditChannelAdmin = %v, want %v", got, tc.want)
			}
		})
	}
}
