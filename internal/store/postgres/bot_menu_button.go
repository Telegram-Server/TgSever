package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// SetBotUserMenuButton 写入 bot 对单个用户的专属菜单按钮；default 类型即删除该行
// （回落到 bots 表的全局默认）。不 bump bot_info_version：跨实例缓存失效由表上的
// trigger 发 (user, bot) 维度的 dialog_light 事件完成（迁移 20261021020000）。
func (s *BotStore) SetBotUserMenuButton(ctx context.Context, botUserID, userID int64, button domain.BotMenuButton) error {
	if button.Type == domain.BotMenuButtonDefault {
		if err := s.q.DeleteBotUserMenuButton(ctx, sqlcgen.DeleteBotUserMenuButtonParams{BotUserID: botUserID, UserID: userID}); err != nil {
			return fmt.Errorf("delete bot user menu button: %w", err)
		}
		return nil
	}
	if err := s.q.UpsertBotUserMenuButton(ctx, sqlcgen.UpsertBotUserMenuButtonParams{
		BotUserID:      botUserID,
		UserID:         userID,
		MenuButtonType: int16(button.Type),
		MenuButtonText: button.Text,
		MenuButtonUrl:  button.URL,
	}); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.ForeignKeyViolation {
			if pgErr.ConstraintName == "bot_user_menu_buttons_user_id_fkey" {
				return domain.ErrUserNotFound
			}
			return domain.ErrBotNotFound
		}
		return fmt.Errorf("upsert bot user menu button: %w", err)
	}
	return nil
}

// GetBotUserMenuButton 返回该用户的专属菜单按钮；未设置时 found=false。
func (s *BotStore) GetBotUserMenuButton(ctx context.Context, botUserID, userID int64) (domain.BotMenuButton, bool, error) {
	if botUserID == 0 || userID == 0 {
		return domain.BotMenuButton{}, false, nil
	}
	row, err := s.q.GetBotUserMenuButton(ctx, sqlcgen.GetBotUserMenuButtonParams{BotUserID: botUserID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.BotMenuButton{}, false, nil
		}
		return domain.BotMenuButton{}, false, fmt.Errorf("get bot user menu button: %w", err)
	}
	return domain.BotMenuButton{
		Type: domain.BotMenuButtonType(row.MenuButtonType),
		Text: row.MenuButtonText,
		URL:  row.MenuButtonUrl,
	}, true, nil
}
