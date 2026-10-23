package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appcontacts "telesrv/internal/app/contacts"
	appstories "telesrv/internal/app/stories"
	appupdates "telesrv/internal/app/updates"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// 用户经 contacts.block / contacts.unblock 拉黑、解除拉黑 bot：bot 在线时收到带 qts 的
// updateBotStopped（附带该用户），离线后经 getDifference 按 qts 补回（#148）。
func TestBotStoppedPushAndDifference(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	botUpdates := memory.NewBotUpdateStore(users)
	contactsStore := memory.NewContactStore()
	storyStore := memory.NewStoryStore()
	events := memory.NewUpdateEventStore()
	rpcTestBlocklistStores(contactsStore, users, storyStore, events)
	contactsStore.AttachBotUpdateStore(botUpdates)
	sessions := &phoneCaptureSessions{}
	r := New(Config{}, Deps{
		Users:      appusers.NewService(users),
		Contacts:   appcontacts.NewService(contactsStore, users),
		Stories:    appstories.NewService(storyStore),
		Updates:    appupdates.NewService(memory.NewUpdateStateStore(), events),
		BotUpdates: botUpdates,
		Sessions:   sessions,
	}, zaptest.NewLogger(t), clock.System)
	user, err := users.Create(ctx, domain.User{AccessHash: 8201, Phone: "13800008201", FirstName: "Stopper"})
	if err != nil {
		t.Fatal(err)
	}
	bot, err := users.Create(ctx, domain.User{AccessHash: 8202, FirstName: "StoppedBot", Bot: true})
	if err != nil {
		t.Fatal(err)
	}
	userCtx := WithAuthKeyID(WithSessionID(WithUserID(ctx, user.ID), 82010), [8]byte{8, 2, 0, 1})
	botCtx := WithAuthKeyID(WithSessionID(WithUserID(ctx, bot.ID), 82020), [8]byte{8, 2, 0, 2})
	inputBot := &tg.InputPeerUser{UserID: bot.ID, AccessHash: bot.AccessHash}

	if _, err := r.onContactsBlock(userCtx, &tg.ContactsBlockRequest{ID: inputBot}); err != nil {
		t.Fatalf("block bot: %v", err)
	}
	if _, err := r.onContactsUnblock(userCtx, &tg.ContactsUnblockRequest{ID: inputBot}); err != nil {
		t.Fatalf("unblock bot: %v", err)
	}

	hasUser := func(list []tg.UserClass) bool {
		for _, u := range list {
			if u, ok := u.(*tg.User); ok && u.ID == user.ID {
				return true
			}
		}
		return false
	}
	checkStopped := func(where string, got []*tg.UpdateBotStopped) {
		t.Helper()
		if len(got) != 2 {
			t.Fatalf("%s: %d updateBotStopped, want 2: %+v", where, len(got), got)
		}
		for i, stopped := range []bool{true, false} {
			if got[i].Qts != i+2 || got[i].UserID != user.ID || got[i].Stopped != stopped {
				t.Fatalf("%s: update %d = %+v, want qts %d user %d stopped=%v", where, i, got[i], i+2, user.ID, stopped)
			}
		}
	}

	var pushed []*tg.UpdateBotStopped
	for _, rec := range sessions.records() {
		updates, ok := rec.msg.(*tg.Updates)
		if !ok {
			continue
		}
		for _, update := range updates.Updates {
			if u, ok := update.(*tg.UpdateBotStopped); ok {
				if rec.userID != bot.ID {
					t.Fatalf("updateBotStopped pushed to user %d, want only the bot", rec.userID)
				}
				if !hasUser(updates.Users) {
					t.Fatalf("pushed updateBotStopped without the user: %+v", updates.Users)
				}
				for _, chat := range updates.Chats {
					if chat == nil {
						t.Fatal("pushed updateBotStopped with a nil chat")
					}
				}
				pushed = append(pushed, u)
			}
		}
	}
	checkStopped("push", pushed)

	diff, err := r.onUpdatesGetDifference(botCtx, &tg.UpdatesGetDifferenceRequest{Qts: 0, Date: 1})
	if err != nil {
		t.Fatalf("bot getDifference: %v", err)
	}
	full, ok := diff.(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("bot getDifference = %T, want updates.difference", diff)
	}
	var replayed []*tg.UpdateBotStopped
	for _, update := range full.OtherUpdates {
		if u, ok := update.(*tg.UpdateBotStopped); ok {
			replayed = append(replayed, u)
		}
	}
	checkStopped("getDifference", replayed)
	if !hasUser(full.Users) || full.State.Qts != 3 {
		t.Fatalf("getDifference users=%+v qts=%d, want the user and qts 3", full.Users, full.State.Qts)
	}
}
