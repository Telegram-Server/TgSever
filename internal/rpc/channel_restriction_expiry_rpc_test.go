package rpc

import (
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// 限制期限在 RPC 发送路径上生效：禁言到期前 messages.sendMessage 报 CHAT_WRITE_FORBIDDEN，
// 到期后可发。读取时的生效值由 storetest.ChannelRestrictionExpiry 覆盖。
func TestChannelRestrictionExpiryGatesSendMessage(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	now := int(time.Now().Unix())
	restrict := func(until, date int) {
		t.Helper()
		if _, err := f.channels.EditBanned(f.ctx, f.owner.ID, domain.EditChannelBannedRequest{
			UserID:       f.owner.ID,
			ChannelID:    f.channel.ID,
			Participant:  domain.Peer{Type: domain.PeerTypeUser, ID: f.member.ID},
			BannedRights: domain.ChannelBannedRights{SendMessages: true, SendMedia: true, UntilDate: until},
			Date:         date,
		}); err != nil {
			t.Fatalf("restrict member: %v", err)
		}
	}
	send := func(randomID int64) error {
		_, err := f.router.onMessagesSendMessage(WithUserID(f.ctx, f.member.ID), &tg.MessagesSendMessageRequest{
			Peer:     &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
			Message:  "hello",
			RandomID: randomID,
		})
		return err
	}

	restrict(now+3600, now)
	if err := send(7_700_001); err == nil || !strings.Contains(err.Error(), "CHAT_WRITE_FORBIDDEN") {
		t.Fatalf("send before expiry err = %v, want CHAT_WRITE_FORBIDDEN", err)
	}

	restrict(now-60, now-7200)
	if err := send(7_700_002); err != nil {
		t.Fatalf("send after expiry: %v", err)
	}
}

// 被限制（非踢出）的非成员仍能经 channels.joinChannel 自行加入。限制保留与记录形状由
// storetest.ChannelRestrictNonMember 覆盖，投影由 tgChannelParticipant 的纯函数测试覆盖。
func TestChannelRestrictNonMemberStaysJoinable(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	now := int(time.Now().Unix())
	if _, err := f.channels.LeaveChannel(f.ctx, f.member.ID, f.channel.ID, now); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if _, err := f.channels.EditBanned(f.ctx, f.owner.ID, domain.EditChannelBannedRequest{
		UserID:       f.owner.ID,
		ChannelID:    f.channel.ID,
		Participant:  domain.Peer{Type: domain.PeerTypeUser, ID: f.member.ID},
		BannedRights: domain.ChannelBannedRights{SendMedia: true, UntilDate: now + 3600},
		Date:         now,
	}); err != nil {
		t.Fatalf("restrict non-member: %v", err)
	}
	if _, err := f.router.onChannelsJoinChannel(WithUserID(f.ctx, f.member.ID), f.inputChannel()); err != nil {
		t.Fatalf("join as restricted non-member: %v", err)
	}
}
