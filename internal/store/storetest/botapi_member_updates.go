package storetest

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// BotAPIMemberUpdateFixture 是一个新建的机器人（还没有任何 allowed_updates 设置）与一个普通用户。
type BotAPIMemberUpdateFixture struct {
	Store       store.BotAPIUpdateStore
	BotID       int64
	OtherUserID int64
}

// BotAPIMemberUpdateChat 是契约给 BotID 的成员更新用的 chat：只作为载荷与 peer，不要求频道存在。
func BotAPIMemberUpdateChat(botID int64) domain.Peer {
	return domain.Peer{Type: domain.PeerTypeChannel, ID: 880000 + botID}
}

// BotAPIMemberUpdateRequest 构造一条 subject 由普通成员升为管理员的成员更新。
func BotAPIMemberUpdateRequest(f BotAPIMemberUpdateFixture, kind domain.BotAPIUpdateKind, subject int64) domain.EnqueueBotAPIUpdateRequest {
	chat := BotAPIMemberUpdateChat(f.BotID)
	now := baseNow()
	return domain.EnqueueBotAPIUpdateRequest{
		BotUserID: f.BotID, Kind: kind, Peer: chat, Date: now,
		ChatMember: &domain.BotChatMemberUpdated{
			Chat: chat, ActorUserID: f.OtherUserID, Date: now, ViaJoinRequest: true,
			Old: domain.BotChatMemberSnapshot{UserID: subject, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberActive},
			New: domain.BotChatMemberSnapshot{
				UserID: subject, Role: domain.ChannelRoleAdmin, Status: domain.ChannelMemberActive, Rank: "mod",
				AdminRights: domain.ChannelAdminRights{BanUsers: true, PinMessages: true},
			},
		},
	}
}

