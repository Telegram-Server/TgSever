package rpc

import (
	"context"

	"telesrv/internal/app/permissions"
	"telesrv/internal/domain"
)

// checkChannelPermission 判定 userID 对一个会话的加入/退出权限。会话由 load 懒加载：权限控制
// 不作用于该账号时不做任何会话查询，热路径上只多一次缓存的权限视图读取。load 返回 false 表示
// 解析不出会话，此时放行，由后面的业务调用返回原有的错误。
func (r *Router) checkChannelPermission(ctx context.Context, userID int64, kind permissions.ActionKind, load func() (domain.Channel, bool)) error {
	view, enforced, err := r.enforcedPermissionView(ctx, userID)
	if err != nil || !enforced {
		return err
	}
	channel, ok := load()
	if !ok {
		return nil
	}
	return decidePermission(view, permissions.Action{Kind: kind, Target: permissions.ChannelTarget(channel)})
}

// checkChatlistJoinPermission 按聊天文件夹里每个会话分别判定加入权限；任一会话被禁止时整个
// 请求被拒绝，不部分加入。已经加入的会话不算新的加入，跳过。
func (r *Router) checkChatlistJoinPermission(ctx context.Context, userID int64, peers []domain.DialogFolderPeer) error {
	view, enforced, err := r.enforcedPermissionView(ctx, userID)
	if err != nil || !enforced || r.deps.Channels == nil {
		return err
	}
	for _, item := range peers {
		if item.Peer.Type != domain.PeerTypeChannel || item.Peer.ID == 0 {
			continue
		}
		if current, err := r.deps.Channels.ResolveChannel(ctx, userID, item.Peer.ID); err == nil &&
			!current.Forbidden && current.Self.UserID == userID && current.Self.Status == domain.ChannelMemberActive {
			continue
		}
		channel, err := r.deps.Channels.GetJoinableChannel(ctx, userID, item.Peer.ID)
		if err != nil {
			continue
		}
		if err := decidePermission(view, permissions.Action{Kind: permissions.ActionJoin, Target: permissions.ChannelTarget(channel)}); err != nil {
			return err
		}
	}
	return nil
}

// enforcedPermissionView 读取权限视图并报告权限控制是否作用于 userID；读取失败时 fail closed。
func (r *Router) enforcedPermissionView(ctx context.Context, userID int64) (permissions.View, bool, error) {
	if r == nil || r.deps.Permissions == nil || userID == 0 {
		return permissions.View{}, false, nil
	}
	view, err := r.deps.Permissions.View(ctx, userID)
	if err != nil {
		return permissions.View{}, false, internalErr()
	}
	return view, view.Enforced(), nil
}

func decidePermission(view permissions.View, action permissions.Action) error {
	if denial := permissions.Decide(view, action); !denial.Allowed() {
		return permissionDeniedErr(denial)
	}
	return nil
}
