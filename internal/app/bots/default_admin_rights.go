package bots

import (
	"context"

	"telesrv/internal/domain"
)

// SetDefaultAdminRights 写入 bot 被加为群（forChannels=false）或频道（forChannels=true）
// 管理员时建议的默认权限；零值即清空。
//
// 与当前值相同返回 domain.ErrBotAdminRightsNotModified（MTProto 的 RIGHTS_NOT_MODIFIED）；
// botUserID 不是 bot 返回 domain.ErrBotNotFound。比较直接读 store 而不是资料缓存，
// 避免另一实例刚写过、本进程缓存尚未失效时误判为未变。写入 bump bot_info_version
// 并失效本进程的资料缓存，查看者下一次 getFullUser 即看到新值。
func (s *Service) SetDefaultAdminRights(ctx context.Context, botUserID int64, forChannels bool, rights domain.ChannelAdminRights) (int, error) {
	if s == nil || s.bots == nil || botUserID == 0 {
		return 0, domain.ErrBotNotFound
	}
	current, found, err := s.bots.GetBot(ctx, botUserID)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, domain.ErrBotNotFound
	}
	old := current.GroupAdminRights
	if forChannels {
		old = current.BroadcastAdminRights
	}
	if old == rights {
		return 0, domain.ErrBotAdminRightsNotModified
	}
	version, err := s.bots.SetBotDefaultAdminRights(ctx, botUserID, forChannels, rights)
	if err != nil {
		return 0, err
	}
	s.invalidateBotReadCaches(ctx, botUserID)
	return version, nil
}
