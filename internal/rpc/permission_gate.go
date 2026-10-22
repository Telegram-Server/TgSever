package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/app/permissions"
	"telesrv/internal/domain"
)

// AccountPermissionsService 提供员工权限控制（#118）的权限视图。生产接线是
// app/permissions.Service：视图在进程内缓存，跨实例经读模型通知失效。
type AccountPermissionsService interface {
	View(ctx context.Context, userID int64) (permissions.View, error)
	InvalidateAccountPermissions(userID int64)
}

// permissionMethodActions 是 Router 方法闸门：不需要目标信息的动作按语义方法名登记在这里，
// 在冻结检查（checkFrozenRPC）旁边、handler 之前统一拦截。后续票把删除、清空对话、添加
// 联系人等方法加进来（例如 "messages.deleteMessages": permissions.ActionDelete），并在
// permissions.Decide 里加对应的 case。需要目标信息（会话种类、私聊对象）的动作不登记在这里，
// 而是在 handler 解析出目标之后调用 Router.checkPermission。
var permissionMethodActions = map[string]permissions.ActionKind{
	"messages.editMessage": permissions.ActionEdit,
	// 删除消息（#120）。setHistoryTTL / setDefaultHistoryTTL 只拦开启或修改计时器，需要看请求
	// 里的 period，在 handler 里判定。
	"messages.deleteMessages":           permissions.ActionDelete,
	"channels.deleteMessages":           permissions.ActionDelete,
	"channels.deleteParticipantHistory": permissions.ActionDelete,
	// 清空对话（#120）。
	"messages.deleteHistory":      permissions.ActionClearHistory,
	"channels.deleteHistory":      permissions.ActionClearHistory,
	"messages.deleteSavedHistory": permissions.ActionClearHistory,
	// sendScreenshotNotification 尚未实现，先按方法名登记：没有目标信息时按"非收藏夹"判定。
	// 其他发送方法需要目标（收藏夹放行），在 handler 里调用 checkSendPermission。
	"messages.sendScreenshotNotification": permissions.ActionSend,
	// messages.createChat 总是创建群组（没有普通群，落成 megagroup）；channels.createChannel
	// 按 megagroup/broadcast 区分，在 handler 里判定（checkCreateChannelPermission）。
	"messages.createChat": permissions.ActionCreateGroup,
	// 添加联系人的全部入口；删除、读取联系人不登记。contacts.importContactToken 尚未实现，
	// 按方法名先登记，实现后自动受控。
	"contacts.addContact":         permissions.ActionAddContact,
	"contacts.importContacts":     permissions.ActionAddContact,
	"contacts.acceptContact":      permissions.ActionAddContact,
	"contacts.importContactToken": permissions.ActionAddContact,
}

// createChannelPermissionAction 按 channels.createChannel 的 flag 给出动作：与 store 的落库语义
// 一致——没有 megagroup 或带了 broadcast 都会落成广播频道（两个 flag 都没给时按 broadcast 创建），
// 只有 megagroup 且不带 broadcast（含论坛）才是群组。
func createChannelPermissionAction(req *tg.ChannelsCreateChannelRequest) permissions.Action {
	if req.Megagroup && !req.Broadcast {
		return permissions.Action{Kind: permissions.ActionCreateGroup}
	}
	return permissions.Action{Kind: permissions.ActionCreateChannel}
}

// checkPermissionRPC 是按方法名的权限闸门；未登录、未登记的方法与未接线的 Router 直接放行。
func (r *Router) checkPermissionRPC(ctx context.Context, method string) error {
	if r == nil || r.deps.Permissions == nil {
		return nil
	}
	kind, gated := permissionMethodActions[method]
	if !gated {
		return nil
	}
	userID, authorized := UserIDFrom(ctx)
	if !authorized || userID == 0 {
		return nil
	}
	return r.checkPermission(ctx, userID, permissions.Action{Kind: kind})
}

