package storetest

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// BotAPIJoinRequestUpdateRequest 构造一条 OtherUserID 通过邀请链接申请加入的 chat_join_request。
// 复用成员更新的 fixture（新建机器人 + 普通用户）与 chat。
func BotAPIJoinRequestUpdateRequest(f BotAPIMemberUpdateFixture) domain.EnqueueBotAPIUpdateRequest {
	chat := BotAPIMemberUpdateChat(f.BotID)
	now := baseNow()
	return domain.EnqueueBotAPIUpdateRequest{
		BotUserID: f.BotID, Kind: domain.BotAPIUpdateChatJoinRequest, Peer: chat, Date: now,
		ChatJoinRequest: &domain.BotChatJoinRequest{
			Chat: chat, UserID: f.OtherUserID, Date: now, Bio: "hello there",
			Invite: &domain.ChannelInvite{
				ChannelID: chat.ID, InviteID: 7, Hash: "joinRequestHash", AdminUserID: f.OtherUserID,
				Title: "applicants", RequestNeeded: true, ExpireDate: now + 3600, RequestedCount: 1, Date: now - 60,
			},
		},
	}
}

// BotAPIJoinRequestUpdates 校验 chat_join_request 的入队与读取：载荷往返（含邀请链接快照）且
// 读出的是拷贝、同一申请人再申请一次也入队、默认集合包含它、显式 allowed_updates 不含它时
// 不入队、尾部读取带载荷、代表性的畸形请求被拒。每个子测试新建自己的机器人。
func BotAPIJoinRequestUpdates(t *testing.T, newFixture func(t *testing.T) BotAPIMemberUpdateFixture) {
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
	sameRequest := func(got, want *domain.BotChatJoinRequest) bool {
		if got == nil || want == nil || got.Chat != want.Chat || got.UserID != want.UserID ||
			got.Date != want.Date || got.Bio != want.Bio || (got.Invite == nil) != (want.Invite == nil) {
			return false
		}
		return got.Invite == nil || *got.Invite == *want.Invite
	}

	t.Run("round trip returns a copy", func(t *testing.T) {
		f := newFixture(t)
		req := BotAPIJoinRequestUpdateRequest(f)
		first, created := enqueue(t, f, req)
		if !created || first.ID == 0 {
			t.Fatalf("enqueue chat_join_request = %+v created=%v", first, created)
		}
		rows := list(t, f, first.ID)
		if len(rows) != 1 {
			t.Fatalf("rows = %+v, want one", rows)
		}
		got := rows[0]
		if got.Kind != domain.BotAPIUpdateChatJoinRequest || got.Peer != req.Peer || got.MessageID != 0 ||
			got.Date != req.Date || got.ChatMember != nil || !sameRequest(got.ChatJoinRequest, req.ChatJoinRequest) {
			t.Fatalf("chat_join_request round trip = %+v (payload %+v), want %+v", got, got.ChatJoinRequest, req.ChatJoinRequest)
		}
		got.ChatJoinRequest.Bio = "mutated"
		got.ChatJoinRequest.Invite.Title = "mutated"
		again := list(t, f, first.ID)
		if again[0].ChatJoinRequest.Bio != "hello there" || again[0].ChatJoinRequest.Invite.Title != "applicants" {
			t.Fatalf("ListBotAPIUpdates must return a copy of the join request payload, got %+v", again[0].ChatJoinRequest)
		}
		tail, err := f.Store.ListTailBotAPIUpdates(ctx, f.BotID, 1, 100)
		if err != nil || len(tail) != 1 || !sameRequest(tail[0].ChatJoinRequest, req.ChatJoinRequest) {
			t.Fatalf("tail = %+v err=%v", tail, err)
		}
	})

	// 通过公开用户名申请时没有邀请链接。
	t.Run("a request without an invite link", func(t *testing.T) {
		f := newFixture(t)
		req := BotAPIJoinRequestUpdateRequest(f)
		req.ChatJoinRequest.Invite = nil
		req.ChatJoinRequest.Bio = ""
		row, created := enqueue(t, f, req)
		if !created {
			t.Fatal("chat_join_request without link not queued")
		}
		rows := list(t, f, row.ID)
		if len(rows) != 1 || !sameRequest(rows[0].ChatJoinRequest, req.ChatJoinRequest) {
			t.Fatalf("rows = %+v, want the link-less request", rows)
		}
	})

	// 申请被拒后可以再申请：没有去重键，每次都是新的一条。
	t.Run("a repeated request queues again", func(t *testing.T) {
		f := newFixture(t)
		req := BotAPIJoinRequestUpdateRequest(f)
		first, created := enqueue(t, f, req)
		if !created {
			t.Fatal("first chat_join_request not queued")
		}
		second, created := enqueue(t, f, req)
		if !created || second.ID == first.ID {
			t.Fatalf("repeated request = %+v created=%v, want a new row", second, created)
		}
	})

	t.Run("allowed_updates decides delivery", func(t *testing.T) {
		f := newFixture(t)
		req := BotAPIJoinRequestUpdateRequest(f)
		// 默认集合包含 chat_join_request；显式清空也回到默认集合。
		if _, created := enqueue(t, f, req); !created {
			t.Fatal("default chat_join_request not queued")
		}
		if err := f.Store.SetBotAPIAllowedUpdates(ctx, f.BotID, []domain.BotAPIUpdateKind{domain.BotAPIUpdateMessage}); err != nil {
			t.Fatalf("set allowed updates: %v", err)
		}
		if row, created := enqueue(t, f, req); created || row.ID != 0 {
			t.Fatalf("chat_join_request outside allowed_updates = %+v created=%v, want dropped", row, created)
		}
		if err := f.Store.SetBotAPIAllowedUpdates(ctx, f.BotID, []domain.BotAPIUpdateKind{domain.BotAPIUpdateChatJoinRequest}); err != nil {
			t.Fatalf("set allowed updates: %v", err)
		}
		if _, created := enqueue(t, f, req); !created {
			t.Fatal("listed chat_join_request not queued")
		}
		if err := f.Store.SetBotAPIAllowedUpdates(ctx, f.BotID, nil); err != nil {
			t.Fatalf("reset allowed updates: %v", err)
		}
		if _, created := enqueue(t, f, req); !created {
			t.Fatal("chat_join_request after reset to the default set not queued")
		}
	})

	t.Run("malformed requests are rejected", func(t *testing.T) {
		f := newFixture(t)
		cases := map[string]func(*domain.EnqueueBotAPIUpdateRequest){
			"missing payload":     func(r *domain.EnqueueBotAPIUpdateRequest) { r.ChatJoinRequest = nil },
			"payload on message":  func(r *domain.EnqueueBotAPIUpdateRequest) { r.Kind = domain.BotAPIUpdateMessage },
			"chat differs":        func(r *domain.EnqueueBotAPIUpdateRequest) { r.Peer.ID++ },
			"bot applies to self": func(r *domain.EnqueueBotAPIUpdateRequest) { r.ChatJoinRequest.UserID = r.BotUserID },
			"link of another chat": func(r *domain.EnqueueBotAPIUpdateRequest) {
				r.ChatJoinRequest.Invite.ChannelID++
			},
		}
		for name, mutate := range cases {
			req := BotAPIJoinRequestUpdateRequest(f)
			mutate(&req)
			if _, created, err := f.Store.EnqueueBotAPIUpdate(ctx, req); err == nil || created {
				t.Fatalf("%s: created=%v err=%v, want rejection", name, created, err)
			}
		}
	})
}
