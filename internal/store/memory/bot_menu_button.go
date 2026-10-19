package memory

import (
	"context"

	"telesrv/internal/domain"
)

// SetBotUserMenuButton 写入/删除 bot 对单个用户的专属菜单按钮（default = 删除）。
// 不 bump bot_info_version。
func (s *BotStore) SetBotUserMenuButton(_ context.Context, botUserID, userID int64, button domain.BotMenuButton) error {
	key := [2]int64{botUserID, userID}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[botUserID]; !ok {
		return domain.ErrBotNotFound
	}
	if button.Type == domain.BotMenuButtonDefault {
		delete(s.userMenus, key)
		return nil
	}
	s.userMenus[key] = button
	return nil
}

// GetBotUserMenuButton 返回该用户的专属菜单按钮；未设置时 found=false。
func (s *BotStore) GetBotUserMenuButton(_ context.Context, botUserID, userID int64) (domain.BotMenuButton, bool, error) {
	s.mu.RLock()
	button, ok := s.userMenus[[2]int64{botUserID, userID}]
	s.mu.RUnlock()
	return button, ok, nil
}
