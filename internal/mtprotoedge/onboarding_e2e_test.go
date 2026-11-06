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
				return nil
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
