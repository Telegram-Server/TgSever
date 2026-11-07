package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// TestContactSignUpServiceMessageConvertsToTheOfficialAction 只测协议映射：入驻写的 contactSignUp
// 服务消息转成 messageService{action: messageActionContactSignUp}，带上发送者与静音标记。
func TestContactSignUpServiceMessageConvertsToTheOfficialAction(t *testing.T) {
	from := domain.Peer{Type: domain.PeerTypeUser, ID: 42}
	got := tgMessage(domain.Message{
		ID: 7, OwnerUserID: 9, Peer: from, From: from, Date: 1700000000, Silent: true,
		Media: &domain.MessageMedia{
			Kind:          domain.MessageMediaKindService,
			ServiceAction: &domain.MessageServiceAction{Kind: domain.MessageServiceActionContactSignUp},
		},
	})
	service, ok := got.(*tg.MessageService)
	if !ok {
		t.Fatalf("converted = %T, want *tg.MessageService", got)
	}
	if _, ok := service.Action.(*tg.MessageActionContactSignUp); !ok {
		t.Fatalf("action = %T, want *tg.MessageActionContactSignUp", service.Action)
	}
	fromID, ok := service.FromID.(*tg.PeerUser)
	if service.Out || !service.Silent || !ok || fromID.UserID != 42 || service.ID != 7 {
		t.Fatalf("service message = %+v, want incoming silent message 7 from user 42", service)
	}
}
