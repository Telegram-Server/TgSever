package rpc

import (
	"context"

	"telesrv/internal/domain"
)

// 频道导入（app/channelimport）的实时下发钩子。导入经 admin API 进来，没有调用会话；落库由
// 导入服务完成，这里只把已经提交的变化推给在线成员。全部是尽力而为：持久的频道事件和 pts
// 才是 getChannelDifference 补拉的依据。

// NotifyChannelImportCreated 把导入任务新建的频道告知频道主的全部在线会话，效果与频道主在
// 客户端里 channels.createChannel 之后收到的更新相同。
func (r *Router) NotifyChannelImportCreated(ctx context.Context, res domain.CreateChannelResult) {
	if r == nil || res.Channel.ID == 0 {
		return
	}
	r.addOnlineChannelMemberships(res.Channel.ID, channelMemberUserIDs(res.Members)...)
	r.announceCreatedChannel(ctx, res.Channel.CreatorUserID, res)
}

// NotifyChannelImportEvents 下发镜像阶段的新帖与编辑。每个事件走与普通发送、编辑相同的
// fan-out（在线成员收到完整更新，超出上限的成员收到带 pts 的 updateChannelTooLong），新帖
// 同样触发离线推送。回填阶段没有事件，不会走到这里。
func (r *Router) NotifyChannelImportEvents(ctx context.Context, channel domain.Channel, senderUserID int64, events []domain.ChannelUpdateEvent) {
	if r == nil || channel.ID == 0 {
		return
	}
	for _, event := range events {
		switch event.Type {
		case domain.ChannelUpdateNewMessage:
			r.enqueueChannelMessageFanout(ctx, senderUserID, domain.SendChannelMessageResult{
				Channel: channel,
				Message: event.Message,
				Event:   event,
			}, nil)
		case domain.ChannelUpdateEditMessage:
			r.enqueueChannelEditMessageFanout(ctx, senderUserID, domain.EditChannelMessageResult{
				Channel: channel,
				Message: event.Message,
				Event:   event,
			})
		}
	}
}

// NotifyChannelImportRefreshed 让频道的在线成员重新拉取：回填不写逐条事件，游标落后的客户端
// 收到带当前 pts 的 updateChannelTooLong 后调 getChannelDifference，拿到 tooLong 并重载历史。
func (r *Router) NotifyChannelImportRefreshed(ctx context.Context, channel domain.Channel) {
	if r == nil || channel.ID == 0 {
		return
	}
	r.invalidateRPCProjectionForChannel(channel.ID)
	r.nudgeBeyondCapChannelMembers(ctx, channel.ID, channel.Pts, nil)
}
