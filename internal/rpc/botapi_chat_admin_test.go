package rpc

import (
	"slices"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// participant 以群主视角读取某个用户在测试群里的成员记录。
func (f botAPIBridgeFixture) participant(t *testing.T, userID int64) domain.ChannelMember {
	t.Helper()
	member, err := f.channels.GetParticipant(f.ctx, f.owner.ID, f.channel.ID, userID)
	if err != nil {
		t.Fatalf("GetParticipant(%d): %v", userID, err)
	}
	return member
}

// grantBot 由群主把机器人提为管理员（不依赖 fixture 的 promoteBot）。
func (f botAPIBridgeFixture) grantBot(t *testing.T, rights domain.ChannelAdminRights) {
	t.Helper()
	if _, err := f.channels.EditAdmin(f.ctx, f.owner.ID, domain.EditChannelAdminRequest{
		UserID:      f.owner.ID,
		ChannelID:   f.channel.ID,
		MemberID:    f.bot.ID,
		AdminRights: rights,
		Date:        20,
	}); err != nil {
		t.Fatalf("promote bot: %v", err)
	}
}

func wantBotAPIError(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), code) {
		t.Fatalf("err = %v, want %s", err, code)
	}
}

func TestBotAPIBanChatMemberRequiresBanRight(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	err := f.router.BotAPIBanChatMember(f.ctx, f.bot.ID, f.chatID, f.member.ID, 0, false)
	wantBotAPIError(t, err, "CHAT_ADMIN_REQUIRED")
	if got := f.participant(t, f.member.ID); got.Status != domain.ChannelMemberActive {
		t.Fatalf("member status = %s, want still active", got.Status)
	}
}

func TestBotAPIBanChatMemberKicksAndNotifiesMembers(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.grantBot(t, domain.ChannelAdminRights{BanUsers: true})
	f.sessions.clearMessages()

	if err := f.router.BotAPIBanChatMember(f.ctx, f.bot.ID, f.chatID, f.member.ID, 0, false); err != nil {
		t.Fatalf("BotAPIBanChatMember: %v", err)
	}
	got := f.participant(t, f.member.ID)
	if got.Status != domain.ChannelMemberKicked || !got.BannedRights.ViewMessages || !got.BannedRights.SendMessages {
		t.Fatalf("member = %+v, want kicked with every right banned", got)
	}
	if !slices.Contains(f.sessions.pushedUserIDs(), f.owner.ID) {
		t.Fatalf("pushed to %v, want the owner to see the ban", f.sessions.pushedUserIDs())
	}
}

func TestBotAPIBanChatMemberKeepsUntilDate(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.grantBot(t, domain.ChannelAdminRights{BanUsers: true})

	if err := f.router.BotAPIBanChatMember(f.ctx, f.bot.ID, f.chatID, f.member.ID, 1_900_000_000, false); err != nil {
		t.Fatalf("BotAPIBanChatMember: %v", err)
	}
	if got := f.participant(t, f.member.ID).BannedRights.UntilDate; got != 1_900_000_000 {
		t.Fatalf("until_date = %d, want 1900000000", got)
	}
}

// kickMember 由群主把 member 踢出（永久封禁）。
func (f botAPIBridgeFixture) kickMember(t *testing.T) {
	t.Helper()
	if _, err := f.router.onChannelsEditBanned(WithUserID(f.ctx, f.owner.ID), &tg.ChannelsEditBannedRequest{
		Channel:      &tg.InputChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		Participant:  &tg.InputPeerUser{UserID: f.member.ID, AccessHash: f.member.AccessHash},
		BannedRights: tgChatBannedRights(botAPIFullBan(0)),
	}); err != nil {
		t.Fatalf("kick member: %v", err)
	}
}

