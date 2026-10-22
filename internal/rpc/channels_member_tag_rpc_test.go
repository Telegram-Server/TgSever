package rpc

import (
	"strings"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

func TestChannelMemberTagUnicodeAndMessageRPC(t *testing.T) {
	f := newRPCChannelFixture(t)
	owner := f.user(561, "15550005601", "Owner")
	member := f.user(562, "15550005602", "Member")
	bot, err := f.users.Create(f.ctx, domain.User{AccessHash: 563, Phone: "15550005603", FirstName: "Tagger", Bot: true, BotInfoVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	group := f.createLegacyMegagroup(owner, "Unicode tags", member, bot)
	if _, err := f.router.onChannelsEditAdmin(f.userCtx(owner), &tg.ChannelsEditAdminRequest{Channel: inputChannel(group), UserID: inputUser(bot), AdminRights: tg.ChatAdminRights{ManageRanks: true}}); err != nil {
		t.Fatal(err)
	}
	edit := func(rank string) error {
		_, err := f.router.onMessagesEditChatParticipantRank(f.userCtx(bot), &tg.MessagesEditChatParticipantRankRequest{Peer: inputPeerChannel(group), Participant: &tg.InputPeerUser{UserID: member.ID, AccessHash: member.AccessHash}, Rank: rank})
		return err
	}
	tag := strings.Repeat("中", 16)
	if err := edit(tag); err != nil {
		t.Fatalf("16 Chinese characters = %v", err)
	}
	for _, tc := range []struct{ name, rank, errorCode string }{
		{"ASCII too long", strings.Repeat("a", 17), "ADMIN_RANK_INVALID"},
		{"Chinese too long", strings.Repeat("中", 17), "ADMIN_RANK_INVALID"},
		{"emoji", "guide🙂", "ADMIN_RANK_EMOJI_NOT_ALLOWED"},
		{"folded hands", "guide🙏", "ADMIN_RANK_EMOJI_NOT_ALLOWED"},
		{"rocket", "guide🚀", "ADMIN_RANK_EMOJI_NOT_ALLOWED"},
		{"robot", "guide🤖", "ADMIN_RANK_EMOJI_NOT_ALLOWED"},
		{"Unicode17 tail", "guide\U0001FAF8", "ADMIN_RANK_EMOJI_NOT_ALLOWED"},
		{"BMP emoji", "guide☀", "ADMIN_RANK_EMOJI_NOT_ALLOWED"},
		{"keycap emoji", "guide1️⃣", "ADMIN_RANK_EMOJI_NOT_ALLOWED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := edit(tc.rank); err == nil || !strings.Contains(err.Error(), tc.errorCode) {
				t.Fatalf("rank error = %v, want %s", err, tc.errorCode)
			}
		})
	}
	single, err := f.router.onChannelsGetParticipant(f.userCtx(owner), &tg.ChannelsGetParticipantRequest{Channel: inputChannel(group), Participant: &tg.InputPeerUser{UserID: member.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if rank, _ := single.Participant.(*tg.ChannelParticipant).GetRank(); rank != tag {
		t.Fatalf("failed edits changed rank = %q", rank)
	}
	sessions := newNudgeSessions([]int64{owner.ID, bot.ID})
	sessions.channelViewers = map[int64][]int64{group.ID: {owner.ID, bot.ID}}
	sessions.channelMembers = map[int64][]int64{group.ID: {owner.ID, bot.ID}}
	f.router.deps.Sessions = sessions
	updates, err := f.router.onMessagesSendMessage(f.userCtx(member), &tg.MessagesSendMessageRequest{Peer: inputPeerChannel(group), Message: "tagged", RandomID: 5601})
	if err != nil {
		t.Fatal(err)
	}
	sent := newMessageFromUpdates(t, updates)
	if rank, _ := sent.GetFromRank(); rank != tag {
		t.Fatalf("message from_rank = %q", rank)
	}
	for _, recipient := range []domain.User{owner, bot} {
		peerUpdates, ok := sessions.msgFor(recipient.ID).(tg.UpdatesClass)
		if !ok {
			t.Fatalf("peer %d updates = %T", recipient.ID, sessions.msgFor(recipient.ID))
		}
		pushed := false
		for _, message := range channelMessagesFromUpdates(peerUpdates) {
			if m, ok := message.(*tg.Message); ok && m.ID == sent.ID {
				pushed = true
				if rank, _ := m.GetFromRank(); rank != tag {
					t.Fatalf("peer %d from_rank = %q", recipient.ID, rank)
				}
			}
		}
		if !pushed {
			t.Fatalf("tagged message not pushed to peer %d", recipient.ID)
		}
	}
	queried, err := f.router.onChannelsGetMessages(f.userCtx(owner), &tg.ChannelsGetMessagesRequest{Channel: inputChannel(group), ID: []tg.InputMessageClass{&tg.InputMessageID{ID: sent.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	messages, _, _ := searchMessagesPayload(t, queried)
	if rank, _ := messages[0].(*tg.Message).GetFromRank(); rank != tag {
		t.Fatalf("history from_rank = %q", rank)
	}
	if err := edit(""); err != nil {
		t.Fatal(err)
	}
	cleared, err := f.router.onMessagesSendMessage(f.userCtx(member), &tg.MessagesSendMessageRequest{Peer: inputPeerChannel(group), Message: "untagged", RandomID: 5602})
	if err != nil {
		t.Fatal(err)
	}
	if rank, present := newMessageFromUpdates(t, cleared).GetFromRank(); present || rank != "" {
		t.Fatalf("cleared from_rank = %q", rank)
	}
	if err := edit("team #123*"); err != nil {
		t.Fatalf("ordinary ASCII digits = %v", err)
	}
}
