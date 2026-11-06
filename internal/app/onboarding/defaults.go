package onboarding

import (
	"context"
	"fmt"

	"telesrv/internal/domain"
)

// Defaults 读取入驻模板；found=false 表示还没有设置过。
func (s *Service) Defaults(ctx context.Context) (domain.OnboardingDefaults, bool, error) {
	return s.store.GetOnboardingDefaults(ctx)
}

// ValidateDefaults 校验一份要写入的模板：两份名单不超过上限、没有重复项；默认联系人存在且未
// 注销，bot 只能单向添加（bot 没有通讯录）；群和频道存在且没有删除。不合法时返回包着
// domain.ErrOnboardingDefaultsInvalid 的错误，说明是哪一项。只读。
func (s *Service) ValidateDefaults(ctx context.Context, d domain.OnboardingDefaults) error {
	if len(d.Contacts) > domain.MaxOnboardingContacts {
		return invalidDefaults("at most %d default contacts", domain.MaxOnboardingContacts)
	}
	if len(d.ChannelIDs) > domain.MaxOnboardingChannels {
		return invalidDefaults("at most %d default channels", domain.MaxOnboardingChannels)
	}
	seenUsers := make(map[int64]struct{}, len(d.Contacts))
	for _, c := range d.Contacts {
		if _, dup := seenUsers[c.UserID]; dup {
			return invalidDefaults("contact %d is listed twice", c.UserID)
		}
		seenUsers[c.UserID] = struct{}{}
		if c.UserID == 0 {
			return invalidDefaults("contact user id is empty")
		}
		u, found, err := s.users.ByID(ctx, c.UserID)
		if err != nil {
			return err
		}
		if !found || u.Deleted {
			return invalidDefaults("contact %d does not exist or is deleted", c.UserID)
		}
		if c.Mutual && u.Bot {
			return invalidDefaults("contact %d is a bot and cannot be added both ways", c.UserID)
		}
	}
	seenChannels := make(map[int64]struct{}, len(d.ChannelIDs))
	for _, id := range d.ChannelIDs {
		if _, dup := seenChannels[id]; dup {
			return invalidDefaults("channel %d is listed twice", id)
		}
		seenChannels[id] = struct{}{}
		if _, err := s.channels.GetChannelByID(ctx, id); err != nil {
			if _, skip := channelSkipReason(err); skip {
				return invalidDefaults("channel %d does not exist", id)
			}
			return err
		}
	}
	return nil
}

// SetDefaults 校验后整体替换模板，返回替换前（没有时 prevFound=false）与写入的值。只影响之后
// 新建的账号。
func (s *Service) SetDefaults(ctx context.Context, d domain.OnboardingDefaults, commandID string) (prev domain.OnboardingDefaults, prevFound bool, written domain.OnboardingDefaults, err error) {
	if err := s.ValidateDefaults(ctx, d); err != nil {
		return domain.OnboardingDefaults{}, false, domain.OnboardingDefaults{}, err
	}
	next := d.Clone()
	next.CommandID = commandID
	return s.store.SetOnboardingDefaults(ctx, next)
}

func invalidDefaults(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{domain.ErrOnboardingDefaultsInvalid}, args...)...)
}
