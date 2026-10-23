package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"

	appcommunities "telesrv/internal/app/communities"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// bot 只经 qts 序列收 updateChannelParticipant：TDLib 把 qts 为 0 的这类 update 当作错误 qts
// 丢弃并触发 getDifference。下面三条路径以前对 bot 推无 qts 的旧式更新。

// assertNoPlainParticipantUpdates 断言推给 userID 的 updateChannelParticipant 都带 qts。
func (f *adminBotAudienceFixture) assertNoPlainParticipantUpdates(t *testing.T, who string, userID int64) {
	t.Helper()
	for _, p := range f.pushedParticipants(userID) {
		if p.Qts == 0 {
			t.Fatalf("%s received an updateChannelParticipant without qts: %+v", who, p)
		}
	}
}

// 成员 tag（rank）变更是权限变更（#146）：被改的 bot 自己与管理员 bot 各收一条带 qts 的
// updateChannelParticipant；不是管理员的 bot 收不到别人的变更。
func TestEditChatParticipantRankReachesBotsOnlyViaQts(t *testing.T) {
	ctx := context.Background()
	fx := newAdminBotAudienceFixture(t)
	owner, adminBot, plainBot, human := fx.newUser(t, 8401, false), fx.newUser(t, 8402, true), fx.newUser(t, 8403, true), fx.newUser(t, 8404, false)
	now := int(time.Now().Unix())
	created, err := fx.channels.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{Title: "rank", Megagroup: true, Date: now})
	if err != nil {
		t.Fatal(err)
	}
	channel := created.Channel
	if _, err := fx.channels.InviteToChannel(ctx, owner.ID, channel.ID, []int64{plainBot.ID, human.ID}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.channels.EditAdmin(ctx, owner.ID, domain.EditChannelAdminRequest{ChannelID: channel.ID, MemberID: adminBot.ID, AdminRights: domain.ChannelAdminRights{BanUsers: true}, Date: now}); err != nil {
		t.Fatal(err)
	}
	adminBefore, plainBefore := len(fx.pushedParticipants(adminBot.ID)), len(fx.pushedParticipants(plainBot.ID))
	peer := &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	editRank := func(target domain.User, rank string) {
		t.Helper()
		if _, err := fx.router.onMessagesEditChatParticipantRank(fx.userCtx(owner), &tg.MessagesEditChatParticipantRankRequest{
			Peer:        peer,
			Participant: &tg.InputPeerUser{UserID: target.ID, AccessHash: target.AccessHash},
			Rank:        rank,
		}); err != nil {
			t.Fatalf("edit rank of %d: %v", target.ID, err)
		}
	}
	editRank(human, "tag")
	editRank(plainBot, "bot tag")

	fx.assertNoPlainParticipantUpdates(t, "admin bot", adminBot.ID)
	fx.assertNoPlainParticipantUpdates(t, "plain bot", plainBot.ID)
	gotAdmin := fx.pushedParticipants(adminBot.ID)[adminBefore:]
	if len(gotAdmin) != 2 || gotAdmin[0].UserID != human.ID || gotAdmin[1].UserID != plainBot.ID {
		t.Fatalf("admin bot rank updates = %+v, want the member's then the plain bot's", gotAdmin)
	}
	gotPlain := fx.pushedParticipants(plainBot.ID)[plainBefore:]
	if len(gotPlain) != 1 || gotPlain[0].UserID != plainBot.ID {
		t.Fatalf("plain bot rank updates = %+v, want only its own", gotPlain)
	}
	if np, _ := gotPlain[0].GetNewParticipant(); participantKind(np) != "self" {
		t.Fatalf("plain bot new participant = %T", np)
	}
	if st, _ := fx.botUpdates.BotUpdatesState(ctx, plainBot.ID); st.Qts != gotPlain[0].Qts {
		t.Fatalf("plain bot qts state %d, pushed %d", st.Qts, gotPlain[0].Qts)
	}
}

