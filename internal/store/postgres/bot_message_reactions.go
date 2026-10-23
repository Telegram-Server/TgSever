package postgres

import (
	"context"
	"fmt"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// 消息反应发给 bot（#150）：反应写入的事务里读出旧反应，写完后按 domain 的接收规则为能
// 收到的 bot 写 bot qts 记录，与反应一起提交或回滚。

// channelUserReactionsTx 返回 userID 在频道消息上当前的反应（按选择顺序）。
func channelUserReactionsTx(ctx context.Context, tx sqlcgen.DBTX, channelID int64, messageID int, userID int64) ([]domain.MessageReaction, error) {
	rows, err := sqlcgen.New(tx).ListUserChannelMessageReactions(ctx, sqlcgen.ListUserChannelMessageReactionsParams{ChannelID: channelID, MessageID: int32(messageID), ReactedUserID: userID})
	if err != nil {
		return nil, fmt.Errorf("list user channel reactions: %w", err)
	}
	return reactionsFromRows(rows, func(row sqlcgen.ListUserChannelMessageReactionsRow) (string, string) {
		return row.ReactionType, row.ReactionValue
	}), nil
}

// reactionsFromRows 把 (reaction_type, reaction_value) 行转成反应，跳过无法识别的值。
func reactionsFromRows[Row any](rows []Row, value func(Row) (string, string)) []domain.MessageReaction {
	out := make([]domain.MessageReaction, 0, len(rows))
	for _, row := range rows {
		kind, v := value(row)
		if reaction, ok := domain.MessageReactionFromValue(domain.MessageReactionType(kind), v); ok {
			out = append(out, reaction)
		}
	}
	return out
}

// appendChannelReactionBotUpdatesTx 在反应写入之后、提交之前为管理员 bot 写 qts 记录。
func appendChannelReactionBotUpdatesTx(ctx context.Context, tx sqlcgen.DBTX, channel domain.Channel, req domain.SetChannelMessageReactionsRequest, old []domain.MessageReaction) ([]domain.BotUpdate, error) {
	admins, err := channelAdminBotMembersTx(ctx, tx, channel.ID)
	if err != nil {
		return nil, err
	}
	if len(admins) == 0 {
		return nil, nil
	}
	current, err := channelUserReactionsTx(ctx, tx, channel.ID, req.MessageID, req.UserID)
	if err != nil {
		return nil, err
	}
	if domain.SameMessageReactions(old, current) {
		return nil, nil
	}
	q := sqlcgen.New(tx)
	actorBot, err := q.FilterBotUserIDs(ctx, []int64{req.UserID})
	if err != nil {
		return nil, fmt.Errorf("filter reaction actor bot: %w", err)
	}
	var counts []domain.ChannelMessageReactionCount
	if channel.Broadcast && !channel.Megagroup {
		rows, err := q.CountChannelMessageReactions(ctx, sqlcgen.CountChannelMessageReactionsParams{ChannelID: channel.ID, MessageID: int32(req.MessageID)})
		if err != nil {
			return nil, fmt.Errorf("count channel reactions: %w", err)
		}
		for _, row := range rows {
			if reaction, ok := domain.MessageReactionFromValue(domain.MessageReactionType(row.ReactionType), row.ReactionValue); ok {
				counts = append(counts, domain.ChannelMessageReactionCount{Reaction: reaction, Count: int(row.ReactionCount)})
			}
		}
	}
	updates := domain.BotChannelMessageReactionChanges(channel, req.MessageID, req.UserID, len(actorBot) > 0, old, current, counts, admins, req.Date)
	return appendBotUpdatesTx(ctx, tx, updates)
}

// privateUserReactionsTx 返回 userID 在私聊消息上当前的反应（按选择顺序）。
func privateUserReactionsTx(ctx context.Context, tx sqlcgen.DBTX, messageSenderID, privateMessageID, userID int64) ([]domain.MessageReaction, error) {
	rows, err := sqlcgen.New(tx).ListUserPrivateMessageReactions(ctx, sqlcgen.ListUserPrivateMessageReactionsParams{MessageSenderID: messageSenderID, PrivateMessageID: privateMessageID, UserID: userID})
	if err != nil {
		return nil, fmt.Errorf("list user private reactions: %w", err)
	}
	return reactionsFromRows(rows, func(row sqlcgen.ListUserPrivateMessageReactionsRow) (string, string) {
		return row.ReactionType, row.ReactionValue
	}), nil
}

// appendPrivateReactionBotUpdatesTx 在私聊反应写入之后、提交之前：对话另一方是 bot 时为它写
// qts 记录。boxes 是本次事务读出的双方消息副本，从中取 bot 自己的 box id。
func appendPrivateReactionBotUpdatesTx(ctx context.Context, tx sqlcgen.DBTX, req domain.SetPrivateMessageReactionsRequest, messageSenderID, privateMessageID int64, old []domain.MessageReaction, boxes []domain.Message) ([]domain.BotUpdate, error) {
	q := sqlcgen.New(tx)
	bots, err := q.FilterBotUserIDs(ctx, []int64{req.UserID, req.Peer.ID})
	if err != nil {
		return nil, fmt.Errorf("filter private reaction bots: %w", err)
	}
	peerIsBot, actorIsBot := false, false
	for _, id := range bots {
		peerIsBot = peerIsBot || id == req.Peer.ID
		actorIsBot = actorIsBot || id == req.UserID
	}
	if !peerIsBot || actorIsBot {
		return nil, nil
	}
	botMessageID := 0
	for _, msg := range boxes {
		if msg.OwnerUserID == req.Peer.ID {
			botMessageID = msg.ID
		}
	}
	current, err := privateUserReactionsTx(ctx, tx, messageSenderID, privateMessageID, req.UserID)
	if err != nil {
		return nil, err
	}
	update, ok := domain.BotPrivateMessageReactionChange(req.Peer.ID, req.UserID, actorIsBot, botMessageID, old, current, req.Date)
	if !ok {
		return nil, nil
	}
	return appendBotUpdatesTx(ctx, tx, []domain.BotUpdate{update})
}
