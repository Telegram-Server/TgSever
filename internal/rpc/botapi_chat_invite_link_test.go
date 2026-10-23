package rpc

import (
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func inviteOption[T any](v T) *T { return &v }

// inviteHashOf 取出完整邀请链接末尾的 hash（https://…/+hash）。
func inviteHashOf(t *testing.T, link string) string {
	t.Helper()
	idx := strings.LastIndex(link, "+")
	if idx < 0 || idx == len(link)-1 {
		t.Fatalf("invite link %q has no +hash", link)
	}
	return link[idx+1:]
}

// memberLeaves 让 member 退群，之后可以用邀请链接验证"能否加入"。
func (f botAPIBridgeFixture) memberLeaves(t *testing.T) {
	t.Helper()
	if _, err := f.router.onChannelsLeaveChannel(WithUserID(f.ctx, f.member.ID), f.inputChannel()); err != nil {
		t.Fatalf("member leaves: %v", err)
	}
}

// memberJoinsVia 以 member 身份用邀请链接入群。
func (f botAPIBridgeFixture) memberJoinsVia(t *testing.T, link string) error {
	t.Helper()
	_, err := f.router.onMessagesImportChatInvite(WithUserID(f.ctx, f.member.ID), inviteHashOf(t, link))
	return err
}

// ownerSeesInvite 以群主视角读取一条邀请（客户端链接管理页看到的那条）。
func (f botAPIBridgeFixture) ownerSeesInvite(t *testing.T, link string) *tg.ChatInviteExported {
	t.Helper()
	res, err := f.router.onMessagesGetExportedChatInvite(WithUserID(f.ctx, f.owner.ID), &tg.MessagesGetExportedChatInviteRequest{
		Peer: &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		Link: link,
	})
	if err != nil {
		t.Fatalf("owner reads invite %s: %v", link, err)
	}
	exported, ok := res.(*tg.MessagesExportedChatInvite)
	if !ok {
		t.Fatalf("owner reads invite = %T", res)
	}
	invite, ok := exported.Invite.(*tg.ChatInviteExported)
	if !ok {
		t.Fatalf("invite = %T", exported.Invite)
	}
	return invite
}

// 普通成员机器人、以及只有与邀请无关权限的管理员机器人，四个方法都被拒绝。
func TestBotAPIChatInviteLinksRequireInviteRight(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rights *domain.ChannelAdminRights
	}{
		{name: "member"},
		{name: "admin without invite right", rights: &domain.ChannelAdminRights{DeleteMessages: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBotAPIBridgeFixture(t)
			if tc.rights != nil {
				f.promoteBot(t, *tc.rights)
			}
			ownerLink := "https://t.me/+" + f.exportInvite(t, false)

			_, err := f.router.BotAPICreateChatInviteLink(f.ctx, f.bot.ID, f.chatID, domain.BotAPIChatInviteLinkOptions{Name: inviteOption("x")})
			wantBotAPIError(t, err, "CHAT_ADMIN_REQUIRED")
			_, err = f.router.BotAPIEditChatInviteLink(f.ctx, f.bot.ID, f.chatID, ownerLink, domain.BotAPIChatInviteLinkOptions{Name: inviteOption("x")})
			wantBotAPIError(t, err, "CHAT_ADMIN_REQUIRED")
			_, err = f.router.BotAPIRevokeChatInviteLink(f.ctx, f.bot.ID, f.chatID, ownerLink)
			wantBotAPIError(t, err, "CHAT_ADMIN_REQUIRED")
			_, err = f.router.BotAPIExportChatInviteLink(f.ctx, f.bot.ID, f.chatID)
			wantBotAPIError(t, err, "CHAT_ADMIN_REQUIRED")

			if got := f.ownerSeesInvite(t, ownerLink); got.Revoked || got.Title != "" {
				t.Fatalf("owner's invite = %+v, want it untouched", got)
			}
		})
	}
}

