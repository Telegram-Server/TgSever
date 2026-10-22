package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// editBannedSenderChat 是 channels.editBanned 收到频道 peer 时的分支（#137）：只接受全封
// （view_messages）与解封，封禁记录与管理日志由 app/store 负责。频道 peer 不是成员，没有
// 成员更新可推，返回空 Updates。
func (r *Router) editBannedSenderChat(ctx context.Context, userID, channelID, senderChatID int64, rights tg.ChatBannedRights) (tg.UpdatesClass, error) {
	date := int(r.clock.Now().Unix())
	if _, err := r.deps.Channels.EditBannedSenderChat(ctx, userID, domain.EditChannelBannedSenderChatRequest{
		UserID:       userID,
		ChannelID:    channelID,
		SenderChatID: senderChatID,
		BannedRights: domainChannelBannedRights(rights),
		Date:         date,
	}); err != nil {
		return nil, senderChatBanErr(err)
	}
	return &tg.Updates{Updates: []tg.UpdateClass{}, Users: []tg.UserClass{}, Chats: []tg.ChatClass{}, Date: date}, nil
}

func senderChatBanErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrChannelBannedRightsInvalid):
		return tgerr400("BANNED_RIGHTS_INVALID")
	case errors.Is(err, domain.ErrChannelSenderChatInvalid):
		return peerIDInvalidErr()
	default:
		return channelAdminErr(err)
	}
}

// senderChatParticipant 处理 channels.getParticipant 的频道 peer：只有被封禁一种状态。
func (r *Router) senderChatParticipant(ctx context.Context, userID, channelID, senderChatID int64) (*tg.ChannelsChannelParticipant, error) {
	member, sender, err := r.deps.Channels.GetSenderChatParticipant(ctx, userID, channelID, senderChatID)
	if err != nil {
		if errors.Is(err, domain.ErrChannelAdminRequired) {
			return nil, tgerr400("CHAT_ADMIN_REQUIRED")
		}
		return nil, channelParticipantErr(err)
	}
	participant := tgChannelParticipant(userID, member)
	users := r.tgUsersForIDs(ctx, userID, channelParticipantUserRefs(participant))
	chats := tgSenderChats(userID, []domain.Channel{sender})
	r.applyPeerReadModels(ctx, userID, users, chats)
	return &tg.ChannelsChannelParticipant{Participant: participant, Chats: chats, Users: users}, nil
}

// tgSenderChats 把被封禁频道 peer 的基础行投影成 min channel：查看者通常不是这些频道的成员。
func tgSenderChats(viewerUserID int64, channels []domain.Channel) []tg.ChatClass {
	out := make([]tg.ChatClass, 0, len(channels))
	for _, channel := range channels {
		if channel.ID == 0 {
			continue
		}
		out = append(out, tgChannelChatMin(viewerUserID, channel))
	}
	return out
}

// tgSenderChatParticipant 是频道 peer（SenderChatID 非零）的参与者投影：被封禁时为
// channelParticipantBanned，否则为 channelParticipantLeft，peer 都是频道。
func tgSenderChatParticipant(member domain.ChannelMember) tg.ChannelParticipantClass {
	peer := &tg.PeerChannel{ChannelID: member.SenderChatID}
	if member.Status == domain.ChannelMemberKicked || member.BannedRights.ViewMessages {
		return &tg.ChannelParticipantBanned{
			Left:         true,
			Peer:         peer,
			KickedBy:     member.InviterUserID,
			Date:         member.LeftAt,
			BannedRights: tgChatBannedRights(member.BannedRights),
		}
	}
	return &tg.ChannelParticipantLeft{Peer: peer}
}