// checkPermission 读取 userID 的权限视图并判定 action；被拒绝时返回 403 TELESRV_PERMISSION_*。
// 读取失败时拒绝（fail closed），与冻结闸门一致。
func (r *Router) checkPermission(ctx context.Context, userID int64, action permissions.Action) error {
	if r == nil || r.deps.Permissions == nil || userID == 0 {
		return nil
	}
	view, err := r.deps.Permissions.View(ctx, userID)
	if err != nil {
		return internalErr()
	}
	if denial := permissions.Decide(view, action); !denial.Allowed() {
		return permissionDeniedErr(denial)
	}
	return nil
}

// checkSendPermission 判定 userID 向 peer 发送消息：和自己的对话（收藏夹）放行，私聊带上对方
// user id 供白名单判定。频道 peer 不再解析群组/频道种类（发送判定不区分两者），按 TargetGroup
// 处理以免热路径多一次查询。
func (r *Router) checkSendPermission(ctx context.Context, userID int64, peer domain.Peer) error {
	return r.checkPermission(ctx, userID, permissions.Action{Kind: permissions.ActionSend, Target: sendPermissionTarget(userID, peer)})
}

func sendPermissionTarget(userID int64, peer domain.Peer) permissions.Target {
	if peer.Type == domain.PeerTypeUser {
		if peer.ID == userID {
			return permissions.Target{Kind: permissions.TargetSelf}
		}
		return permissions.Target{Kind: permissions.TargetPrivate, UserID: peer.ID}
	}
	return permissions.Target{Kind: permissions.TargetGroup}
}

// checkSecretChatSendPermission 判定 userID 在秘密聊天 chatID 里建链或发送。对方要查一次
// 秘密聊天，只在权限控制作用于这个账号时才查；查不到时对方留空（照样按发送权限判定），
// 由后续的 store 调用返回原本的错误。
func (r *Router) checkSecretChatSendPermission(ctx context.Context, userID int64, chatID int) error {
	if r == nil || r.deps.Permissions == nil || userID == 0 {
		return nil
	}
	view, err := r.deps.Permissions.View(ctx, userID)
	if err != nil {
		return internalErr()
	}
	if !view.Enforced() {
		return nil
	}
	target := permissions.Target{Kind: permissions.TargetSecretChat}
	if r.deps.SecretChats != nil {
		chat, found, err := r.deps.SecretChats.GetSecretChat(ctx, chatID)
		if err != nil {
			return internalErr()
		}
		if found && chat.HasParticipant(userID) {
			target.UserID = chat.PeerOf(userID)
		}
	}
	if denial := permissions.Decide(view, permissions.Action{Kind: permissions.ActionSend, Target: target}); !denial.Allowed() {
		return permissionDeniedErr(denial)
	}
	return nil
}

// checkSecretChatCounterpartPermission 判定 userID 与已知对方 counterpartID 建链或在秘密聊天里发送。
func (r *Router) checkSecretChatCounterpartPermission(ctx context.Context, userID, counterpartID int64) error {
	return r.checkPermission(ctx, userID, permissions.Action{
		Kind:   permissions.ActionSend,
		Target: permissions.Target{Kind: permissions.TargetSecretChat, UserID: counterpartID},
	})
}

// checkHistoryTTLPermission 判定开启或修改自动删除计时器（算作删除消息）；关闭计时器放行。
func (r *Router) checkHistoryTTLPermission(ctx context.Context, userID int64, period int) error {
	if period == 0 {
		return nil
	}
	return r.checkPermission(ctx, userID, permissions.Action{Kind: permissions.ActionDelete})
}

func permissionDeniedErr(denial permissions.Denial) error {
	return tgerr.New(403, string(denial))
}

// NotifyAccountPermissionsChanged 是管理端在权限设置写入后（包括关闭总开关）调用的通知：
// 立刻失效本实例的权限视图，并向该账号在本实例上的全部在线会话推送 updateConfig，客户端据此
// 重新拉取 appConfig。其他实例的缓存由写入时提交的读模型通知失效。
func (r *Router) NotifyAccountPermissionsChanged(ctx context.Context, userID int64) error {
	if r == nil || userID == 0 {
		return nil
	}
	if r.deps.Permissions != nil {
		r.deps.Permissions.InvalidateAccountPermissions(userID)
	}
	r.pushUserUpdates(ctx, userID, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateConfig{}},
		Date:    int(r.clock.Now().Unix()),
	})
	return nil
}
