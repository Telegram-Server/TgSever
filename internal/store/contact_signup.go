package store

import (
	"context"

	"telesrv/internal/domain"
)

// ContactSignUpNoticeStore 写 messageActionContactSignUp 服务消息（新账号默认入驻用）。memory 与
// postgres 实现同一份契约（storetest.ContactSignUpNoticeContract）。
type ContactSignUpNoticeStore interface {
	// DeliverContactSignUpNotice 在 n.OwnerUserID 与 n.FromUserID 的私聊里给 Owner 写一条以 From 为
	// 发送者的 contactSignUp 服务消息（收到的消息，计未读），只写 Owner 一侧：消息、会话、pts 事件
	// 与投递任务在同一个事务里，由 outbox 推送。每对 (Owner, From) 只写一次：再次调用返回第一次写入
	// 的消息与 created=false。Owner 或 From 不存在、已注销，或 Owner 是 bot 时返回
	// domain.ErrUserNotFound，什么都不写。
	DeliverContactSignUpNotice(ctx context.Context, n domain.ContactSignUpNotice) (msg domain.Message, created bool, err error)
}
