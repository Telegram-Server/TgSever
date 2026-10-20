package domain

import "fmt"

// MessageReactionChange records the actor's exact selection before and after a write.
type MessageReactionChange struct {
	MessageID    int
	ActorUserID  int64
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
	if !reaction || u == nil || req.BotUserID <= 0 || u.MessageID <= 0 || u.MessageID > MaxMessageBoxID || req.SourcePts != 0 || req.MessageID != u.MessageID || req.Peer != u.Chat || u.Date <= 0 || req.Date != u.Date || u.ActorUserID <= 0 || (u.Chat.Type != PeerTypeUser && u.Chat.Type != PeerTypeChannel) || u.Chat.ID <= 0 || req.ChatMember != nil || req.Callback != nil || req.PreCheckout != nil || req.Ephemeral != nil {
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
