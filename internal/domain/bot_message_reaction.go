package domain

// BotMessageReactionUpdate 是 updateBotMessageReaction（#150）：某个用户在 bot 能看到的
// 消息上改了自己的反应。TDLib 转成 Bot API 的 message_reaction。
type BotMessageReactionUpdate struct {
	// Peer 是 bot 视角的会话：私聊为对方用户，超级群为频道。
	Peer Peer
	// Channel 是超级群的快照（私聊为零值），投影 chats 用。
	Channel   Channel `json:",omitempty"`
	MessageID int
	Actor     Peer
	Old       []MessageReaction `json:",omitempty"`
	New       []MessageReaction `json:",omitempty"`
}

// BotMessageReactionsUpdate 是 updateBotMessageReactions（#150）：匿名反应（广播频道）的
// 消息反应计数变化。TDLib 转成 Bot API 的 message_reaction_count。
type BotMessageReactionsUpdate struct {
	Peer      Peer
	Channel   Channel `json:",omitempty"`
	MessageID int
	// Reactions 是变化后的计数；不含任何查看者自己的 chosen 信息。
	Reactions []ChannelMessageReactionCount `json:",omitempty"`
}

// 接收规则（与 Telegram 官方 Bot API 一致，allowed_updates 由 telegram-bot-api 过滤，服务端不处理）：
//   - 私聊：对话里的 bot 收到对方用户在任一条消息上的反应变化；
//   - 超级群：只有身为创建者/管理员的 bot 收到（官方："The bot must be an administrator in the chat"）；
//     普通成员 bot（包括 privacy 模式下看不到消息的 bot）不收到；消息在 bot 的可见历史之前也不收到；
//   - 广播频道：反应匿名，管理员 bot 只收到反应计数 updateBotMessageReactions；
//   - bot 设的反应不送达（官方："The update isn't received for reactions set by bots"）；
//   - 反应前后相同（重复设置）不产生更新（TDLib 视 old == new 为非法更新并丢弃）。

// BotPrivateMessageReactionChange 生成私聊里对方用户改反应后发给 bot 的记录。botMessageID 是
// 消息在 bot 自己消息盒里的 id；actorIsBot 由调用方在同一事务里判定。
func BotPrivateMessageReactionChange(botUserID, actorUserID int64, actorIsBot bool, botMessageID int, old, current []MessageReaction, date int) (BotUpdate, bool) {
	if botUserID == 0 || actorUserID == 0 || actorIsBot || botUserID == actorUserID || botMessageID <= 0 || SameMessageReactions(old, current) {
		return BotUpdate{}, false
	}
	return BotUpdate{
		BotUserID: botUserID,
		Date:      date,
		MessageReaction: &BotMessageReactionUpdate{
			Peer:      Peer{Type: PeerTypeUser, ID: actorUserID},
			MessageID: botMessageID,
			Actor:     Peer{Type: PeerTypeUser, ID: actorUserID},
			Old:       cloneMessageReactions(old),
			New:       cloneMessageReactions(current),
		},
	}, true
}

// BotChannelMessageReactionChanges 生成超级群/广播频道里一次反应变化发给各个 bot 的记录。
// botMembers 是频道里 bot 账号的成员记录（调用方在同一事务里读出）；counts 是变化后的反应
// 计数，只用于广播频道。
func BotChannelMessageReactionChanges(channel Channel, messageID int, actorUserID int64, actorIsBot bool, old, current []MessageReaction, counts []ChannelMessageReactionCount, botMembers []ChannelMember, date int) []BotUpdate {
	if channel.ID == 0 || messageID <= 0 || actorUserID == 0 || SameMessageReactions(old, current) {
		return nil
	}
	anonymous := channel.Broadcast && !channel.Megagroup
	if actorIsBot && !anonymous {
		return nil
	}
	peer := Peer{Type: PeerTypeChannel, ID: channel.ID}
	out := make([]BotUpdate, 0, len(botMembers))
	for _, member := range botMembers {
		if !botReceivesChannelReactions(member, messageID) || member.UserID == actorUserID {
			continue
		}
		update := BotUpdate{BotUserID: member.UserID, Date: date}
		if anonymous {
			update.MessageReactions = &BotMessageReactionsUpdate{Peer: peer, Channel: channel, MessageID: messageID, Reactions: anonymousReactionCounts(counts)}
		} else {
			update.MessageReaction = &BotMessageReactionUpdate{
				Peer:      peer,
				Channel:   channel,
				MessageID: messageID,
				Actor:     Peer{Type: PeerTypeUser, ID: actorUserID},
				Old:       cloneMessageReactions(old),
				New:       cloneMessageReactions(current),
			}
		}
		out = append(out, update)
	}
	return out
}

func botReceivesChannelReactions(member ChannelMember, messageID int) bool {
	return IsChannelAdminBotAudience(member) && messageID > member.AvailableMinID
}

// SameMessageReactions 报告两组反应是否相同（按顺序比较，与 TDLib 的判定一致）。
func SameMessageReactions(a, b []MessageReaction) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Type != b[i].Type || a[i].Value() != b[i].Value() {
			return false
		}
	}
	return true
}

func anonymousReactionCounts(in []ChannelMessageReactionCount) []ChannelMessageReactionCount {
	out := make([]ChannelMessageReactionCount, 0, len(in))
	for _, item := range in {
		if item.Count <= 0 || !item.Reaction.Valid() {
			continue
		}
		out = append(out, ChannelMessageReactionCount{Reaction: item.Reaction, Count: item.Count})
	}
	return out
}

func cloneMessageReactions(in []MessageReaction) []MessageReaction {
	if len(in) == 0 {
		return nil
	}
	return append([]MessageReaction(nil), in...)
}