func TestBotAPIUnbanChatMemberLiftsABan(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.grantBot(t, domain.ChannelAdminRights{BanUsers: true})
	f.kickMember(t)

	if err := f.router.BotAPIUnbanChatMember(f.ctx, f.bot.ID, f.chatID, f.member.ID, true); err != nil {
		t.Fatalf("BotAPIUnbanChatMember: %v", err)
	}
	if got := f.participant(t, f.member.ID); got.Status != domain.ChannelMemberLeft || got.BannedRights.ViewMessages {
		t.Fatalf("member = %+v, want left without a ban", got)
	}
}

func TestBotAPIUnbanChatMemberRequiresBanRight(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.kickMember(t)

	err := f.router.BotAPIUnbanChatMember(f.ctx, f.bot.ID, f.chatID, f.member.ID, false)
	wantBotAPIError(t, err, "CHAT_ADMIN_REQUIRED")
	if got := f.participant(t, f.member.ID); got.Status != domain.ChannelMemberKicked {
		t.Fatalf("member status = %s, want still kicked", got.Status)
	}
}

// 官方默认语义：调用后用户不在群里但可以再加入——正常成员会被移出。
func TestBotAPIUnbanChatMemberRemovesAnActiveMember(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.grantBot(t, domain.ChannelAdminRights{BanUsers: true})

	if err := f.router.BotAPIUnbanChatMember(f.ctx, f.bot.ID, f.chatID, f.member.ID, false); err != nil {
		t.Fatalf("BotAPIUnbanChatMember: %v", err)
	}
	if got := f.participant(t, f.member.ID); got.Status != domain.ChannelMemberLeft || got.BannedRights.ViewMessages {
		t.Fatalf("member = %+v, want removed but free to rejoin", got)
	}
}

func TestBotAPIUnbanChatMemberOnlyIfBannedLeavesMembersAlone(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.grantBot(t, domain.ChannelAdminRights{BanUsers: true})

	if err := f.router.BotAPIUnbanChatMember(f.ctx, f.bot.ID, f.chatID, f.member.ID, true); err != nil {
		t.Fatalf("BotAPIUnbanChatMember: %v", err)
	}
	if got := f.participant(t, f.member.ID); got.Status != domain.ChannelMemberActive {
		t.Fatalf("member status = %s, want still active", got.Status)
	}
}

func TestBotAPIRestrictChatMemberRequiresBanRight(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	err := f.router.BotAPIRestrictChatMember(f.ctx, f.bot.ID, f.chatID, f.member.ID, domain.ChannelBannedRights{SendMedia: true})
	wantBotAPIError(t, err, "CHAT_ADMIN_REQUIRED")
}

func TestBotAPIRestrictChatMemberAppliesRights(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.grantBot(t, domain.ChannelAdminRights{BanUsers: true})
	rights := domain.ChannelBannedRights{SendMedia: true, SendPhotos: true, UntilDate: 1_900_000_000}

	if err := f.router.BotAPIRestrictChatMember(f.ctx, f.bot.ID, f.chatID, f.member.ID, rights); err != nil {
		t.Fatalf("BotAPIRestrictChatMember: %v", err)
	}
	got := f.participant(t, f.member.ID)
	if got.Status != domain.ChannelMemberActive || got.BannedRights != rights {
		t.Fatalf("member = %+v, want active with %+v", got, rights)
	}
}

func TestBotAPIPromoteChatMemberRequiresAddAdminsRight(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.grantBot(t, domain.ChannelAdminRights{BanUsers: true})

	err := f.router.BotAPIPromoteChatMember(f.ctx, f.bot.ID, f.chatID, f.member.ID, domain.ChannelAdminRights{PinMessages: true})
	if err == nil {
		t.Fatal("promote without can_promote_members succeeded")
	}
	if got := f.participant(t, f.member.ID); got.Role != domain.ChannelRoleMember {
		t.Fatalf("member role = %s, want unchanged", got.Role)
	}
}