func TestBotAPICreateChatInviteLinkReturnsFullLink(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{InviteUsers: true})
	expire := int(time.Now().Add(time.Hour).Unix())

	got, err := f.router.BotAPICreateChatInviteLink(f.ctx, f.bot.ID, f.chatID, domain.BotAPIChatInviteLinkOptions{
		Name: inviteOption("promo"), ExpireDate: inviteOption(expire), MemberLimit: inviteOption(10),
	})
	if err != nil {
		t.Fatalf("BotAPICreateChatInviteLink: %v", err)
	}
	if got.Invite.Title != "promo" || got.Invite.ExpireDate != expire || got.Invite.UsageLimit != 10 ||
		got.Invite.Permanent || got.Invite.Revoked || got.Invite.RequestNeeded {
		t.Fatalf("invite = %+v, want promo / expire / limit 10, additional and active", got.Invite)
	}
	if got.Creator.ID != f.bot.ID || !got.Creator.Bot || got.Creator.FirstName != "BridgeBot" {
		t.Fatalf("creator = %+v, want the bot", got.Creator)
	}
	// 群主在客户端的链接管理页里看到这条链接记在机器人名下。
	if seen := f.ownerSeesInvite(t, got.Link); seen.AdminID != f.bot.ID || seen.Title != "promo" {
		t.Fatalf("owner sees %+v, want the bot's promo link", seen)
	}

	approval, err := f.router.BotAPICreateChatInviteLink(f.ctx, f.bot.ID, f.chatID, domain.BotAPIChatInviteLinkOptions{
		CreatesJoinRequest: inviteOption(true),
	})
	if err != nil || !approval.Invite.RequestNeeded || approval.Invite.Hash == got.Invite.Hash {
		t.Fatalf("approval link = %+v, %v; want a new link that needs approval", approval.Invite, err)
	}
	f.memberLeaves(t)
	wantBotAPIError(t, f.memberJoinsVia(t, approval.Link), "INVITE_REQUEST_SENT")
}

func TestBotAPIEditChatInviteLinkChangesOnlyGivenFields(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{ChangeInfo: true})
	created, err := f.router.BotAPICreateChatInviteLink(f.ctx, f.bot.ID, f.chatID, domain.BotAPIChatInviteLinkOptions{
		Name: inviteOption("promo"), MemberLimit: inviteOption(10),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	expire := int(time.Now().Add(time.Hour).Unix())

	edited, err := f.router.BotAPIEditChatInviteLink(f.ctx, f.bot.ID, f.chatID, created.Link, domain.BotAPIChatInviteLinkOptions{
		ExpireDate: inviteOption(expire),
	})
	if err != nil {
		t.Fatalf("BotAPIEditChatInviteLink: %v", err)
	}
	if edited.Link != created.Link || edited.Invite.Title != "promo" || edited.Invite.UsageLimit != 10 || edited.Invite.ExpireDate != expire {
		t.Fatalf("edited = %+v (%s), want same link, name and limit kept, expire set", edited.Invite, edited.Link)
	}
	if seen := f.ownerSeesInvite(t, created.Link); seen.Title != "promo" || seen.UsageLimit != 10 || seen.ExpireDate != expire {
		t.Fatalf("owner sees %+v after edit", seen)
	}

	renamed, err := f.router.BotAPIEditChatInviteLink(f.ctx, f.bot.ID, f.chatID, created.Link, domain.BotAPIChatInviteLinkOptions{
		Name: inviteOption("spring"),
	})
	if err != nil || renamed.Invite.Title != "spring" || renamed.Invite.UsageLimit != 10 || renamed.Invite.ExpireDate != expire {
		t.Fatalf("renamed = %+v, %v; want only the name changed", renamed.Invite, err)
	}
}

func TestBotAPIRevokeChatInviteLinkStopsJoins(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{InviteUsers: true})
	created, err := f.router.BotAPICreateChatInviteLink(f.ctx, f.bot.ID, f.chatID, domain.BotAPIChatInviteLinkOptions{Name: inviteOption("promo")})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	revoked, err := f.router.BotAPIRevokeChatInviteLink(f.ctx, f.bot.ID, f.chatID, created.Link)
	if err != nil {
		t.Fatalf("BotAPIRevokeChatInviteLink: %v", err)
	}
	if !revoked.Invite.Revoked || revoked.Link != created.Link || revoked.Invite.Title != "promo" || revoked.Creator.ID != f.bot.ID {
		t.Fatalf("revoked = %+v (%s), want the same link marked revoked", revoked.Invite, revoked.Link)
	}
	f.memberLeaves(t)
	wantBotAPIError(t, f.memberJoinsVia(t, created.Link), "INVITE_HASH_INVALID")
}

