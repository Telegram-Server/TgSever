package domain

import "fmt"

// MessageReactionChange records the actor's exact selection before and after a write.
type MessageReactionChange struct {
	MessageID    int
	ActorUserID  int64
	ActorChatID  int64
	OldReactions []MessageReaction
	NewReactions []MessageReaction
}

type BotMessageReactionUpdated struct {
	Chat Peer `json:"chat"`
	MessageReactionChange
	Date   int                           `json:"date"`
	Counts []ChannelMessageReactionCount `json:"counts,omitempty"`
}

func (u *BotMessageReactionUpdated) Clone() *BotMessageReactionUpdated {
	if u == nil {
		return nil
	}
	out := *u
	out.OldReactions = append([]MessageReaction(nil), u.OldReactions...)
	out.NewReactions = append([]MessageReaction(nil), u.NewReactions...)
	out.Counts = append([]ChannelMessageReactionCount(nil), u.Counts...)
	return &out
}

func ValidateBotAPIReactionRequest(req EnqueueBotAPIUpdateRequest) error {
	reaction := req.Kind == BotAPIUpdateMessageReaction || req.Kind == BotAPIUpdateMessageReactionCount
	if !reaction && req.Reaction == nil {
		return nil
	}
	u := req.Reaction
	if !reaction || u == nil || req.BotUserID <= 0 || u.MessageID <= 0 || u.MessageID > MaxMessageBoxID || req.SourcePts != 0 || req.MessageID != u.MessageID || req.Peer != u.Chat || u.Date <= 0 || req.Date != u.Date || !ReactionActorValid(u.Actor()) || (u.Chat.Type != PeerTypeUser && u.Chat.Type != PeerTypeChannel) || u.Chat.ID <= 0 || req.ChatMember != nil || req.Callback != nil || req.PreCheckout != nil || req.Ephemeral != nil {
		return fmt.Errorf("invalid bot api reaction update")
	}
	if req.Kind == BotAPIUpdateMessageReactionCount && u.Chat.Type != PeerTypeChannel {
		return fmt.Errorf("invalid anonymous reaction peer")
	}
	for _, reactions := range [][]MessageReaction{u.OldReactions, u.NewReactions} {
		for _, item := range reactions {
			if !item.Valid() {
				return fmt.Errorf("invalid bot api reaction selection")
			}
		}
	}
	for _, count := range u.Counts {
		if !count.Reaction.Valid() || count.Count < 0 {
			return fmt.Errorf("invalid bot api reaction count")
		}
	}
	return nil
}

// ReactionActorKey is the persistent actor namespace: positive users, negative channels.
// User and channel identifiers themselves are always positive.
func ReactionActorKey(peer Peer) int64 {
	if !ReactionActorValid(peer) {
		return 0
	}
	if peer.Type == PeerTypeChannel {
		return -peer.ID
	}
	return peer.ID
}

func ReactionActorValid(peer Peer) bool {
	return peer.ID > 0 && (peer.Type == PeerTypeUser || peer.Type == PeerTypeChannel)
}

func DecodeReactionActorKey(key int64) Peer {
	if key < 0 && key != -1<<63 {
		return Peer{Type: PeerTypeChannel, ID: -key}
	}
	if key > 0 {
		return Peer{Type: PeerTypeUser, ID: key}
	}
	return Peer{}
}

func (c MessageReactionChange) Actor() Peer {
	if c.ActorUserID > 0 && c.ActorChatID == 0 {
		return Peer{Type: PeerTypeUser, ID: c.ActorUserID}
	}
	if c.ActorChatID > 0 && c.ActorUserID == 0 {
		return Peer{Type: PeerTypeChannel, ID: c.ActorChatID}
	}
	return Peer{}
}

func ReactionChangeForActor(messageID int, actor Peer, old, next []MessageReaction) MessageReactionChange {
	out := MessageReactionChange{MessageID: messageID, OldReactions: old, NewReactions: next}
	if actor.Type == PeerTypeChannel {
		out.ActorChatID = actor.ID
	} else {
		out.ActorUserID = actor.ID
	}
	return out
}

func (r ChannelMessagePeerReaction) Actor() Peer {
	if r.ActorChatID > 0 {
		return Peer{Type: PeerTypeChannel, ID: r.ActorChatID}
	}
	return Peer{Type: PeerTypeUser, ID: r.UserID}
}

func (r ChannelMessagePeerReaction) ActorKey() int64 { return ReactionActorKey(r.Actor()) }

func (r *ChannelMessagePeerReaction) SetActorKey(key int64) {
	peer := DecodeReactionActorKey(key)
	r.UserID, r.ActorChatID = 0, 0
	if peer.Type == PeerTypeChannel {
		r.ActorChatID = peer.ID
	} else {
		r.UserID = peer.ID
	}
}