func TestBotAPIPromoteChatMemberGrantsAndRevokes(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.grantBot(t, domain.ChannelAdminRights{AddAdmins: true, PinMessages: true, DeleteMessages: true})

	rights := domain.ChannelAdminRights{PinMessages: true}
	if err := f.router.BotAPIPromoteChatMember(f.ctx, f.bot.ID, f.chatID, f.member.ID, rights); err != nil {
		t.Fatalf("promote: %v", err)
	}
	got := f.participant(t, f.member.ID)
	if got.Role != domain.ChannelRoleAdmin || !got.AdminRights.PinMessages || !got.AdminRights.ManageChat || got.AdminRights.DeleteMessages {
		t.Fatalf("member = %+v, want admin with pin (and the implied manage_chat)", got)
	}

	if err := f.router.BotAPIPromoteChatMember(f.ctx, f.bot.ID, f.chatID, f.member.ID, domain.ChannelAdminRights{}); err != nil {
		t.Fatalf("demote: %v", err)
	}
	if got := f.participant(t, f.member.ID); got.Role != domain.ChannelRoleMember || got.AdminRights != (domain.ChannelAdminRights{}) {
		t.Fatalf("member = %+v, want demoted to a plain member", got)
	}
}

func TestBotAPISetChatAdministratorCustomTitleKeepsRights(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.grantBot(t, domain.ChannelAdminRights{AddAdmins: true, PinMessages: true, InviteUsers: true})
	granted := domain.ChannelAdminRights{PinMessages: true, InviteUsers: true}
	if err := f.router.BotAPIPromoteChatMember(f.ctx, f.bot.ID, f.chatID, f.member.ID, granted); err != nil {
		t.Fatalf("promote: %v", err)
	}
	before := f.participant(t, f.member.ID).AdminRights

	if err := f.router.BotAPISetChatAdministratorCustomTitle(f.ctx, f.bot.ID, f.chatID, f.member.ID, "Moderator"); err != nil {
		t.Fatalf("set title: %v", err)
	}
	got := f.participant(t, f.member.ID)
	if got.Rank != "Moderator" || got.AdminRights != before || got.Role != domain.ChannelRoleAdmin {
		t.Fatalf("member = %+v, want rank Moderator with rights %+v", got, before)
	}
}

func TestBotAPISetChatAdministratorCustomTitleNeedsAnAdmin(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.grantBot(t, domain.ChannelAdminRights{AddAdmins: true})

	err := f.router.BotAPISetChatAdministratorCustomTitle(f.ctx, f.bot.ID, f.chatID, f.member.ID, "Moderator")
	wantBotAPIError(t, err, "USER_NOT_ADMIN")
	if got := f.participant(t, f.member.ID); got.Role != domain.ChannelRoleMember || got.Rank != "" {
		t.Fatalf("member = %+v, want untouched", got)
	}
}

func TestBotAPISetChatPermissionsRequiresBanRight(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	err := f.router.BotAPISetChatPermissions(f.ctx, f.bot.ID, f.chatID, domain.ChannelBannedRights{SendPolls: true})
	wantBotAPIError(t, err, "CHAT_ADMIN_REQUIRED")
}

func TestBotAPISetChatPermissionsUpdatesDefaults(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.grantBot(t, domain.ChannelAdminRights{BanUsers: true})
	rights := domain.ChannelBannedRights{SendPolls: true, PinMessages: true}

	if err := f.router.BotAPISetChatPermissions(f.ctx, f.bot.ID, f.chatID, rights); err != nil {
		t.Fatalf("BotAPISetChatPermissions: %v", err)
	}
	view, err := f.channels.ResolveChannel(f.ctx, f.owner.ID, f.channel.ID)
	if err != nil {
		t.Fatalf("ResolveChannel: %v", err)
	}
	if view.Channel.DefaultBannedRights != rights {
		t.Fatalf("default rights = %+v, want %+v", view.Channel.DefaultBannedRights, rights)
	}
}

// pinnedChannelMessageID 返回群主视角当前的置顶消息 id。
func (f botAPIBridgeFixture) pinnedChannelMessageID(t *testing.T) int {
	t.Helper()
	view, err := f.channels.ResolveChannel(f.ctx, f.owner.ID, f.channel.ID)
	if err != nil {
		t.Fatalf("ResolveChannel: %v", err)
	}
	return view.Channel.PinnedMessageID
}

