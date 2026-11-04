package contacts

import (
	"context"
	"testing"

	privacyapp "telesrv/internal/app/privacy"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

type peerSettingsFixture struct {
	svc      *Service
	contacts *memory.ContactStore
	users    *memory.UserStore
	owner    domain.User
}

func newPeerSettingsFixture(t *testing.T) peerSettingsFixture {
	t.Helper()
	ctx := context.Background()
	users := memory.NewUserStore()
	contactsStore := memory.NewContactStore()
	owner, err := users.Create(ctx, domain.User{Phone: "15550001000", FirstName: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	privacyStore := memory.NewPrivacyStore()
	contactsStore.AttachBlocklistStores(memory.NewStoryStore(), privacyStore, users, memory.NewChannelStore())
	privacySvc := privacyapp.NewService(privacyStore, contactsStore)
	svc := NewService(contactsStore, users).Configure(WithPrivacyEvaluator(privacySvc))
	return peerSettingsFixture{svc: svc, contacts: contactsStore, users: users, owner: owner}
}

func (f peerSettingsFixture) user(t *testing.T, u domain.User) domain.User {
	t.Helper()
	created, err := f.users.Create(context.Background(), u)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func (f peerSettingsFixture) settings(t *testing.T, peer domain.User) domain.PeerSettings {
	t.Helper()
	got, err := f.svc.GetPeerSettings(context.Background(), f.owner.ID, domain.Peer{Type: domain.PeerTypeUser, ID: peer.ID})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// 机器人、已注销账号、官方客服号不能加为联系人，聊天顶部不出现「添加联系人 / 屏蔽」；
// 普通陌生人照常出现。
func TestPeerSettingsOfferNoContactActionsForBotsDeletedAndSupport(t *testing.T) {
	f := newPeerSettingsFixture(t)
	for name, peer := range map[string]domain.User{
		"bot":     f.user(t, domain.User{FirstName: "Helper", Bot: true}),
		"deleted": f.user(t, domain.User{Phone: "15550001001", FirstName: "Gone", Deleted: true}),
		"support": f.user(t, domain.User{Phone: "15550001002", FirstName: "Support", Support: true}),
	} {
		if got := f.settings(t, peer); got != (domain.PeerSettings{}) {
			t.Errorf("%s: settings = %+v, want empty", name, got)
		}
	}
	stranger := f.user(t, domain.User{Phone: "15550001003", FirstName: "Stranger"})
	if got := f.settings(t, stranger); !got.AddContact || !got.BlockContact {
		t.Errorf("stranger: settings = %+v, want add_contact and block_contact", got)
	}
}

// 双方按用户名互加（对方记录里没有我的号码）时会提示「分享我的手机号码」；点分享
// （acceptContact）后对方能看到号码，提示消失，对方给我起的名字不变。
func TestAcceptContactSharesPhoneWithAMutualContact(t *testing.T) {
	ctx := context.Background()
	f := newPeerSettingsFixture(t)
	peer := f.user(t, domain.User{Phone: "15550001004", FirstName: "Peer"})
	if _, err := f.svc.AddContact(ctx, f.owner.ID, domain.ContactInput{ContactUserID: peer.ID, FirstName: "Peer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AddContact(ctx, peer.ID, domain.ContactInput{ContactUserID: f.owner.ID, FirstName: "Boss"}); err != nil {
		t.Fatal(err)
	}
	if got := f.settings(t, peer); !got.ShareContact {
		t.Fatalf("before sharing: settings = %+v, want share_contact", got)
	}

	if _, err := f.svc.AcceptContact(ctx, f.owner.ID, peer.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.settings(t, peer); got.ShareContact {
		t.Fatalf("after sharing: settings = %+v, want no share_contact", got)
	}
	theirs, found, err := f.contacts.Get(ctx, peer.ID, f.owner.ID)
	if err != nil || !found {
		t.Fatalf("peer's entry found=%v err=%v", found, err)
	}
	if theirs.Phone != f.owner.Phone || theirs.FirstName != "Boss" {
		t.Fatalf("peer's entry = phone %q name %q, want phone %q name Boss", theirs.Phone, theirs.FirstName, f.owner.Phone)
	}
}

// 提示栏不给机器人任何动作，但屏蔽状态照常报告（userFull.blocked 依赖它）。
func TestPeerSettingsStillReportBlockedBot(t *testing.T) {
	f := newPeerSettingsFixture(t)
	bot := f.user(t, domain.User{FirstName: "Helper", Bot: true})
	block := store.BlocklistMutation{Kind: store.BlocklistBlock, OwnerUserID: f.owner.ID, PeerIDs: []int64{bot.ID}, Date: 1}
	if _, err := f.contacts.MutateBlocklist(context.Background(), block, store.BlocklistDeliveryEffects); err != nil {
		t.Fatal(err)
	}
	got := f.settings(t, bot)
	if !got.Blocked || got.AddContact || got.BlockContact {
		t.Fatalf("blocked bot settings = %+v, want Blocked only", got)
	}
}
