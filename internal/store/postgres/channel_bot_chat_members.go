package postgres

import (
	"context"
	"fmt"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// appendChannelBotUpdatesTx 在成员变动的事务里写 bot qts 记录：变动的成员本人（是 bot
// 时，#145）与群里每个管理员 bot（其他成员的变动，#146）。管理员 bot 读自同一事务里变动
// 之后的成员表。
func appendChannelBotUpdatesTx(ctx context.Context, tx sqlcgen.DBTX, channelID int64, changes []domain.BotUpdate) ([]domain.BotUpdate, error) {
	if len(changes) == 0 {
		return nil, nil
	}
	admins, err := channelAdminBotMembersTx(ctx, tx, channelID)
	if err != nil {
		return nil, err
	}
	return appendBotOnlyUpdatesTx(ctx, tx, domain.WithChannelAdminBotAudience(changes, admins))
}

// channelAdminBotMembersTx 在调用方的事务里读出群里的管理员 bot（在群的群主与管理员，
// domain.IsChannelAdminBotAudience）的完整成员记录，按 user id 排序。成员变动（#146）、入群
// 申请（#149）、消息反应（#150）的收件人都从这里取，各自的领域规则再细分。
func channelAdminBotMembersTx(ctx context.Context, tx sqlcgen.DBTX, channelID int64) ([]domain.ChannelMember, error) {
	ids, err := sqlcgen.New(tx).ListChannelAdminBotIDs(ctx, channelID)
	if err != nil {
		return nil, fmt.Errorf("list channel admin bots: %w", err)
	}
	admins := make([]domain.ChannelMember, 0, len(ids))
	for _, id := range ids {
		member, err := getChannelMemberByID(ctx, tx, channelID, id)
		if err != nil {
			return nil, fmt.Errorf("load channel admin bot %d: %w", id, err)
		}
		admins = append(admins, member)
	}
	return admins, nil
}
