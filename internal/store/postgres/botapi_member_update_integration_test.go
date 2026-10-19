package postgres

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestBotAPIChatMemberUpdatesRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	bot, err := users.Create(ctx, domain.User{AccessHash: 951, Phone: "+1951" + suffix + "01", FirstName: "MemberQueueBot"})
	if err != nil {
		t.Fatalf("create bot user: %v", err)
	}
	other, err := users.Create(ctx, domain.User{AccessHash: 952, Phone: "+1952" + suffix + "01", FirstName: "MemberQueueUser"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO bots (bot_user_id, owner_user_id, token_secret)
VALUES ($1, $1, 'member-queue-secret')`, bot.ID); err != nil {
		t.Fatalf("seed bot: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM bot_api_updates WHERE bot_user_id = $1", bot.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM bot_api_update_states WHERE bot_user_id = $1", bot.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM bots WHERE bot_user_id = $1", bot.ID)
	})

	now := int(time.Now().Unix())
	chat := domain.Peer{Type: domain.PeerTypeChannel, ID: 880000 + bot.ID}
	request := func(kind domain.BotAPIUpdateKind, subject int64) domain.EnqueueBotAPIUpdateRequest {
		return domain.EnqueueBotAPIUpdateRequest{
			BotUserID: bot.ID, Kind: kind, Peer: chat, Date: now,
			ChatMember: &domain.BotChatMemberUpdated{
				Chat: chat, ActorUserID: other.ID, Date: now, ViaJoinRequest: true,
				Old: domain.BotChatMemberSnapshot{UserID: subject, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberActive},
				New: domain.BotChatMemberSnapshot{
					UserID: subject, Role: domain.ChannelRoleAdmin, Status: domain.ChannelMemberActive, Rank: "mod",
					AdminRights: domain.ChannelAdminRights{BanUsers: true, PinMessages: true},
				},
			},
		}
	}
	store := NewBotAPIUpdateStore(pool)

	my := request(domain.BotAPIUpdateMyChatMember, bot.ID)
	first, created, err := store.EnqueueBotAPIUpdate(ctx, my)
	if err != nil || !created {
		t.Fatalf("enqueue my_chat_member = %+v created=%v err=%v", first, created, err)
	}
	if _, created, err := store.EnqueueBotAPIUpdate(ctx, my); err != nil || !created {
		t.Fatalf("repeated change must queue again: created=%v err=%v", created, err)
	}
	// chat_member 默认不投递，必须显式订阅。
	if row, created, err := store.EnqueueBotAPIUpdate(ctx, request(domain.BotAPIUpdateChatMember, other.ID)); err != nil || created || row.ID != 0 {
		t.Fatalf("default chat_member = %+v created=%v err=%v", row, created, err)
	}
	if err := store.SetBotAPIAllowedUpdates(ctx, bot.ID, []domain.BotAPIUpdateKind{domain.BotAPIUpdateChatMember, domain.BotAPIUpdateMyChatMember}); err != nil {
		t.Fatalf("set allowed updates: %v", err)
	}
	if _, created, err := store.EnqueueBotAPIUpdate(ctx, request(domain.BotAPIUpdateChatMember, other.ID)); err != nil || !created {
		t.Fatalf("opted-in chat_member created=%v err=%v", created, err)
	}
	if _, _, err := store.EnqueueBotAPIUpdate(ctx, domain.EnqueueBotAPIUpdateRequest{
		BotUserID: bot.ID, Kind: domain.BotAPIUpdateChatMember, Peer: chat, Date: now,
	}); err == nil {
		t.Fatal("chat_member without payload must be rejected")
	}

	items, err := store.ListBotAPIUpdates(ctx, bot.ID, first.ID, 100)
	if err != nil || len(items) != 3 {
		t.Fatalf("list = %+v err=%v", items, err)
	}
	got := items[0]
	if got.Kind != domain.BotAPIUpdateMyChatMember || got.Peer != chat || got.MessageID != 0 ||
		got.ChatMember == nil || *got.ChatMember != *my.ChatMember {
		t.Fatalf("my_chat_member round trip = %+v (payload %+v), want %+v", got, got.ChatMember, my.ChatMember)
	}
	if items[2].Kind != domain.BotAPIUpdateChatMember || items[2].ChatMember == nil || items[2].ChatMember.New.UserID != other.ID {
		t.Fatalf("chat_member round trip = %+v", items[2])
	}
	tail, err := store.ListTailBotAPIUpdates(ctx, bot.ID, 1, 100)
	if err != nil || len(tail) != 1 || tail[0].ChatMember == nil {
		t.Fatalf("tail = %+v err=%v", tail, err)
	}

	// 表约束也要守住：成员类行必须有载荷，且载荷的 chat 与 peer 一致。
	if _, err := pool.Exec(ctx, `
INSERT INTO bot_api_updates (bot_user_id, update_kind, peer_type, peer_id, message_id, date)
VALUES ($1, 'my_chat_member', 'channel', $2, 0, $3)`, bot.ID, chat.ID, now); err == nil {
		t.Fatal("a member row without member_payload passed the table constraints")
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO bot_api_updates (bot_user_id, update_kind, peer_type, peer_id, message_id, date, member_payload)
VALUES ($1, 'message', 'channel', $2, 5, $3, '{}'::jsonb)`, bot.ID, chat.ID, now); err == nil {
		t.Fatal("a message row with member_payload passed the table constraints")
	}
}
