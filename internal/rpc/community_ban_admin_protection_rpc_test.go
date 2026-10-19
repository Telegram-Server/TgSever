package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appcommunities "telesrv/internal/app/communities"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// 社区封禁不能绕过频道层的管理员保护：非群主、非提拔人封禁关联频道的在任管理员返回 USER_ADMIN_INVALID。
func TestCommunityBanOfOthersChannelAdminIsUserAdminInvalid(t *testing.T) {
	ctx := WithLayer(context.Background(), communitiesLayer)
	users := memory.NewUserStore()
	newUser := func(hash int64, phone, name string) domain.User {
		t.Helper()
		u, err := users.Create(ctx, domain.User{AccessHash: hash, Phone: phone, FirstName: name})
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	owner := newUser(731, "15552000031", "Ban Owner")
	admin := newUser(732, "15552000032", "Community Admin")
	target := newUser(733, "15552000033", "Channel Admin")
	channels := memory.NewChannelStore()
	channelService := appchannels.NewService(channels)
	communityService := appcommunities.NewService(memory.NewCommunityStore(users, channels, nil, nil))
	r := New(Config{}, Deps{Users: appusers.NewService(users), Channels: channelService, Communities: communityService}, zaptest.NewLogger(t), clock.System)

	channel := communityRPCChannel(t, channelService, owner, "Ban Protected", admin, target)
	if _, err := channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: owner.ID, ChannelID: channel.ID, MemberID: target.ID, AdminRights: domain.ChannelAdminRights{BanUsers: true}, Date: 1_800_100_001}); err != nil {
		t.Fatal(err)
	}
	view, err := communityService.Create(ctx, owner.ID, domain.CreateCommunityRequest{
		Title: "Ban Community", InitialPeer: domain.Peer{Type: domain.PeerTypeChannel, ID: channel.ID},
		Visibility: domain.CommunityPeerVisible, Date: 1_800_100_002,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := communityService.EditAdmin(ctx, owner.ID, domain.CommunityEditAdminRequest{CommunityID: view.Community.ID, UserID: admin.ID, Rights: domain.ChannelAdminRights{BanUsers: true}, Date: 1_800_100_003}); err != nil {
		t.Fatal(err)
	}

	_, err = r.onCommunitiesToggleParticipantBanned(WithUserID(ctx, admin.ID), &tg.CommunitiesToggleParticipantBannedRequest{
		Community:   &tg.InputChannel{ChannelID: view.Community.ID, AccessHash: view.Community.AccessHash},
		Participant: &tg.InputPeerUser{UserID: target.ID, AccessHash: target.AccessHash},
	})
	if err == nil || !tgerr.Is(err, "USER_ADMIN_INVALID") {
		t.Fatalf("community ban of channel admin by non-promoter err = %v, want USER_ADMIN_INVALID", err)
	}
	if m, err := channels.GetParticipant(ctx, owner.ID, channel.ID, target.ID); err != nil || m.Role != domain.ChannelRoleAdmin || m.Status != domain.ChannelMemberActive {
		t.Fatalf("channel admin after rejected community ban = %+v, %v", m, err)
	}
}