func TestBotAPIExportChatInviteLinkReplacesPrimaryLink(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{InviteUsers: true})

	first, err := f.router.BotAPIExportChatInviteLink(f.ctx, f.bot.ID, f.chatID)
	if err != nil {
		t.Fatalf("first export: %v", err)
	}
	second, err := f.router.BotAPIExportChatInviteLink(f.ctx, f.bot.ID, f.chatID)
	if err != nil {
		t.Fatalf("second export: %v", err)
	}
	if first == second || !strings.Contains(second, "/+") {
		t.Fatalf("exports = %q then %q, want a fresh link", first, second)
	}
	if seen := f.ownerSeesInvite(t, second); !seen.Permanent || seen.Revoked || seen.AdminID != f.bot.ID {
		t.Fatalf("new link = %+v, want the bot's active primary link", seen)
	}
	if seen := f.ownerSeesInvite(t, first); !seen.Revoked {
		t.Fatalf("old link = %+v, want it revoked", seen)
	}
	f.memberLeaves(t)
	wantBotAPIError(t, f.memberJoinsVia(t, first), "INVITE_HASH_INVALID")
	if err := f.memberJoinsVia(t, second); err != nil {
		t.Fatalf("join via new primary link: %v", err)
	}
}

// createChatInviteLink 永不创建主链接：不带任何参数也是额外链接，exportChatInviteLink
// 重建主链接时不会撤销它；把额外链接的名字清空也不会让它变成主链接（review #11）。
func TestBotAPICreateChatInviteLinkWithoutOptionsIsNotPrimary(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{InviteUsers: true})

	plain, err := f.router.BotAPICreateChatInviteLink(f.ctx, f.bot.ID, f.chatID, domain.BotAPIChatInviteLinkOptions{})
	if err != nil {
		t.Fatalf("create without options: %v", err)
	}
	if plain.Invite.Permanent {
		t.Fatalf("plain link = %+v, want an additional (non-primary) link", plain.Invite)
	}
	named, err := f.router.BotAPICreateChatInviteLink(f.ctx, f.bot.ID, f.chatID, domain.BotAPIChatInviteLinkOptions{Name: inviteOption("promo")})
	if err != nil {
		t.Fatalf("create named: %v", err)
	}
	cleared, err := f.router.BotAPIEditChatInviteLink(f.ctx, f.bot.ID, f.chatID, named.Link, domain.BotAPIChatInviteLinkOptions{Name: inviteOption("")})
	if err != nil || cleared.Invite.Permanent {
		t.Fatalf("cleared = %+v, %v; want it to stay an additional link", cleared.Invite, err)
	}
	if _, err := f.router.BotAPIExportChatInviteLink(f.ctx, f.bot.ID, f.chatID); err != nil {
		t.Fatalf("export: %v", err)
	}
	for _, link := range []string{plain.Link, named.Link} {
		if seen := f.ownerSeesInvite(t, link); seen.Revoked || seen.Permanent {
			t.Fatalf("additional link %s after export = %+v, want active and non-primary", link, seen)
		}
	}
}

// 链接名按字符（rune）计数：32 个汉字（96 字节）可以创建和改名，33 个字符被拒绝（review #12）。
func TestChatInviteTitleLimitCountsCharacters(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{InviteUsers: true})
	longest := strings.Repeat("链", domain.MaxChannelInviteTitleLength)

	created, err := f.router.BotAPICreateChatInviteLink(f.ctx, f.bot.ID, f.chatID, domain.BotAPIChatInviteLinkOptions{Name: inviteOption(longest)})
	if err != nil || created.Invite.Title != longest {
		t.Fatalf("create with %d-character name = %+v, %v", domain.MaxChannelInviteTitleLength, created.Invite, err)
	}
	renamed, err := f.router.BotAPIEditChatInviteLink(f.ctx, f.bot.ID, f.chatID, created.Link, domain.BotAPIChatInviteLinkOptions{Name: inviteOption(strings.Repeat("名", domain.MaxChannelInviteTitleLength))})
	if err != nil || renamed.Invite.Title != strings.Repeat("名", domain.MaxChannelInviteTitleLength) {
		t.Fatalf("rename with %d-character name = %+v, %v", domain.MaxChannelInviteTitleLength, renamed.Invite, err)
	}
	_, err = f.router.BotAPICreateChatInviteLink(f.ctx, f.bot.ID, f.chatID, domain.BotAPIChatInviteLinkOptions{Name: inviteOption(longest + "x")})
	wantBotAPIError(t, err, "LIMIT_INVALID")
}

