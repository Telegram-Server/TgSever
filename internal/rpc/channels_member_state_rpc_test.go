package rpc

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
	appchannels "telesrv/internal/app/channels"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestChannelParticipantRestrictedAndKickedRPC(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	owner, _ := users.Create(ctx, domain.User{AccessHash: 71, Phone: "15550004401", FirstName: "Owner"})
	member, _ := users.Create(ctx, domain.User{AccessHash: 72, Phone: "15550004402", FirstName: "Member"})
	r := New(Config{}, Deps{Users: appusers.NewService(users), Channels: appchannels.NewService(memory.NewChannelStore())}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{Users: []tg.InputUserClass{&tg.InputUser{UserID: member.ID, AccessHash: member.AccessHash}}, Title: "Member states"})
	if err != nil {
		t.Fatal(err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	input := &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	target := &tg.InputPeerUser{UserID: member.ID, AccessHash: member.AccessHash}
	if _, err := r.onMessagesEditChatParticipantRank(WithUserID(ctx, owner.ID), &tg.MessagesEditChatParticipantRankRequest{Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}, Participant: target, Rank: "guide"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		rights tg.ChatBannedRights
		filter tg.ChannelParticipantsFilterClass
		left   bool
	}{
		{"restricted", tg.ChatBannedRights{SendMessages: true, UntilDate: int(time.Now().Add(time.Hour).Unix())}, &tg.ChannelParticipantsBanned{}, false},
		{"kicked", tg.ChatBannedRights{ViewMessages: true}, &tg.ChannelParticipantsKicked{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			updates, err := r.onChannelsEditBanned(WithUserID(ctx, owner.ID), &tg.ChannelsEditBannedRequest{Channel: input, Participant: target, BannedRights: tc.rights})
			if err != nil {
				t.Fatal(err)
			}
			var changed tg.ChannelParticipantClass
			for _, update := range updates.(*tg.Updates).Updates {
				if u, ok := update.(*tg.UpdateChannelParticipant); ok {
					changed, _ = u.GetNewParticipant()
				}
			}
			single, err := r.onChannelsGetParticipant(WithUserID(ctx, owner.ID), &tg.ChannelsGetParticipantRequest{Channel: input, Participant: target})
			if err != nil {
				t.Fatal(err)
			}
			banned, ok := single.Participant.(*tg.ChannelParticipantBanned)
			if !ok {
				t.Fatalf("participant = %T, want banned", single.Participant)
			}
			if banned.Left != tc.left || banned.BannedRights.SendMessages != tc.rights.SendMessages || banned.BannedRights.ViewMessages != tc.rights.ViewMessages || banned.BannedRights.UntilDate != tc.rights.UntilDate {
				t.Fatalf("participant state = %+v", banned)
			}
			if !tc.left {
				if rank, _ := banned.GetRank(); rank != "guide" {
					t.Fatalf("restricted rank = %q", rank)
				}
			}
			if !reflect.DeepEqual(single.Participant, changed) {
				t.Fatalf("query = %#v, update = %#v", single.Participant, changed)
			}
			list, err := r.onChannelsGetParticipants(WithUserID(ctx, owner.ID), &tg.ChannelsGetParticipantsRequest{Channel: input, Filter: tc.filter, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, participant := range list.(*tg.ChannelsChannelParticipants).Participants {
				if reflect.DeepEqual(participant, single.Participant) {
					found = true
				}
			}
			if !found {
				t.Fatalf("list missing queried participant: %#v", list)
			}
		})
	}
}
