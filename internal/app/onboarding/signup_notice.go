package onboarding

import (
	"context"
	"errors"
	"fmt"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// AccountSettings 读取账号的"联系人加入时通知"设置（生产里是 app/account.Service）。
type AccountSettings interface {
	GetAccountSettings(ctx context.Context, userID int64) (domain.AccountSettings, error)
}

// WithContactSignUpNotices 让入驻在会话里写 messageActionContactSignUp（"XX 加入了 …"），使默认
// 联系人出现在新账号的聊天列表里：每个非 bot 的默认联系人 C 在新账号那边写一条由 C 发出的；
// 双向联系人在 C 那边再写一条由新账号发出的，C 关了"联系人加入时通知"就静音。不设置时只加通讯录。
func (s *Service) WithContactSignUpNotices(notices store.ContactSignUpNoticeStore, settings AccountSettings) *Service {
	s.notices, s.accountSettings = notices, settings
	return s
}

// signUpNotice 在 owner 与 from 的会话里给 owner 写一条由 from 发出的服务消息。owner 或 from
// 在执行期间变得不可用（store 报 ErrUserNotFound）时不写，也不算失败。
func (s *Service) signUpNotice(ctx context.Context, owner, from int64, silent bool) error {
	if s.notices == nil {
		return nil
	}
	_, _, err := s.notices.DeliverContactSignUpNotice(ctx, domain.ContactSignUpNotice{
		OwnerUserID: owner,
		FromUserID:  from,
		Silent:      silent,
		Date:        int(s.now().Unix()),
	})
	if err != nil && !errors.Is(err, domain.ErrUserNotFound) {
		return fmt.Errorf("onboarding: sign-up notice from %d to %d: %w", from, owner, err)
	}
	return nil
}

// contactSignUpSilent 报告 userID 是否关了"联系人加入时通知"。
func (s *Service) contactSignUpSilent(ctx context.Context, userID int64) (bool, error) {
	if s.accountSettings == nil {
		return false, nil
	}
	settings, err := s.accountSettings.GetAccountSettings(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("onboarding: read account settings of %d: %w", userID, err)
	}
	return settings.ContactSignUpSilent, nil
}
