package bots

import (
	"context"
	"net/url"
	"strings"

	"telesrv/internal/domain"
)

// normalizeBotMenuButton 校验并规整菜单按钮（全局与按用户共用同一套规则）。
func normalizeBotMenuButton(button domain.BotMenuButton) (domain.BotMenuButton, error) {
	switch button.Type {
	case domain.BotMenuButtonDefault, domain.BotMenuButtonCommands:
		button.Text, button.URL = "", ""
	case domain.BotMenuButtonWebView:
		button.Text = strings.TrimSpace(button.Text)
		button.URL = strings.TrimSpace(button.URL)
		if button.Text == "" || len(button.Text) > domain.MaxBotMenuButtonTextLen ||
			button.URL == "" || len(button.URL) > domain.MaxBotMenuButtonURLLen {
			return domain.BotMenuButton{}, domain.ErrBotMenuButtonInvalid
		}
		// 强制 https（对齐官方 BUTTON_URL_INVALID）：menu button URL 经
		// userFull.bot_info.menu_button 下发给客户端 webview 入口，
		// 拒绝 javascript:/file:/intent: 等非 https scheme，防 bot 投毒。
		if u, err := url.Parse(button.URL); err != nil || u.Scheme != "https" || u.Host == "" {
			return domain.BotMenuButton{}, domain.ErrBotMenuButtonInvalid
		}
	default:
		return domain.BotMenuButton{}, domain.ErrBotMenuButtonInvalid
	}
	return button, nil
}

// SetBotMenuButtonForUser 设置 bot 对单个用户的专属菜单按钮；default 即删除专属按钮，
// 该用户回落到全局默认。
//
// 与全局按钮不同：
//   - 不 bump bot_info_version（所有用户共享），也不动 bot 资料缓存（它只存全局默认）；
//     调用方负责失效该用户看机器人的投影并推 updateBotMenuButton；
//   - 不生成/改写 main mini app 与附件菜单项：那些是所有用户共享的，写进去就会把
//     一个用户的 URL 泄露给其他人。
func (s *Service) SetBotMenuButtonForUser(ctx context.Context, botUserID, userID int64, button domain.BotMenuButton) error {
	button, err := normalizeBotMenuButton(button)
	if err != nil {
		return err
	}
	if userID <= 0 || userID == botUserID {
		return domain.ErrUserNotFound
	}
	if s.users != nil {
		u, found, err := s.users.ByID(ctx, userID)
		if err != nil {
			return err
		}
		if !found || u.Deleted {
			return domain.ErrUserNotFound
		}
	}
	return s.bots.SetBotUserMenuButton(ctx, botUserID, userID, button)
}

// BotMenuButtonForUser 返回 userID 看到的菜单按钮：有专属按钮用专属的，否则用全局默认。
// 专属按钮不进 bot 资料缓存（那份缓存是所有查看者共享的）。
func (s *Service) BotMenuButtonForUser(ctx context.Context, botUserID, userID int64) (domain.BotMenuButton, error) {
	if userID > 0 && userID != botUserID && s.bots != nil {
		button, found, err := s.bots.GetBotUserMenuButton(ctx, botUserID, userID)
		if err != nil {
			return domain.BotMenuButton{}, err
		}
		if found {
			return button, nil
		}
	}
	return s.GetBotMenuButton(ctx, botUserID)
}
