package contacts

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

// 用户在私聊里拉黑、解除拉黑 bot 时，bot 的 qts 序列各得一条 updateBotStopped（#148）：
// 拉黑 stopped=true，解除 stopped=false；拉黑状态没变的重复操作不生成；拉黑普通用户、
// bot 拉黑用户都不进任何 bot 的序列。
func TestBlockingBotGeneratesBotStoppedQtsUpdates(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	updates := memory.NewBotUpdateStore(users)
	base := memory.NewContactStore()
	base.AttachBlocklistStores(memory.NewStoryStore(), memory.NewPrivacyStore(), users, memory.NewChannelStore())
	base.AttachBotUpdateStore(updates)
	svc := NewService(base, users)
	newUser := func(bot bool) int64 {
		t.Helper()
		u, err := users.Create(ctx, domain.User{AccessHash: 5, FirstName: "u", Bot: bot})
		if err != nil {
			t.Fatal(err)
		}
		return u.ID
	}
	user, bot, human := newUser(false), newUser(true), newUser(false)
	mutate := func(step string, kind store.BlocklistMutationKind, owner int64, peers ...int64) store.BlocklistMutationSnapshot {
		t.Helper()
		res, err := svc.MutateBlocklist(ctx, store.BlocklistMutation{Kind: kind, OwnerUserID: owner, PeerIDs: peers, Date: contactMutationTestDate}, store.BlocklistDeliveryEffects)
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		return res
	}

	blocked := mutate("block bot", store.BlocklistBlock, user, bot)
	if len(blocked.BotUpdates) != 1 || blocked.BotUpdates[0].Qts != 1 {
		t.Fatalf("block bot returned bot updates %+v, want the qts=1 record", blocked.BotUpdates)
	}
	if again := mutate("block bot again", store.BlocklistBlock, user, bot); len(again.BotUpdates) != 0 {
		t.Fatalf("repeated block returned bot updates %+v", again.BotUpdates)
	}
	mutate("block human", store.BlocklistBlock, user, human)
	mutate("bot blocks human", store.BlocklistBlock, bot, human)
	unblocked := mutate("unblock bot", store.BlocklistUnblock, user, bot)
	if len(unblocked.BotUpdates) != 1 || unblocked.BotUpdates[0].Qts != 2 {
		t.Fatalf("unblock bot returned bot updates %+v, want the qts=2 record", unblocked.BotUpdates)
	}
	mutate("unblock bot again", store.BlocklistUnblock, user, bot)
	// setBlocked 一次替换整张名单：把 bot 换进去等同拉黑。
	mutate("replace with bot", store.BlocklistReplace, user, human, bot)

	rows, err := updates.ListBotUpdates(ctx, bot, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := []bool{true, false, true}
	if len(rows) != len(want) {
		t.Fatalf("bot qts records = %+v, want %d", rows, len(want))
	}
	for i, row := range rows {
		if row.Qts != i+1 || row.Date != contactMutationTestDate || row.BotStopped == nil {
			t.Fatalf("record %d = %+v", i, row)
		}
		if got := *row.BotStopped; got.UserID != user || got.Stopped != want[i] {
			t.Fatalf("record %d bot stopped = %+v, want user %d stopped=%v", i, got, user, want[i])
		}
	}
	for _, id := range []int64{user, human} {
		st, err := updates.BotUpdatesState(ctx, id)
		if err != nil || st.Qts != 0 {
			t.Fatalf("non-bot %d qts state = %+v err=%v, want none", id, st, err)
		}
	}
}
