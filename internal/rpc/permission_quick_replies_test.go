package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

// permissionQuickReplies 让 messages.sendQuickReplyMessages 通过业务自动化可用性检查；只实现
// GetQuickReplyMessages（返回空列表），记下被读的次数：权限拒绝的请求不应读到快捷回复。
type permissionQuickReplies struct {
	AccountService
	AccountBusinessAutomationService
	reads int
}

func (q *permissionQuickReplies) GetQuickReplyMessages(context.Context, int64, int, []int) (domain.QuickReplyMessages, error) {
	q.reads++
	return domain.QuickReplyMessages{}, nil
}

// permissionNoMessages 只占位 Deps.Messages：快捷回复列表为空时不会发送。
type permissionNoMessages struct{ MessagesService }

func assertRPCErrorType(t *testing.T, err error, typ string) {
	t.Helper()
	rpcErr, ok := tgerr.As(err)
	if !ok || rpcErr.Type != typ {
		t.Fatalf("err = %v, want %s", err, typ)
	}
}

// 业务自动化不可用时仍先返回 SHORTCUT_INVALID（与员工权限之前的错误优先级一致），
// 哪怕对方不合法或发送被关闭。
func TestPermissionSendQuickReplyMessagesKeepsAutomationErrorFirst(t *testing.T) {
	f := newPermissionMappingFixture(t)
	f.router.deps.Account = nil
	f.perms.items[f.me.ID] = permissionFlagsOff(f.me.ID, "send")
	otherPeer := &tg.InputPeerUser{UserID: f.other.ID, AccessHash: f.other.AccessHash}

	_, err := f.router.onMessagesSendQuickReplyMessages(f.ctx, &tg.MessagesSendQuickReplyMessagesRequest{Peer: otherPeer, ShortcutID: 1, ID: []int{1}, RandomID: []int64{1}})
	assertRPCErrorType(t, err, "SHORTCUT_INVALID")
	_, err = f.router.onMessagesSendQuickReplyMessages(f.ctx, &tg.MessagesSendQuickReplyMessagesRequest{Peer: inputPeerChannel(f.group), ShortcutID: 1, ID: []int{1}, RandomID: []int64{2}})
	assertRPCErrorType(t, err, "SHORTCUT_INVALID")
}

// 业务自动化可用（newPermissionMappingFixture 接上了 permissionQuickReplies）时，发送被关闭的
// 账号在读取、发送任何快捷回复之前被拒绝。
func TestPermissionSendQuickReplyMessagesDeniedBeforeReadingTemplates(t *testing.T) {
	f := newPermissionMappingFixture(t)
	quick := f.router.deps.Account.(*permissionQuickReplies)
	otherPeer := &tg.InputPeerUser{UserID: f.other.ID, AccessHash: f.other.AccessHash}
	req := &tg.MessagesSendQuickReplyMessagesRequest{Peer: otherPeer, ShortcutID: 1, ID: []int{1}, RandomID: []int64{3}}

	f.perms.items[f.me.ID] = permissionFlagsOff(f.me.ID, "send")
	_, err := f.router.onMessagesSendQuickReplyMessages(f.ctx, req)
	assertPermissionDenied(t, err, "TELESRV_PERMISSION_SEND")
	if quick.reads != 0 {
		t.Fatalf("denied request read quick replies %d times", quick.reads)
	}

	f.perms.items[f.me.ID] = permissionFlagsOff(f.me.ID)
	if _, err := f.router.onMessagesSendQuickReplyMessages(f.ctx, req); err != nil {
		t.Fatalf("allowed: %v", err)
	}
	if quick.reads != 1 {
		t.Fatalf("allowed request read quick replies %d times, want 1", quick.reads)
	}
}