// BotAPIMemberUpdates 校验 my_chat_member / chat_member 更新的入队与读取：载荷往返且读出的是
// 拷贝、同一变化再来一次也入队、chat_member 需要显式订阅、allowed_updates 之外的种类不入队、
// 代表性的畸形请求被拒。其余载荷校验（chat 不一致、主体不符、无变化）由 domain 的
// ValidateFor / ValidateBotAPIChatMemberRequest 表驱动测试覆盖。每个子测试新建自己的机器人。
func BotAPIMemberUpdates(t *testing.T, newFixture func(t *testing.T) BotAPIMemberUpdateFixture) {
	t.Helper()
	ctx := context.Background()
	enqueue := func(t *testing.T, f BotAPIMemberUpdateFixture, req domain.EnqueueBotAPIUpdateRequest) (domain.BotAPIUpdate, bool) {
		t.Helper()
		row, created, err := f.Store.EnqueueBotAPIUpdate(ctx, req)
		if err != nil {
			t.Fatalf("enqueue %s: %v", req.Kind, err)
		}
		return row, created
	}
	list := func(t *testing.T, f BotAPIMemberUpdateFixture, from int64) []domain.BotAPIUpdate {
		t.Helper()
		rows, err := f.Store.ListBotAPIUpdates(ctx, f.BotID, from, 100)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		return rows
	}

	t.Run("round trip returns a copy", func(t *testing.T) {
		f := newFixture(t)
		my := BotAPIMemberUpdateRequest(f, domain.BotAPIUpdateMyChatMember, f.BotID)
		first, created := enqueue(t, f, my)
		if !created || first.ID == 0 {
			t.Fatalf("enqueue my_chat_member = %+v created=%v", first, created)
		}
		rows := list(t, f, first.ID)
		if len(rows) != 1 {
			t.Fatalf("rows = %+v, want one", rows)
		}
		got := rows[0]
		if got.Kind != domain.BotAPIUpdateMyChatMember || got.Peer != my.Peer || got.MessageID != 0 ||
			got.ChatMember == nil || *got.ChatMember != *my.ChatMember {
			t.Fatalf("my_chat_member round trip = %+v (payload %+v), want %+v", got, got.ChatMember, my.ChatMember)
		}
		got.ChatMember.New.Rank = "mutated"
		if again := list(t, f, first.ID); again[0].ChatMember.New.Rank != "mod" {
			t.Fatalf("ListBotAPIUpdates must return a copy of the member payload, got %+v", again[0].ChatMember)
		}
	})

	// 成员更新没有消息去重键：同一变化再来一次也是新的一条。
	t.Run("a repeated change queues again", func(t *testing.T) {
		f := newFixture(t)
		my := BotAPIMemberUpdateRequest(f, domain.BotAPIUpdateMyChatMember, f.BotID)
		first, created := enqueue(t, f, my)
		if !created {
			t.Fatal("first my_chat_member not queued")
		}
		second, created := enqueue(t, f, my)
		if !created || second.ID == first.ID {
			t.Fatalf("repeated change = %+v created=%v, want a new row", second, created)
		}
		if rows := list(t, f, first.ID); len(rows) != 2 {
			t.Fatalf("rows = %+v, want two", rows)
		}
	})

	// 官方默认（allowed_updates 未设或为空）不投递 chat_member。
	t.Run("chat_member needs an explicit opt-in", func(t *testing.T) {
		f := newFixture(t)
		member := BotAPIMemberUpdateRequest(f, domain.BotAPIUpdateChatMember, f.OtherUserID)
		if row, created := enqueue(t, f, member); created || row.ID != 0 {
			t.Fatalf("default chat_member = %+v created=%v, want dropped", row, created)
		}
		if _, created := enqueue(t, f, BotAPIMemberUpdateRequest(f, domain.BotAPIUpdateMyChatMember, f.BotID)); !created {
			t.Fatal("default my_chat_member not queued")
		}
		if err := f.Store.SetBotAPIAllowedUpdates(ctx, f.BotID, []domain.BotAPIUpdateKind{domain.BotAPIUpdateChatMember}); err != nil {
			t.Fatalf("set allowed updates: %v", err)
		}
		row, created := enqueue(t, f, member)
		if !created {
			t.Fatal("opted-in chat_member not queued")
		}
		rows := list(t, f, row.ID)
		if len(rows) != 1 || rows[0].Kind != domain.BotAPIUpdateChatMember || rows[0].ChatMember == nil || rows[0].ChatMember.New.UserID != f.OtherUserID {
			t.Fatalf("chat_member round trip = %+v", rows)
		}
	})

	t.Run("kinds outside allowed_updates are dropped", func(t *testing.T) {
		f := newFixture(t)
		if err := f.Store.SetBotAPIAllowedUpdates(ctx, f.BotID, []domain.BotAPIUpdateKind{domain.BotAPIUpdateChatMember}); err != nil {
			t.Fatalf("set allowed updates: %v", err)
		}
		if row, created := enqueue(t, f, BotAPIMemberUpdateRequest(f, domain.BotAPIUpdateMyChatMember, f.BotID)); created || row.ID != 0 {
			t.Fatalf("my_chat_member outside allowed_updates = %+v created=%v, want dropped", row, created)
		}
	})

	t.Run("malformed requests are rejected", func(t *testing.T) {
		f := newFixture(t)
		cases := map[string]func(*domain.EnqueueBotAPIUpdateRequest){
			"missing payload":    func(r *domain.EnqueueBotAPIUpdateRequest) { r.ChatMember = nil },
			"payload on message": func(r *domain.EnqueueBotAPIUpdateRequest) { r.Kind = domain.BotAPIUpdateMessage },
		}
		for name, mutate := range cases {
			req := BotAPIMemberUpdateRequest(f, domain.BotAPIUpdateMyChatMember, f.BotID)
			mutate(&req)
			if _, created, err := f.Store.EnqueueBotAPIUpdate(ctx, req); err == nil || created {
				t.Fatalf("%s: created=%v err=%v, want rejection", name, created, err)
			}
		}
	})
}
