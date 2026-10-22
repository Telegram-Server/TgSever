package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/app/permissions"
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