func TestBotAPIPinChatMessageRequiresPinRight(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.setDefaultBannedRights(t, domain.ChannelBannedRights{PinMessages: true})
	id := f.sendAs(t, f.owner.ID, "rules", 601)

	err := f.router.BotAPIPinChatMessage(f.ctx, f.bot.ID, f.chatID, id, false)
	if err == nil {
		t.Fatal("pin without the pin right succeeded")
	}
	if got := f.pinnedChannelMessageID(t); got != 0 {
		t.Fatalf("pinned = %d, want nothing pinned", got)
	}
}

func TestBotAPIPinAndUnpinChannelMessages(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.grantBot(t, domain.ChannelAdminRights{PinMessages: true})
	first := f.sendAs(t, f.owner.ID, "rules", 611)
	second := f.sendAs(t, f.owner.ID, "news", 612)

	for _, id := range []int{first, second} {
		if err := f.router.BotAPIPinChatMessage(f.ctx, f.bot.ID, f.chatID, id, true); err != nil {
			t.Fatalf("pin %d: %v", id, err)
		}
	}
	if got := f.pinnedChannelMessageID(t); got != second {
		t.Fatalf("pinned = %d, want %d", got, second)
	}
	// 省略 message_id 取消最近一条置顶。
	if err := f.router.BotAPIUnpinChatMessage(f.ctx, f.bot.ID, f.chatID, 0); err != nil {
		t.Fatalf("unpin latest: %v", err)
	}
	if got := f.pinnedChannelMessageID(t); got != first {
		t.Fatalf("pinned after unpinning latest = %d, want %d", got, first)
	}
	if err := f.router.BotAPIPinChatMessage(f.ctx, f.bot.ID, f.chatID, second, true); err != nil {
		t.Fatalf("re-pin: %v", err)
	}
	if err := f.router.BotAPIUnpinChatMessage(f.ctx, f.bot.ID, f.chatID, first); err != nil {
		t.Fatalf("unpin first: %v", err)
	}
	if got := f.pinnedChannelMessageID(t); got != second {
		t.Fatalf("pinned = %d, want %d", got, second)
	}
	if err := f.router.BotAPIUnpinAllChatMessages(f.ctx, f.bot.ID, f.chatID); err != nil {
		t.Fatalf("unpin all: %v", err)
	}
	if got := f.pinnedChannelMessageID(t); got != 0 {
		t.Fatalf("pinned after unpinAll = %d, want 0", got)
	}
}

func TestBotAPIPinAndUnpinPrivateMessages(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	updates, err := f.router.onMessagesSendMessage(WithUserID(f.ctx, f.bot.ID), &tg.MessagesSendMessageRequest{
		Peer: &tg.InputPeerUser{UserID: f.member.ID, AccessHash: f.member.AccessHash}, Message: "welcome", RandomID: 621,
	})
	if err != nil {
		t.Fatalf("send private: %v", err)
	}
	ids := botAPIUpdateMessageIDs(domain.Peer{Type: domain.PeerTypeUser, ID: f.member.ID}, updates)
	if len(ids) != 1 {
		t.Fatalf("sent ids = %v", ids)
	}
	pinned := func() int {
		got, err := f.router.botAPILatestPinnedMessageID(f.ctx, f.bot.ID, f.member.ID)
		if err != nil {
			t.Fatalf("latest pinned: %v", err)
		}
		return got
	}

	if err := f.router.BotAPIPinChatMessage(f.ctx, f.bot.ID, f.member.ID, ids[0], false); err != nil {
		t.Fatalf("pin private: %v", err)
	}
	if got := pinned(); got != ids[0] {
		t.Fatalf("pinned = %d, want %d", got, ids[0])
	}
	if err := f.router.BotAPIUnpinChatMessage(f.ctx, f.bot.ID, f.member.ID, 0); err != nil {
		t.Fatalf("unpin latest private: %v", err)
	}
	if got := pinned(); got != 0 {
		t.Fatalf("pinned after unpin = %d, want 0", got)
	}
	// 没有置顶时省略 message_id 报错。
	wantBotAPIError(t, f.router.BotAPIUnpinChatMessage(f.ctx, f.bot.ID, f.member.ID, 0), "MESSAGE_ID_INVALID")
	if err := f.router.BotAPIUnpinAllChatMessages(f.ctx, f.bot.ID, f.member.ID); err != nil {
		t.Fatalf("unpin all private: %v", err)
	}
}

