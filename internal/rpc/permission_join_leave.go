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

// checkChatlistLeavePermission 按聊天文件夹里每个会话分别判定退出权限：退出文件夹会顺带退出其中
// 的会话，所以任一被禁止退出的会话都让整个请求被拒绝，不部分退出。账号不在其中的会话不算退出，跳过。
func (r *Router) checkChatlistLeavePermission(ctx context.Context, userID int64, peers []domain.DialogFolderPeer) error {
	view, enforced, err := r.enforcedPermissionView(ctx, userID)
	if err != nil || !enforced || r.deps.Channels == nil {
		return err
	}
	for _, item := range peers {
		if item.Peer.Type != domain.PeerTypeChannel || item.Peer.ID == 0 {
			continue
		}
		current, err := r.deps.Channels.ResolveChannel(ctx, userID, item.Peer.ID)
		if err != nil || current.Forbidden || current.Self.UserID != userID || current.Self.Status != domain.ChannelMemberActive {
			continue
		}
		if err := decidePermission(view, permissions.Action{Kind: permissions.ActionLeave, Target: permissions.ChannelTarget(current.Channel)}); err != nil {
			return err
		}
	}
	return nil
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
