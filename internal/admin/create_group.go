package admin

import (
	"context"
	"fmt"
	"strings"

	"telesrv/internal/domain"
)

// ActionCreateGroup 以指定账号身份建一个超级群（#168）。一个命令只建一个群：批量由后台
// 前端逐行发命令，每个群各有 command id、审计记录和按命令的幂等。
const ActionCreateGroup = "channel.create_group"

// 建群命令的稳定错误码，后台前端按它们显示每一行的失败原因。
const (
	CodeGroupOwnerNotFound = "GROUP_OWNER_NOT_FOUND"
	// CodeGroupOwnerInvalid：群主是机器人、系统账号或已删除账号，群会落到一个用不了它的账号名下。
	CodeGroupOwnerInvalid = "GROUP_OWNER_INVALID"
	CodeGroupOwnerFrozen  = "GROUP_OWNER_FROZEN"
	CodeGroupTitleInvalid = "GROUP_TITLE_INVALID"
)

// GroupCreator 以 ownerID 的身份建一个超级群，效果与该账号在客户端里自己建群相同（在线
// 客户端立刻看到新群）。由 RPC Router 实现：建群后的推送在 RPC 层，admin 服务不直接调频道
// 业务服务。实现绕过账号权限闸门，但要报告闸门本会拒绝与否。
type GroupCreator interface {
	AdminCreateGroup(ctx context.Context, ownerID int64, title string) (domain.AdminCreatedGroup, error)
}

type CreateGroupRequest struct {
	CommandMeta
	OwnerUserID int64  `json:"owner_user_id"`
	Title       string `json:"title"`
}

// CreateGroup 以 OwnerUserID 的身份建一个超级群。试跑只校验：群主存在，不是机器人、系统
// 账号，未删除、未冻结；去首尾空白后的群名符合 domain 的频道标题规则。校验放在命令通道
// 里面，被拒绝的试跑同样留下审计记录。正式执行经 GroupCreator 建群，details 记下新频道 id、
// 永久邀请链接和 account_permission_bypassed。命令的目标用户是群主。
func (s *Service) CreateGroup(ctx context.Context, req CreateGroupRequest) (CommandResult, error) {
	if s == nil || s.groupCreator == nil {
		return CommandResult{}, fmt.Errorf("admin group creator dependency is not configured")
	}
	if s.users == nil {
		return CommandResult{}, fmt.Errorf("admin user dependency is not configured")
	}
	if req.OwnerUserID <= 0 {
		return CommandResult{}, fmt.Errorf("owner_user_id is required")
	}
	req.Title = strings.TrimSpace(req.Title)
	return s.runCommand(ctx, req.CommandMeta, ActionCreateGroup, req.OwnerUserID, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{
			"owner_user_id": req.OwnerUserID,
			"title":         req.Title,
		}
		if err := s.validateGroupOwner(ctx, req.OwnerUserID); err != nil {
			return CommandResult{Details: details}, err
		}
		if !domain.ValidChannelTitle(req.Title) {
			return CommandResult{Details: details}, codedError(CodeGroupTitleInvalid, domain.ErrChannelTitleInvalid)
		}
		if req.DryRun {
			return CommandResult{Message: "group creation validated", Details: details}, nil
		}
		created, err := s.groupCreator.AdminCreateGroup(ctx, req.OwnerUserID, req.Title)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		details["channel_id"] = created.Channel.ID
		details["invite_link"] = created.InviteLink
		details["account_permission_bypassed"] = created.AccountPermissionBypassed
		return CommandResult{Message: "group created", Details: details}, nil
	})
}

// validateGroupOwner 拒绝不能拥有群的账号：不存在、机器人、系统账号、已删除、已冻结。
func (s *Service) validateGroupOwner(ctx context.Context, ownerID int64) error {
	owner, found, err := s.users.AdminUser(ctx, ownerID)
	if err != nil {
		return err
	}
	if !found {
		return codedError(CodeGroupOwnerNotFound, domain.ErrUserNotFound)
	}
	switch {
	case owner.Bot:
		return codedError(CodeGroupOwnerInvalid, fmt.Errorf("owner %d is a bot", ownerID))
	case domain.IsSystemUserID(owner.ID):
		return codedError(CodeGroupOwnerInvalid, fmt.Errorf("owner %d is a system account", ownerID))
	case owner.Deleted:
		return codedError(CodeGroupOwnerInvalid, fmt.Errorf("owner %d is deleted", ownerID))
	}
	freeze, frozen, err := s.AccountFreeze(ctx, ownerID)
	if err != nil {
		return err
	}
	if frozen && freeze.Frozen {
		return codedError(CodeGroupOwnerFrozen, domain.ErrUserFrozen)
	}
	return nil
}
