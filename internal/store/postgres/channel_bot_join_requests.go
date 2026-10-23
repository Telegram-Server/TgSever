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
	admins, err := channelAdminBotMembersTx(ctx, tx, channel.ID)
	if err != nil {
		return nil, err
	}
	if len(admins) == 0 {
		return nil, nil
	}
	about, err := sqlcgen.New(tx).GetJoinRequesterAbout(ctx, requesterUserID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("load join requester about: %w", err)
	}
	if date == 0 {
		date = nowUnix()
	}
	return appendBotUpdatesTx(ctx, tx, domain.BotChatInviteRequesterUpdates(channel, invite, requesterUserID, about, admins, date))
}