// 群主转让不生成 qts 记录（已接受的偏离），但也不能对 bot 推无 qts 的 updateChannelParticipant。
func TestOwnershipTransferPushesNoPlainParticipantUpdateToBots(t *testing.T) {
	ctx := context.Background()
	fx := newAdminBotAudienceFixture(t)
	fx.router.deps.Account = acceptPasswordAccountService{}
	owner, adminBot, plainBot, heir := fx.newUser(t, 8411, false), fx.newUser(t, 8412, true), fx.newUser(t, 8413, true), fx.newUser(t, 8414, false)
	now := int(time.Now().Unix())
	created, err := fx.channels.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{Title: "transfer", Megagroup: true, Date: now})
	if err != nil {
		t.Fatal(err)
	}
	channel := created.Channel
	if _, err := fx.channels.InviteToChannel(ctx, owner.ID, channel.ID, []int64{plainBot.ID, heir.ID}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.channels.EditAdmin(ctx, owner.ID, domain.EditChannelAdminRequest{ChannelID: channel.ID, MemberID: adminBot.ID, AdminRights: domain.ChannelAdminRights{BanUsers: true}, Date: now}); err != nil {
		t.Fatal(err)
	}
	ownerUpdates, err := fx.router.onMessagesEditChatCreator(fx.userCtx(owner), &tg.MessagesEditChatCreatorRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		UserID:   &tg.InputUser{UserID: heir.ID, AccessHash: heir.AccessHash},
		Password: &tg.InputCheckPasswordSRP{SRPID: 1, A: []byte{1}, M1: []byte{2}},
	})
	if err != nil {
		t.Fatalf("transfer: %v", err)
	}
	fx.assertNoPlainParticipantUpdates(t, "admin bot", adminBot.ID)
	fx.assertNoPlainParticipantUpdates(t, "plain bot", plainBot.ID)
	// 人类成员照旧收到成员变化。
	if !hasChannelParticipantUpdate(ownerUpdates.(*tg.Updates)) {
		t.Fatalf("owner transfer result lost its participant updates: %+v", ownerUpdates)
	}
}

// 社区封禁同样不生成 qts 记录（已接受的偏离），不能对 bot 推无 qts 的 updateChannelParticipant。
func TestCommunityBanPushesNoPlainParticipantUpdateToBots(t *testing.T) {
	ctx := WithLayer(context.Background(), communitiesLayer)
	fx := newAdminBotAudienceFixture(t)
	communities := appcommunities.NewService(memory.NewCommunityStore(fx.users, fx.store, nil, nil))
	fx.router.deps.Communities = communities
	owner, adminBot, plainBot, human := fx.newUser(t, 8421, false), fx.newUser(t, 8422, true), fx.newUser(t, 8423, true), fx.newUser(t, 8424, false)
	now := int(time.Now().Unix())
	created, err := fx.channels.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{Title: "community ban", Megagroup: true, Date: now})
	if err != nil {
		t.Fatal(err)
	}
	channel := created.Channel
	if _, err := fx.channels.InviteToChannel(ctx, owner.ID, channel.ID, []int64{plainBot.ID, human.ID}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.channels.EditAdmin(ctx, owner.ID, domain.EditChannelAdminRequest{ChannelID: channel.ID, MemberID: adminBot.ID, AdminRights: domain.ChannelAdminRights{BanUsers: true}, Date: now}); err != nil {
		t.Fatal(err)
	}
	view, err := communities.Create(ctx, owner.ID, domain.CreateCommunityRequest{
		Title: "bots", InitialPeer: domain.Peer{Type: domain.PeerTypeChannel, ID: channel.ID},
		Visibility: domain.CommunityPeerVisible, Date: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	humanBefore := len(fx.pushedParticipants(owner.ID))
	if _, err := fx.router.onCommunitiesToggleParticipantBanned(WithUserID(ctx, owner.ID), &tg.CommunitiesToggleParticipantBannedRequest{
		Community:   &tg.InputChannel{ChannelID: view.Community.ID, AccessHash: view.Community.AccessHash},
		Participant: &tg.InputPeerUser{UserID: human.ID, AccessHash: human.AccessHash},
	}); err != nil {
		t.Fatalf("community ban: %v", err)
	}
	fx.assertNoPlainParticipantUpdates(t, "admin bot", adminBot.ID)
	fx.assertNoPlainParticipantUpdates(t, "plain bot", plainBot.ID)
	if len(fx.pushedParticipants(owner.ID)) == humanBefore {
		t.Fatal("the owner no longer receives the ban's participant update")
	}
}

func hasChannelParticipantUpdate(updates *tg.Updates) bool {
	for _, update := range updates.Updates {
		if _, ok := update.(*tg.UpdateChannelParticipant); ok {
			return true
		}
	}
	return false
}
