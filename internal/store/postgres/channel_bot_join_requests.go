package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// appendBotJoinRequestUpdatesTx 在写入新入群申请的同一事务里，给有邀请权限的管理员 bot 写
// updateBotChatInviteRequester 记录（#149）。invite 为零值（InviteID 0）表示公开群的入群申请。
func appendBotJoinRequestUpdatesTx(ctx context.Context, tx pgx.Tx, channel domain.Channel, invite domain.ChannelInvite, requesterUserID int64, date int) ([]domain.BotUpdate, error) {
	q := sqlcgen.New(tx)
	botIDs, err := q.ListChannelAdminBotIDs(ctx, channel.ID)
	if err != nil {
		return nil, fmt.Errorf("list channel admin bots: %w", err)
	}
	if len(botIDs) == 0 {
		return nil, nil
	}
	admins := make([]domain.ChannelMember, 0, len(botIDs))
	for _, botID := range botIDs {
		member, err := getChannelMemberByID(ctx, tx, channel.ID, botID)
		if err != nil {
			return nil, fmt.Errorf("load channel admin bot: %w", err)
		}
		admins = append(admins, member)
	}
	about, err := q.GetJoinRequesterAbout(ctx, requesterUserID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("load join requester about: %w", err)
	}
	if date == 0 {
		date = nowUnix()
	}
	return appendBotUpdatesTx(ctx, tx, domain.BotChatInviteRequesterUpdates(channel, invite, requesterUserID, about, admins, date))
}
