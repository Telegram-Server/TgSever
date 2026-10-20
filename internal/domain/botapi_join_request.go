package domain

import "errors"

// BotChatJoinRequest is a request-time snapshot, never re-read from pending importers.
type BotChatJoinRequest struct {
	Chat       Peer           `json:"chat"`
	UserID     int64          `json:"user_id"`
	UserChatID int64          `json:"user_chat_id"`
	Date       int            `json:"date"`
	About      string         `json:"about,omitempty"`
	Invite     *ChannelInvite `json:"invite,omitempty"`
}

func (u *BotChatJoinRequest) Clone() *BotChatJoinRequest {
	if u == nil {
		return nil
	}
	out := *u
	if u.Invite != nil {
		invite := *u.Invite
		out.Invite = &invite
	}
	return &out
}

func ValidateBotAPIJoinRequest(req EnqueueBotAPIUpdateRequest) error {
	if req.Kind != BotAPIUpdateChatJoinRequest {
		if req.JoinRequest != nil {
			return errors.New("unexpected join request payload")
		}
		return nil
	}
	u := req.JoinRequest
	if req.BotUserID <= 0 || u == nil || u.Chat != req.Peer || u.Chat.Type != PeerTypeChannel || u.Chat.ID <= 0 || u.UserID <= 0 || u.UserChatID <= 0 || u.Date <= 0 || u.Date != req.Date || req.MessageID != 0 || req.SourcePts != 0 || req.ChatMember != nil || req.Reaction != nil || req.Callback != nil || req.PreCheckout != nil || req.Ephemeral != nil {
		return errors.New("invalid bot api join request")
	}
	if u.Invite != nil && (u.Invite.ChannelID != u.Chat.ID || u.Invite.Hash == "") {
		return errors.New("invalid join request invite")
	}
	return nil
}

func NewChannelJoinRequest(channelID, userID int64, date int, invite *ChannelInvite) *BotChatJoinRequest {
	return (&BotChatJoinRequest{Chat: Peer{Type: PeerTypeChannel, ID: channelID}, UserID: userID, UserChatID: userID, Date: date, Invite: invite}).Clone()
}

func NewChannelMemberJoin(invite ChannelInvite, approvedBy int64, message ChannelMessage, event ChannelUpdateEvent) ChannelMemberJoin {
	join := ChannelMemberJoin{ApprovedBy: approvedBy, ViaJoinRequest: approvedBy != 0, Message: message, Event: event}
	if invite.Hash != "" {
		join.Invite = &invite
	}
	return join
}