// 别的管理员建的链接，机器人修改后拿到的只是遮住后半段的形式，不能直接拿来入群；
// 机器人自己建的链接仍返回完整链接。
func TestBotAPIChatInviteLinkMasksOtherAdminsLinks(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{InviteUsers: true})
	hash := f.exportInvite(t, false)
	full := publicLinkWithBaseURL(f.router.cfg.PublicBaseURL, "+"+hash)

	edited, err := f.router.BotAPIEditChatInviteLink(f.ctx, f.bot.ID, f.chatID, full, domain.BotAPIChatInviteLinkOptions{Name: inviteOption("renamed")})
	if err != nil {
		t.Fatalf("edit owner's link: %v", err)
	}
	want := strings.TrimSuffix(full, hash[len(hash)/2:]) + "…"
	if edited.Link != want || strings.Contains(edited.Link, hash) || edited.Creator.ID != f.owner.ID {
		t.Fatalf("edited link = %q (creator %d), want %q created by the owner", edited.Link, edited.Creator.ID, want)
	}

	own, err := f.router.BotAPICreateChatInviteLink(f.ctx, f.bot.ID, f.chatID, domain.BotAPIChatInviteLinkOptions{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.HasSuffix(own.Link, "+"+own.Invite.Hash) {
		t.Fatalf("bot's own link = %q, want the full link", own.Link)
	}
}

// 人数上限与需要审批不能同时存在：只传其中一个字段时，也要和链接现有的另一个字段合起来判定。
func TestBotAPIEditChatInviteLinkRejectsLimitWithApproval(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{InviteUsers: true})
	limited, err := f.router.BotAPICreateChatInviteLink(f.ctx, f.bot.ID, f.chatID, domain.BotAPIChatInviteLinkOptions{MemberLimit: inviteOption(10)})
	if err != nil {
		t.Fatalf("create limited: %v", err)
	}
	approval, err := f.router.BotAPICreateChatInviteLink(f.ctx, f.bot.ID, f.chatID, domain.BotAPIChatInviteLinkOptions{CreatesJoinRequest: inviteOption(true)})
	if err != nil {
		t.Fatalf("create approval: %v", err)
	}

	_, err = f.router.BotAPIEditChatInviteLink(f.ctx, f.bot.ID, f.chatID, limited.Link, domain.BotAPIChatInviteLinkOptions{CreatesJoinRequest: inviteOption(true)})
	wantBotAPIError(t, err, domain.BotAPIInviteLimitWithApproval)
	_, err = f.router.BotAPIEditChatInviteLink(f.ctx, f.bot.ID, f.chatID, approval.Link, domain.BotAPIChatInviteLinkOptions{MemberLimit: inviteOption(5)})
	wantBotAPIError(t, err, domain.BotAPIInviteLimitWithApproval)
	if seen := f.ownerSeesInvite(t, limited.Link); seen.RequestNeeded || seen.UsageLimit != 10 {
		t.Fatalf("limited link after rejected edit = %+v, want it untouched", seen)
	}
	if seen := f.ownerSeesInvite(t, approval.Link); !seen.RequestNeeded || seen.UsageLimit != 0 {
		t.Fatalf("approval link after rejected edit = %+v, want it untouched", seen)
	}

	// 关掉审批不会冲突。
	if _, err := f.router.BotAPIEditChatInviteLink(f.ctx, f.bot.ID, f.chatID, limited.Link, domain.BotAPIChatInviteLinkOptions{CreatesJoinRequest: inviteOption(false)}); err != nil {
		t.Fatalf("turn approval off on a limited link: %v", err)
	}
}

// 处理器已经提交了撤销或修改，之后的重读失败不能让机器人收到错误（review #6）：
// 结果按处理器返回的链接组装，修改确实生效。
func TestBotAPIChatInviteLinkSurvivesFailedReload(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{InviteUsers: true})
	created, err := f.router.BotAPICreateChatInviteLink(f.ctx, f.bot.ID, f.chatID, domain.BotAPIChatInviteLinkOptions{Name: inviteOption("promo")})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	f.router.deps.Channels = unreadableInvites{f.channels}

	renamed, err := f.router.BotAPIEditChatInviteLink(f.ctx, f.bot.ID, f.chatID, created.Link, domain.BotAPIChatInviteLinkOptions{Name: inviteOption("spring")})
	if err != nil || renamed.Invite.Title != "spring" || renamed.Link != created.Link || renamed.Creator.ID != f.bot.ID {
		t.Fatalf("rename with failing reload = %+v (%s), %v; want the renamed link", renamed.Invite, renamed.Link, err)
	}
	revoked, err := f.router.BotAPIRevokeChatInviteLink(f.ctx, f.bot.ID, f.chatID, created.Link)
	if err != nil || !revoked.Invite.Revoked || revoked.Invite.Title != "spring" {
		t.Fatalf("revoke with failing reload = %+v, %v; want the revoked link", revoked.Invite, err)
	}
	f.router.deps.Channels = f.channels
	if seen := f.ownerSeesInvite(t, created.Link); !seen.Revoked || seen.Title != "spring" {
		t.Fatalf("owner sees %+v, want the link renamed and revoked", seen)
	}
}