func (f botAPIBridgeFixture) channelState(t *testing.T) domain.Channel {
	t.Helper()
	view, err := f.channels.ResolveChannel(f.ctx, f.owner.ID, f.channel.ID)
	if err != nil {
		t.Fatalf("ResolveChannel: %v", err)
	}
	return view.Channel
}

func TestBotAPISetChatTitleAndDescriptionRequireChangeInfo(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.setDefaultBannedRights(t, domain.ChannelBannedRights{ChangeInfo: true})

	if err := f.router.BotAPISetChatTitle(f.ctx, f.bot.ID, f.chatID, "Hijacked"); err == nil {
		t.Fatal("setChatTitle without change_info succeeded")
	}
	if err := f.router.BotAPISetChatDescription(f.ctx, f.bot.ID, f.chatID, "hijacked"); err == nil {
		t.Fatal("setChatDescription without change_info succeeded")
	}
	if got := f.channelState(t); got.Title != "Bridge Group" || got.About != "" {
		t.Fatalf("channel = %q / %q, want unchanged", got.Title, got.About)
	}
}

func TestBotAPISetChatTitleAndDescription(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.grantBot(t, domain.ChannelAdminRights{ChangeInfo: true})
	f.sessions.clearMessages()

	if err := f.router.BotAPISetChatTitle(f.ctx, f.bot.ID, f.chatID, "Renamed Group"); err != nil {
		t.Fatalf("setChatTitle: %v", err)
	}
	if !slices.Contains(f.sessions.pushedUserIDs(), f.member.ID) {
		t.Fatalf("pushed to %v, want members to see the new title", f.sessions.pushedUserIDs())
	}
	if err := f.router.BotAPISetChatDescription(f.ctx, f.bot.ID, f.chatID, "All about bridges"); err != nil {
		t.Fatalf("setChatDescription: %v", err)
	}
	if got := f.channelState(t); got.Title != "Renamed Group" || got.About != "All about bridges" {
		t.Fatalf("channel = %q / %q", got.Title, got.About)
	}
	wantBotAPIError(t, f.router.BotAPISetChatTitle(f.ctx, f.bot.ID, f.member.ID, "Private"), "CHAT_ID_INVALID")
}

func TestBotAPISetChatPhotoRequiresChangeInfo(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.router.deps.Files = &fakeFiles{}
	f.setDefaultBannedRights(t, domain.ChannelBannedRights{ChangeInfo: true})

	if err := f.router.BotAPISetChatPhoto(f.ctx, f.bot.ID, f.chatID, "a.jpg", "image/jpeg", []byte("jpeg")); err == nil {
		t.Fatal("setChatPhoto without change_info succeeded")
	}
	if got := f.channelState(t).PhotoID; got != 0 {
		t.Fatalf("photo = %d, want none", got)
	}
}

