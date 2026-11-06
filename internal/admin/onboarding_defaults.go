package admin

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"telesrv/internal/domain"
)

// 后台设置新账号入驻模板：默认联系人与自动加入的群和频道。走普通的命令通道：试运行、按 command
// id 幂等与冲突检测、写审计。提交的两份名单整体替换当前模板（不给就是空）。模板在进入命令通道
// 之前校验（见 onboarding.Service.ValidateDefaults），不合法时以 ONBOARDING_DEFAULTS_INVALID 拒绝、
// 不留命令记录。修改模板只影响之后新建的账号。

const ActionSetOnboardingDefaults = "onboarding_defaults.set"

// OnboardingDefaultsService 是入驻模板的业务模块（生产里是 app/onboarding.Service）。
type OnboardingDefaultsService interface {
	Defaults(ctx context.Context) (domain.OnboardingDefaults, bool, error)
	ValidateDefaults(ctx context.Context, d domain.OnboardingDefaults) error
	SetDefaults(ctx context.Context, d domain.OnboardingDefaults, commandID string) (prev domain.OnboardingDefaults, prevFound bool, written domain.OnboardingDefaults, err error)
}

// OnboardingContactSetting 是模板里的一个默认联系人；mutual=true 时双向添加。
type OnboardingContactSetting struct {
	UserID int64 `json:"user_id"`
	Mutual bool  `json:"mutual"`
}

// SetOnboardingDefaultsRequest 是 onboarding_defaults.set 的参数。
type SetOnboardingDefaultsRequest struct {
	CommandMeta
	Contacts   []OnboardingContactSetting `json:"contacts"`
	ChannelIDs []int64                    `json:"channel_ids"`
}

func (req SetOnboardingDefaultsRequest) defaults() domain.OnboardingDefaults {
	d := domain.OnboardingDefaults{ChannelIDs: req.ChannelIDs}
	for _, c := range req.Contacts {
		d.Contacts = append(d.Contacts, domain.OnboardingContact{UserID: c.UserID, Mutual: c.Mutual})
	}
	return d.Clone()
}

// OnboardingDefaultsResult 是入驻模板；Found=false 表示还没有设置过（新账号不入驻）。
type OnboardingDefaultsResult struct {
	Found      bool                       `json:"found"`
	Contacts   []OnboardingContactSetting `json:"contacts"`
	ChannelIDs []int64                    `json:"channel_ids"`
	UpdatedAt  string                     `json:"updated_at,omitempty"`
	CommandID  string                     `json:"command_id,omitempty"`
}

func onboardingDefaultsResult(d domain.OnboardingDefaults, found bool) OnboardingDefaultsResult {
	d = d.Clone()
	contacts := make([]OnboardingContactSetting, 0, len(d.Contacts))
	for _, c := range d.Contacts {
		contacts = append(contacts, OnboardingContactSetting{UserID: c.UserID, Mutual: c.Mutual})
	}
	return OnboardingDefaultsResult{
		Found:      found,
		Contacts:   contacts,
		ChannelIDs: d.ChannelIDs,
		UpdatedAt:  formatUpdatedAt(d.UpdatedAt),
		CommandID:  d.CommandID,
	}
}

// OnboardingDefaults 读取入驻模板；没有时返回 found=false 与空名单。
func (s *Service) OnboardingDefaults(ctx context.Context) (OnboardingDefaultsResult, error) {
	if s == nil || s.onboardingDefaults == nil {
		return OnboardingDefaultsResult{}, fmt.Errorf("admin onboarding defaults dependency is not configured")
	}
	d, found, err := s.onboardingDefaults.Defaults(ctx)
	if err != nil {
		return OnboardingDefaultsResult{}, err
	}
	return onboardingDefaultsResult(d, found), nil
}

// SetOnboardingDefaults 处理 onboarding_defaults.set。已有记录的 command id 先按记录重放或报
// COMMAND_ID_CONFLICT；新提交在进入命令通道前校验，不合法时不留命令记录。
func (s *Service) SetOnboardingDefaults(ctx context.Context, req SetOnboardingDefaultsRequest) (CommandResult, error) {
	if s == nil || s.onboardingDefaults == nil {
		return CommandResult{}, fmt.Errorf("admin onboarding defaults dependency is not configured")
	}
	if result, replayed, err := s.existingCommand(ctx, req.CommandMeta, ActionSetOnboardingDefaults, req); replayed || err != nil {
		return result, err
	}
	next := req.defaults()
	if err := s.onboardingDefaults.ValidateDefaults(ctx, next); err != nil {
		return CommandResult{}, onboardingDefaultsError(err)
	}
	prev, prevFound, err := s.onboardingDefaults.Defaults(ctx)
	if err != nil {
		return CommandResult{}, err
	}
	return s.runCommand(ctx, req.CommandMeta, ActionSetOnboardingDefaults, 0, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{
			"previous_defaults": onboardingDefaultsResult(prev, prevFound),
			"defaults":          onboardingDefaultsResult(next, true),
			"would_change":      !prevFound || !sameOnboardingDefaults(prev, next),
		}
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		prev, prevFound, written, err := s.onboardingDefaults.SetDefaults(ctx, next, req.CommandID)
		if err != nil {
			return CommandResult{Details: details}, onboardingDefaultsError(err)
		}
		details["previous_defaults"] = onboardingDefaultsResult(prev, prevFound)
		details["defaults"] = onboardingDefaultsResult(written, true)
		details["would_change"] = !prevFound || !sameOnboardingDefaults(prev, written)
		return CommandResult{Message: "onboarding defaults updated", Details: details}, nil
	})
}

func onboardingDefaultsError(err error) error {
	if errors.Is(err, domain.ErrOnboardingDefaultsInvalid) {
		return codedError("ONBOARDING_DEFAULTS_INVALID", err)
	}
	return err
}

func sameOnboardingDefaults(a, b domain.OnboardingDefaults) bool {
	a, b = a.Clone(), b.Clone()
	return reflect.DeepEqual(a.Contacts, b.Contacts) && reflect.DeepEqual(a.ChannelIDs, b.ChannelIDs)
}
