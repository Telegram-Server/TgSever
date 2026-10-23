package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// 经聊天文件夹（chatlist）加入、退出频道，以及经 chatlist 提交入群申请时，store 在源事件的
// 事务里写下的 bot qts 记录要在线推给 bot，每条恰好一次；入群申请以 INVITE_REQUEST_SENT
// 结束，记录照样要推。
func TestChatlistOperationsPushBotUpdatesOnce(t *testing.T) {
	const userID, botID, channelID = int64(2002), int64(3003), int64(42)
	channel := domain.Channel{ID: channelID, AccessHash: 4200, Title: "Team channel", Megagroup: true, Pts: 9}
	admin := domain.ChannelMember{ChannelID: channelID, UserID: botID, Role: domain.ChannelRoleAdmin, Status: domain.ChannelMemberActive, AdminRights: domain.ChannelAdminRights{InviteUsers: true}}
	participant := domain.BotUpdate{BotUserID: botID, Qts: 2, Date: 100, ChannelParticipant: &domain.BotChannelParticipantUpdate{
		Channel: channel, ActorUserID: userID,
		Current: domain.ChannelMember{ChannelID: channelID, UserID: userID, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberActive},
		Viewer:  admin,
	}}
	requester := domain.BotUpdate{BotUserID: botID, Qts: 3, Date: 100, ChatInviteRequester: &domain.BotChatInviteRequesterUpdate{
		Channel: channel, RequesterUserID: userID, Admin: admin,
	}}
	joinResult := domain.ChatlistJoinResult{
		Folder: domain.DialogFolder{ID: 3, Title: "Team", IsChatlist: true, IncludePeers: []domain.DialogFolderPeer{
			{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}, AccessHash: 4200},
		}},
		ChannelResults: []domain.CreateChannelResult{{
			Channel:    channel,
			Members:    []domain.ChannelMember{participant.ChannelParticipant.Current},
			BotUpdates: []domain.BotUpdate{participant},
		}},
	}
	peers := []tg.InputPeerClass{&tg.InputPeerChannel{ChannelID: channelID, AccessHash: 4200}}
	chatlist := tg.InputChatlistDialogFilter{FilterID: 3}

	for _, tc := range []struct {
		name    string
		records []domain.BotUpdate
		opErr   error
		wantErr string
		call    func(r *Router, ctx context.Context) error
	}{
		{"join invite", []domain.BotUpdate{participant}, nil, "", func(r *Router, ctx context.Context) error {
			_, err := r.onChatlistsJoinChatlistInvite(ctx, &tg.ChatlistsJoinChatlistInviteRequest{Slug: "slug", Peers: peers})
			return err
		}},
		{"join updates", []domain.BotUpdate{participant}, nil, "", func(r *Router, ctx context.Context) error {
			_, err := r.onChatlistsJoinChatlistUpdates(ctx, &tg.ChatlistsJoinChatlistUpdatesRequest{Chatlist: chatlist, Peers: peers})
			return err
		}},
		{"leave", []domain.BotUpdate{participant}, nil, "", func(r *Router, ctx context.Context) error {
			_, err := r.onChatlistsLeaveChatlist(ctx, &tg.ChatlistsLeaveChatlistRequest{Chatlist: chatlist, Peers: peers})
			return err
		}},
		{"join request via invite", []domain.BotUpdate{requester}, domain.ErrInviteRequestSent, "INVITE_REQUEST_SENT", func(r *Router, ctx context.Context) error {
			_, err := r.onChatlistsJoinChatlistInvite(ctx, &tg.ChatlistsJoinChatlistInviteRequest{Slug: "slug", Peers: peers})
			return err
		}},
		{"join request via updates", []domain.BotUpdate{requester}, domain.ErrInviteRequestSent, "INVITE_REQUEST_SENT", func(r *Router, ctx context.Context) error {
			_, err := r.onChatlistsJoinChatlistUpdates(ctx, &tg.ChatlistsJoinChatlistUpdatesRequest{Chatlist: chatlist, Peers: peers})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessions := &phoneCaptureSessions{}
			fake := &fakeChatlistsService{joinResult: joinResult, joinUpdFolder: joinResult.Folder, botUpdates: tc.records, opErr: tc.opErr}
			r := New(Config{}, Deps{Chatlists: fake, Sessions: sessions}, zap.NewNop(), clock.System)
			err := tc.call(r, WithUserID(context.Background(), userID))
			if tc.wantErr == "" && err != nil {
				t.Fatalf("call: %v", err)
			}
			if tc.wantErr != "" && !tgerr.Is(err, tc.wantErr) {
				t.Fatalf("call err = %v, want %s", err, tc.wantErr)
			}
			var qts []int
			for _, update := range pushedToBot(sessions, botID) {
				switch u := update.(type) {
				case *tg.UpdateChannelParticipant:
					qts = append(qts, u.Qts)
				case *tg.UpdateBotChatInviteRequester:
					qts = append(qts, u.Qts)
				}
			}
			if len(qts) != len(tc.records) || qts[0] != tc.records[0].Qts {
				t.Fatalf("bot pushes qts = %v, want each record once (%d)", qts, tc.records[0].Qts)
			}
		})
	}
}
