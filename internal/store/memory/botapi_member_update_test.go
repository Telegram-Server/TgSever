package memory

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

func botAPIMemberRequest(botID int64, kind domain.BotAPIUpdateKind, subjectID int64) domain.EnqueueBotAPIUpdateRequest {
	chat := domain.Peer{Type: domain.PeerTypeChannel, ID: 7001}
	return domain.EnqueueBotAPIUpdateRequest{
		BotUserID: botID,
		Kind:      kind,
		Peer:      chat,
		Date:      1700000100,
		ChatMember: &domain.BotChatMemberUpdated{
			Chat:        chat,
			ActorUserID: 3001,
			Date:        1700000100,
			Old:         domain.BotChatMemberSnapshot{UserID: subjectID, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberLeft},
			New:         domain.BotChatMemberSnapshot{UserID: subjectID, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberActive},
		},
	}
}

func TestBotAPIMyChatMemberRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := NewBotAPIUpdateStore()
	req := botAPIMemberRequest(1001, domain.BotAPIUpdateMyChatMember, 1001)
	row, created, err := store.EnqueueBotAPIUpdate(ctx, req)
	if err != nil || !created {
		t.Fatalf("enqueue my_chat_member = %#v created=%v err=%v", row, created, err)
	}
	// 成员更新没有消息去重键：同一变化再来一次也是新的一条。
	if _, created, err := store.EnqueueBotAPIUpdate(ctx, req); err != nil || !created {
		t.Fatalf("second my_chat_member created=%v err=%v", created, err)
	}
	rows, err := store.ListBotAPIUpdates(ctx, 1001, 1, 100)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows = %#v err=%v", rows, err)
	}
	got := rows[0].ChatMember
	if got == nil || *got != *req.ChatMember || rows[0].Peer != req.Peer || rows[0].Kind != domain.BotAPIUpdateMyChatMember {
		t.Fatalf("round trip = %#v, want %#v", rows[0], req)
	}
	got.New.Rank = "mutated"
	again, _ := store.ListBotAPIUpdates(ctx, 1001, 1, 1)
	if again[0].ChatMember.New.Rank != "" {
		t.Fatal("ListBotAPIUpdates must return a copy of the member payload")
	}
}

func TestBotAPIChatMemberRequiresExplicitOptIn(t *testing.T) {
	ctx := context.Background()
	store := NewBotAPIUpdateStore()
	req := botAPIMemberRequest(1001, domain.BotAPIUpdateChatMember, 2002)
	// 官方默认（allowed_updates 未设或为空）不投递 chat_member。
	if row, created, err := store.EnqueueBotAPIUpdate(ctx, req); err != nil || created || row.ID != 0 {
		t.Fatalf("default chat_member = %#v created=%v err=%v", row, created, err)
	}
	if _, created, err := store.EnqueueBotAPIUpdate(ctx, botAPIMemberRequest(1001, domain.BotAPIUpdateMyChatMember, 1001)); err != nil || !created {
		t.Fatalf("default my_chat_member created=%v err=%v", created, err)
	}
	if err := store.SetBotAPIAllowedUpdates(ctx, 1001, []domain.BotAPIUpdateKind{domain.BotAPIUpdateChatMember}); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	if _, created, err := store.EnqueueBotAPIUpdate(ctx, req); err != nil || !created {
		t.Fatalf("opted-in chat_member created=%v err=%v", created, err)
	}
	if _, created, err := store.EnqueueBotAPIUpdate(ctx, botAPIMemberRequest(1001, domain.BotAPIUpdateMyChatMember, 1001)); err != nil || created {
		t.Fatalf("my_chat_member outside allowed_updates created=%v err=%v", created, err)
	}
}

func TestBotAPIMemberUpdateRejectsMalformedPayload(t *testing.T) {
	ctx := context.Background()
	store := NewBotAPIUpdateStore()
	cases := map[string]func(*domain.EnqueueBotAPIUpdateRequest){
		"missing payload":     func(r *domain.EnqueueBotAPIUpdateRequest) { r.ChatMember = nil },
		"message id":          func(r *domain.EnqueueBotAPIUpdateRequest) { r.MessageID = 5 },
		"user peer":           func(r *domain.EnqueueBotAPIUpdateRequest) { r.Peer = domain.Peer{Type: domain.PeerTypeUser, ID: 9} },
		"chat mismatch":       func(r *domain.EnqueueBotAPIUpdateRequest) { r.ChatMember.Chat.ID++ },
		"other subject":       func(r *domain.EnqueueBotAPIUpdateRequest) { r.ChatMember.New.UserID = 2002 },
		"unchanged":           func(r *domain.EnqueueBotAPIUpdateRequest) { r.ChatMember.Old = r.ChatMember.New },
		"payload on messages": func(r *domain.EnqueueBotAPIUpdateRequest) { r.Kind = domain.BotAPIUpdateMessage },
	}
	for name, mutate := range cases {
		req := botAPIMemberRequest(1001, domain.BotAPIUpdateMyChatMember, 1001)
		mutate(&req)
		if _, created, err := store.EnqueueBotAPIUpdate(ctx, req); err == nil || created {
			t.Fatalf("%s: created=%v err=%v, want rejection", name, created, err)
		}
	}
}
