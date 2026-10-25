package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/app/permissions"
	"telesrv/internal/domain"
)

// filterChatInvitePrivacy evaluates the whole target vector through the
// privacy read model before any membership write. The returned slices preserve
// request order.
//
// Bots are not subject to the chat_invite rule: like Telegram, whether a bot
// may be added is decided only by its bot_nochats setting (/setjoingroups),
// enforced by the membership write (BOT_GROUPS_BLOCKED). Evaluating the rule
// would test the inviter against the bot's own contact list, which a bot never
// has, so the contacts-only default made every bot uninvitable. A failed bot
// lookup aborts the invite before any membership write instead of silently
// evaluating the bot as a human target.
//
// target 是被拉入会话的种类（permissions.ChannelTarget）：被员工权限控制禁止加入该种类会话的
// 被邀请人（#118 被拉入）和"隐私设置不允许拉群"一样进入 missing_invitees，其他人照常加入。
func (r *Router) filterChatInvitePrivacy(ctx context.Context, inviterUserID int64, targetUserIDs []int64, target permissions.Target) ([]int64, []tg.MissingInvitee, error) {
	if len(targetUserIDs) == 0 {
		return nil, nil, nil
	}
	visible, err := r.chatInvitePrivacyVisible(ctx, inviterUserID, targetUserIDs)
	if err != nil {
		return nil, nil, err
	}
	allowed := make([]int64, 0, len(targetUserIDs))
	missing := make([]tg.MissingInvitee, 0)
	for _, targetUserID := range targetUserIDs {
		if visible[targetUserID] {
			restricted, err := r.joinPermissionRestricted(ctx, targetUserID, target)
			if err != nil {
				return nil, nil, err
			}
			if !restricted {
				allowed = append(allowed, targetUserID)
				continue
			}
		}
		missing = append(missing, tg.MissingInvitee{UserID: targetUserID})
	}
	return allowed, missing, nil
}

// joinPermissionRestricted 报告 userID 自己的权限是否禁止加入 target 种类的会话；用于别人把他
// 拉入或批准他的入群申请。读取失败时报错（fail closed）。
func (r *Router) joinPermissionRestricted(ctx context.Context, userID int64, target permissions.Target) (bool, error) {
	view, enforced, err := r.enforcedPermissionView(ctx, userID)
	if err != nil || !enforced {
		return false, err
	}
	return !permissions.Decide(view, permissions.Action{Kind: permissions.ActionJoin, Target: target}).Allowed(), nil
}

func (r *Router) chatInvitePrivacyVisible(ctx context.Context, inviterUserID int64, targetUserIDs []int64) (map[int64]bool, error) {
	visible := make(map[int64]bool, len(targetUserIDs))
	if r.deps.Privacy == nil {
		for _, targetUserID := range targetUserIDs {
			visible[targetUserID] = true
		}
		return visible, nil
	}
	humans := make([]int64, 0, len(targetUserIDs))
	for _, targetUserID := range targetUserIDs {
		bot, _, err := r.lookupUserBotStatus(ctx, targetUserID)
		if err != nil {
			return nil, err
		}
		if bot {
			visible[targetUserID] = true
			continue
		}
		humans = append(humans, targetUserID)
	}
	if len(humans) == 0 {
		// only bots: nothing to evaluate
	} else if batch, ok := r.deps.Privacy.(batchPrivacyEvaluator); ok {
		matrix, err := batch.CanSeeBatch(ctx, humans, inviterUserID, []domain.PrivacyKey{domain.PrivacyKeyChatInvite})
		if err != nil {
			return nil, err
		}
		for _, targetUserID := range humans {
			visible[targetUserID] = matrix[targetUserID][domain.PrivacyKeyChatInvite]
		}
	} else {
		for _, targetUserID := range humans {
			allowed, err := r.deps.Privacy.CanSee(ctx, targetUserID, inviterUserID, domain.PrivacyKeyChatInvite)
			if err != nil {
				return nil, err
			}
			visible[targetUserID] = allowed
		}
	}
	return visible, nil
}

func emptyInvitedUsersUpdates(date int) *tg.Updates {
	return &tg.Updates{
		Updates: []tg.UpdateClass{},
		Users:   []tg.UserClass{},
		Chats:   []tg.ChatClass{},
		Date:    date,
		Seq:     0,
	}
}