func TestBotAPISetAndDeleteChatPhoto(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	files := &fakeFiles{}
	f.router.deps.Files = files
	f.grantBot(t, domain.ChannelAdminRights{ChangeInfo: true})
	f.sessions.clearMessages()

	if err := f.router.BotAPISetChatPhoto(f.ctx, f.bot.ID, f.chatID, "a.jpg", "image/jpeg", []byte("jpeg")); err != nil {
		t.Fatalf("setChatPhoto: %v", err)
	}
	got := f.channelState(t).PhotoID
	if _, stored := files.photos[got]; got == 0 || !stored {
		t.Fatalf("channel photo = %d, want the uploaded photo (stored %v)", got, files.photos)
	}
	if !slices.Contains(f.sessions.pushedUserIDs(), f.member.ID) {
		t.Fatalf("pushed to %v, want members to see the new photo", f.sessions.pushedUserIDs())
	}

	if err := f.router.BotAPIDeleteChatPhoto(f.ctx, f.bot.ID, f.chatID); err != nil {
		t.Fatalf("deleteChatPhoto: %v", err)
	}
	if got := f.channelState(t).PhotoID; got != 0 {
		t.Fatalf("photo after delete = %d, want none", got)
	}
}

func TestBotAPILeaveChat(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.sessions.clearMessages()

	if err := f.router.BotAPILeaveChat(f.ctx, f.bot.ID, f.chatID); err != nil {
		t.Fatalf("leaveChat: %v", err)
	}
	if got := f.participant(t, f.bot.ID); got.Status != domain.ChannelMemberLeft {
		t.Fatalf("bot status = %s, want left", got.Status)
	}
	if !slices.Contains(f.sessions.pushedUserIDs(), f.owner.ID) {
		t.Fatalf("pushed to %v, want the owner to see the bot leave", f.sessions.pushedUserIDs())
	}
	wantBotAPIError(t, f.router.BotAPILeaveChat(f.ctx, f.bot.ID, f.member.ID), "CHAT_ID_INVALID")
}

// setDefaultBannedRights 由群主设置群默认权限。
func (f botAPIBridgeFixture) setDefaultBannedRights(t *testing.T, rights domain.ChannelBannedRights) {
	t.Helper()
	if _, err := f.router.onMessagesEditChatDefaultBannedRights(WithUserID(f.ctx, f.owner.ID), &tg.MessagesEditChatDefaultBannedRightsRequest{
		Peer:         &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		BannedRights: tgChatBannedRights(rights),
	}); err != nil {
		t.Fatalf("set default rights: %v", err)
	}
}

// sendAs 以 userID 身份往测试群发一条消息，返回消息 id。
func (f botAPIBridgeFixture) sendAs(t *testing.T, userID int64, text string, randomID int64) int {
	t.Helper()
	updates, err := f.router.onMessagesSendMessage(WithUserID(f.ctx, userID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		Message:  text,
		RandomID: randomID,
	})
	if err != nil {
		t.Fatalf("send as %d: %v", userID, err)
	}
	ids := botAPIUpdateMessageIDs(domain.Peer{Type: domain.PeerTypeChannel, ID: f.channel.ID}, updates)
	if len(ids) != 1 {
		t.Fatalf("sent ids = %v", ids)
	}
	return ids[0]
}

// visibleMessageIDs 返回群主仍能看到的那些消息 id。
func (f botAPIBridgeFixture) visibleMessageIDs(t *testing.T, ids []int) []int {
	t.Helper()
	history, err := f.channels.GetMessages(f.ctx, f.owner.ID, f.channel.ID, ids)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	var out []int
	for _, msg := range history.Messages {
		if !msg.Deleted && msg.ID != 0 {
			out = append(out, msg.ID)
		}
	}
	return out
}

func TestBotAPIBanChatMemberRevokesMessages(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.grantBot(t, domain.ChannelAdminRights{BanUsers: true, DeleteMessages: true})
	spam := []int{f.sendAs(t, f.member.ID, "spam 1", 501), f.sendAs(t, f.member.ID, "spam 2", 502)}
	keep := f.sendAs(t, f.owner.ID, "owner note", 503)

	if err := f.router.BotAPIBanChatMember(f.ctx, f.bot.ID, f.chatID, f.member.ID, 0, true); err != nil {
		t.Fatalf("BotAPIBanChatMember: %v", err)
	}
	if got := f.visibleMessageIDs(t, append(spam, keep)); !slices.Equal(got, []int{keep}) {
		t.Fatalf("visible messages = %v, want only the owner's %d", got, keep)
	}
}
