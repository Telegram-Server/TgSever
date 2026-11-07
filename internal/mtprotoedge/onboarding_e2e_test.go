package mtprotoedge

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// TestOnboardingEndToEnd 只验证接线：设好入驻模板后，客户端自己用验证码注册的新账号由调度器
// 执行入驻，contacts.getContacts 里有默认联系人，新账号也已经在默认的群里。入驻的规则矩阵在
// internal/app/onboarding，各条建号路径写入任务在 store 契约与 admin 层。
func TestOnboardingEndToEnd(t *testing.T) {
	srv := startLoginPasswordServer(t, loginPasswordServerOptions{})
	ctx := context.Background()
	support, err := srv.users.Create(ctx, domain.User{AccessHash: 1, Phone: "15550164801", FirstName: "Support"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := srv.channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: support.ID, Title: "Lobby", Megagroup: true, Date: int(time.Now().Unix())})
	if err != nil {
		t.Fatal(err)
	}
	group := created.Channel.ID
	if _, _, _, err := srv.onboarding.SetOnboardingDefaults(ctx, domain.OnboardingDefaults{
		Contacts:   []domain.OnboardingContact{{UserID: support.ID}},
		ChannelIDs: []int64{group},
	}); err != nil {
		t.Fatal(err)
	}

	var newcomerID int64
	srv.run(t, func(ctx context.Context, raw *tg.Client) error {
		newcomer, err := signUpWithCode(ctx, raw, "+15550164802", "Newcomer")
		if err != nil {
			return fmt.Errorf("sign up: %w", err)
		}
		newcomerID = newcomer.ID
		// 入驻由调度器异步执行，空闲时轮询间隔最长 5 秒。
		deadline := time.Now().Add(15 * time.Second)
		for {
			res, err := raw.ContactsGetContacts(ctx, 0)
			if err != nil {
				return fmt.Errorf("get contacts: %w", err)
			}
			if list, ok := res.(*tg.ContactsContacts); ok && len(list.Contacts) == 1 && list.Contacts[0].UserID == support.ID {
				return assertSignUpNoticeDialog(ctx, raw, support.ID)
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("contacts = %+v, want the default contact %d", res, support.ID)
			}
			time.Sleep(200 * time.Millisecond)
		}
	})
	member, err := srv.channels.GetParticipant(ctx, support.ID, group, newcomerID)
	if err != nil || member.Status != domain.ChannelMemberActive {
		t.Fatalf("newcomer in the default group = %+v err=%v, want an active member", member, err)
	}
}

// assertSignUpNoticeDialog 要求 messages.getDialogs 里有和 contactID 的私聊，最后一条是由它发出的
// messageActionContactSignUp，且计 1 条未读。
func assertSignUpNoticeDialog(ctx context.Context, raw *tg.Client, contactID int64) error {
	res, err := raw.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 50})
	if err != nil {
		return fmt.Errorf("get dialogs: %w", err)
	}
	modified, ok := res.AsModified()
	if !ok {
		return fmt.Errorf("get dialogs = %T, want a dialog list", res)
	}
	for _, d := range modified.GetDialogs() {
		dialog, ok := d.(*tg.Dialog)
		if !ok {
			continue
		}
		if peer, ok := dialog.Peer.(*tg.PeerUser); !ok || peer.UserID != contactID {
			continue
		}
		if dialog.UnreadCount != 1 {
			return fmt.Errorf("dialog with %d unread = %d, want 1", contactID, dialog.UnreadCount)
		}
		for _, m := range modified.GetMessages() {
			service, ok := m.(*tg.MessageService)
			if !ok || service.ID != dialog.TopMessage {
				continue
			}
			from, ok := service.FromID.(*tg.PeerUser)
			if _, isSignUp := service.Action.(*tg.MessageActionContactSignUp); !isSignUp || !ok || from.UserID != contactID || service.Out {
				return fmt.Errorf("top message = %+v, want an incoming contact sign-up from %d", service, contactID)
			}
			return nil
		}
		return fmt.Errorf("top message %d of the dialog with %d is not a service message", dialog.TopMessage, contactID)
	}
	return fmt.Errorf("get dialogs has no chat with the default contact %d", contactID)
}
