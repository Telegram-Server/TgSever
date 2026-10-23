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
	return appendBotOnlyUpdatesTx(ctx, tx, domain.WithChannelAdminBotAudience(changes, admins))
}
