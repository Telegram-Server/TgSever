package rpc

import (
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// 限制期限端到端：禁言到期前 messages.sendMessage 被拒，到期后可发；
// channels.getParticipant 看到的也是生效值。
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
	if got := tgChannelParticipantForUpdate(f.owner.ID, f.participant(t, f.member.ID)); got == nil {
		t.Fatal("participant after expiry projected to nil")
	} else if _, banned := got.(*tg.ChannelParticipantBanned); banned {
		t.Fatalf("participant after expiry = %#v, want an unrestricted participant", got)
	}
}

// 限制一个不在群里的用户：投影为 channelParticipantBanned(left=true)，他仍能自行加入且限制保留。
func TestChannelRestrictNonMemberStaysJoinable(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	now := int(time.Now().Unix())
	if _, err := f.channels.LeaveChannel(f.ctx, f.member.ID, f.channel.ID, now); err != nil {
		t.Fatalf("leave: %v", err)
	}
	rights := domain.ChannelBannedRights{SendMedia: true, UntilDate: now + 3600}
	if _, err := f.channels.EditBanned(f.ctx, f.owner.ID, domain.EditChannelBannedRequest{
		UserID:       f.owner.ID,
		ChannelID:    f.channel.ID,
		Participant:  domain.Peer{Type: domain.PeerTypeUser, ID: f.member.ID},
		BannedRights: rights,
		Date:         now,
	}); err != nil {
		t.Fatalf("restrict non-member: %v", err)
	}
	// updateChannelParticipant 的投影（channels.getParticipant 走 tgChannelParticipant，
	// 目前对任何受限/被踢记录都不投影 channelParticipantBanned，见汇报）。
	got := tgChannelParticipantForUpdate(f.owner.ID, f.participant(t, f.member.ID))
	banned, ok := got.(*tg.ChannelParticipantBanned)
	if !ok || !banned.Left || !banned.BannedRights.SendMedia || banned.BannedRights.ViewMessages {
		t.Fatalf("restricted non-member = %#v, want channelParticipantBanned left=true with send_media", got)
	}
	if _, err := f.router.onChannelsJoinChannel(WithUserID(f.ctx, f.member.ID), &tg.InputChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}); err != nil {
		t.Fatalf("join as restricted non-member: %v", err)
	}
	if m := f.participant(t, f.member.ID); m.Status != domain.ChannelMemberActive || m.BannedRights != rights {
		t.Fatalf("rejoined member = %+v, want active with %+v", m, rights)
	}
}
