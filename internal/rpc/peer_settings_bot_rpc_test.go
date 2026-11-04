package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appcontacts "telesrv/internal/app/contacts"
	appdialogs "telesrv/internal/app/dialogs"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

// 从联系人列表点进还没聊过的联系人（没有对话记录），关掉「分享我的手机号码」后再打开
// 会话，提示栏不再出现。
func TestHiddenContactBarStaysHiddenWithoutADialog(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	contactsStore := memory.NewContactStore()
	rpcTestBlocklistStores(contactsStore, users, memory.NewStoryStore(), memory.NewUpdateEventStore())
	contacts := appcontacts.NewService(contactsStore, users)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(users),
		Contacts: contacts,
		Dialogs:  appdialogs.NewService(memory.NewDialogStore(), memory.NewChannelStore()),
	}, zaptest.NewLogger(t), clock.System)
	owner, err := users.Create(ctx, domain.User{AccessHash: 9201, Phone: "13800009201", FirstName: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	friend, err := users.Create(ctx, domain.User{AccessHash: 9202, Phone: "13800009202", FirstName: "Friend"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := contacts.AddContact(ctx, owner.ID, domain.ContactInput{ContactUserID: friend.ID, FirstName: "Friend"}); err != nil {
		t.Fatal(err)
	}
	reqCtx := WithUserID(ctx, owner.ID)
	input := &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash}

	before, err := r.onMessagesGetPeerSettings(reqCtx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !before.Settings.ShareContact {
		t.Fatalf("before closing: settings = %+v, want share_contact", before.Settings)
	}
	if ok, err := r.onMessagesHidePeerSettingsBar(reqCtx, input); err != nil || !ok {
		t.Fatalf("hide = %v %v", ok, err)
	}
	after, err := r.onMessagesGetPeerSettings(reqCtx, input)
	if err != nil {
		t.Fatal(err)
	}
	if after.Settings.ShareContact || after.Settings.AddContact || after.Settings.BlockContact {
		t.Fatalf("after closing: settings = %+v, want empty bar", after.Settings)
	}
}

// 机器人会话：getPeerSettings 不给「添加联系人 / 屏蔽」；屏蔽它之后 userFull.blocked
// 仍然报 true（屏蔽状态不能再从提示栏标志反推）。
func TestBotChatHasNoContactBarAndKeepsBlockedState(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	contactsStore := memory.NewContactStore()
	rpcTestBlocklistStores(contactsStore, users, memory.NewStoryStore(), memory.NewUpdateEventStore())
	r := New(Config{}, Deps{
		Users:    appusers.NewService(users),
		Contacts: appcontacts.NewService(contactsStore, users),
	}, zaptest.NewLogger(t), clock.System)
	owner, err := users.Create(ctx, domain.User{AccessHash: 9101, Phone: "13800009101", FirstName: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	bot, err := users.Create(ctx, domain.User{AccessHash: 9102, FirstName: "Helper", Bot: true})
	if err != nil {
		t.Fatal(err)
	}
	reqCtx := WithUserID(ctx, owner.ID)

	got, err := r.onMessagesGetPeerSettings(reqCtx, &tg.InputPeerUser{UserID: bot.ID, AccessHash: bot.AccessHash})
	if err != nil {
		t.Fatal(err)
	}
	if got.Settings.AddContact || got.Settings.BlockContact {
		t.Fatalf("bot peer settings = %+v, want no add/block bar", got.Settings)
	}
	unblocked := &tg.UserFull{}
	if err := r.applyContactPeerStateToUserFull(reqCtx, owner.ID, bot.ID, unblocked); err != nil {
		t.Fatal(err)
	}
	if unblocked.Blocked {
		t.Fatalf("unblocked bot userFull.blocked = true, want false")
	}

	block := store.BlocklistMutation{Kind: store.BlocklistBlock, OwnerUserID: owner.ID, PeerIDs: []int64{bot.ID}, Date: 1}
	if _, err := contactsStore.MutateBlocklist(ctx, block, store.BlocklistDeliveryEffects); err != nil {
		t.Fatal(err)
	}
	r.invalidateRPCProjectionForViewer(owner.ID)
	full := &tg.UserFull{}
	if err := r.applyContactPeerStateToUserFull(reqCtx, owner.ID, bot.ID, full); err != nil {
		t.Fatal(err)
	}
	if !full.Blocked {
		t.Fatalf("blocked bot userFull.blocked = false, want true")
	}
	if full.Settings.AddContact || full.Settings.BlockContact {
		t.Fatalf("blocked bot userFull.settings = %+v, want no add/block bar", full.Settings)
	}
}
