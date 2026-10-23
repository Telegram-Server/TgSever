package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/domain"
)

func thumbs() []domain.MessageReaction {
	return []domain.MessageReaction{{Type: domain.MessageReactionEmoji, Emoticon: "👍"}}
}

// installFailingBotUpdateInsert 让 bot_updates 的写入失败，验证反应与 qts 记录同事务。
func installFailingBotUpdateInsert(t *testing.T, pool *pgxpool.Pool) func() {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
CREATE OR REPLACE FUNCTION fail_bot_reaction_qts() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'injected bot update failure';
END $$;
CREATE TRIGGER fail_bot_reaction_qts BEFORE INSERT ON bot_updates
FOR EACH ROW EXECUTE FUNCTION fail_bot_reaction_qts();`); err != nil {
		t.Fatalf("install failing trigger: %v", err)
	}
	return func() {
		if _, err := pool.Exec(ctx, `DROP TRIGGER fail_bot_reaction_qts ON bot_updates`); err != nil {
			t.Fatalf("drop trigger: %v", err)
		}
	}
}

// 超级群反应（#150）：管理员 bot 的 updateBotMessageReaction 与反应写在同一事务里——qts 记录写入
// 失败时反应也不落库、不消耗 qts；成功时记录带旧反应与新反应。广播频道写计数记录。
func TestChannelReactionBotUpdatesShareTransactionPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	channels := NewChannelStore(pool)
	updates := NewBotUpdateStore(pool)
	owner, human, bot := createBotUpdateTestUser(t, pool, false), createBotUpdateTestUser(t, pool, false), createBotUpdateTestUser(t, pool, true)
	now := int(time.Now().Unix())
	for _, broadcast := range []bool{false, true} {
		created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "votes", Megagroup: !broadcast, Broadcast: broadcast, MemberUserIDs: []int64{human, bot}, Date: now})
		if err != nil {
			t.Fatalf("create channel: %v", err)
		}
		channelID := created.Channel.ID
		if _, err := channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: owner, ChannelID: channelID, MemberID: bot, AdminRights: domain.ChannelAdminRights{DeleteMessages: true, PostMessages: true}, Date: now}); err != nil {
			t.Fatalf("promote bot: %v", err)
		}
		sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: owner, ChannelID: channelID, RandomID: time.Now().UnixNano(), Message: "vote", Date: now})
		if err != nil {
			t.Fatalf("send: %v", err)
		}
		before, err := updates.BotUpdatesState(ctx, bot)
		if err != nil {
			t.Fatal(err)
		}
		req := domain.SetChannelMessageReactionsRequest{UserID: human, ChannelID: channelID, MessageID: sent.Message.ID, Reactions: thumbs(), ReactionsPerUserMax: 1, Date: now}

		restore := installFailingBotUpdateInsert(t, pool)
		if _, err := channels.SetChannelMessageReactions(ctx, req); err == nil {
			t.Fatalf("broadcast=%v: reaction succeeded despite failing bot update insert", broadcast)
		}
		restore()
		var stored int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_message_reactions WHERE channel_id = $1`, channelID).Scan(&stored); err != nil || stored != 0 {
			t.Fatalf("broadcast=%v: reactions after rollback = %d, %v", broadcast, stored, err)
		}
		if st, _ := updates.BotUpdatesState(ctx, bot); st.Qts != before.Qts {
			t.Fatalf("broadcast=%v: qts after rollback = %d, want %d", broadcast, st.Qts, before.Qts)
		}

		res, err := channels.SetChannelMessageReactions(ctx, req)
		if err != nil {
			t.Fatalf("react: %v", err)
		}
		if len(res.BotUpdates) != 1 || res.BotUpdates[0].Qts != before.Qts+1 || res.BotUpdates[0].BotUserID != bot {
			t.Fatalf("broadcast=%v: result bot updates = %+v", broadcast, res.BotUpdates)
		}
		rows, err := updates.ListBotUpdates(ctx, bot, before.Qts, 10)
		if err != nil || len(rows) != 1 {
			t.Fatalf("broadcast=%v: stored bot updates = %+v, %v", broadcast, rows, err)
		}
		if broadcast {
			r := rows[0].MessageReactions
			if r == nil || r.MessageID != sent.Message.ID || len(r.Reactions) != 1 || r.Reactions[0].Count != 1 || r.Reactions[0].Reaction != thumbs()[0] {
				t.Fatalf("count update = %+v", rows[0])
			}
			continue
		}
		r := rows[0].MessageReaction
		if r == nil || r.Actor != (domain.Peer{Type: domain.PeerTypeUser, ID: human}) || len(r.Old) != 0 || len(r.New) != 1 || r.New[0] != thumbs()[0] {
			t.Fatalf("reaction update = %+v", rows[0])
		}
		res, err = channels.SetChannelMessageReactions(ctx, domain.SetChannelMessageReactionsRequest{UserID: human, ChannelID: channelID, MessageID: sent.Message.ID, Date: now})
		if err != nil || len(res.BotUpdates) != 1 {
			t.Fatalf("clear: %+v, %v", res.BotUpdates, err)
		}
		if r := res.BotUpdates[0].MessageReaction; r == nil || len(r.Old) != 1 || r.Old[0] != thumbs()[0] || len(r.New) != 0 {
			t.Fatalf("clear update = %+v", res.BotUpdates[0])
		}
	}
}

// 私聊反应（#150）：bot 的 updateBotMessageReaction 与反应同事务，消息 id 是 bot 自己的 box id。
func TestPrivateReactionBotUpdatesShareTransactionPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	messages := NewMessageStore(pool)
	updates := NewBotUpdateStore(pool)
	human, bot := createBotUpdateTestUser(t, pool, false), createBotUpdateTestUser(t, pool, true)
	now := int(time.Now().Unix())
	sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: bot, RecipientUserID: human, RandomID: time.Now().UnixNano(), Message: "vote", Date: now})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	req := domain.SetPrivateMessageReactionsRequest{UserID: human, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: bot}, MessageID: sent.RecipientMessage.ID, Reactions: thumbs(), ReactionsPerUserMax: 1, Date: now}

	restore := installFailingBotUpdateInsert(t, pool)
	if _, err := messages.SetMessageReactions(ctx, req); err == nil {
		t.Fatal("reaction succeeded despite failing bot update insert")
	}
	restore()
	var stored int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM private_message_reactions WHERE user_id = $1`, human).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("reactions after rollback = %d, %v", stored, err)
	}
	if st, _ := updates.BotUpdatesState(ctx, bot); st.Qts != 0 {
		t.Fatalf("qts after rollback = %d", st.Qts)
	}

	res, err := messages.SetMessageReactions(ctx, req)
	if err != nil {
		t.Fatalf("react: %v", err)
	}
	if len(res.BotUpdates) != 1 || res.BotUpdates[0].Qts != 1 {
		t.Fatalf("result bot updates = %+v", res.BotUpdates)
	}
	rows, err := updates.ListBotUpdates(ctx, bot, 0, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("stored = %+v, %v", rows, err)
	}
	r := rows[0].MessageReaction
	humanPeer := domain.Peer{Type: domain.PeerTypeUser, ID: human}
	if r == nil || r.Peer != humanPeer || r.Actor != humanPeer || r.MessageID != sent.SenderMessage.ID || len(r.Old) != 0 || len(r.New) != 1 || r.New[0] != thumbs()[0] {
		t.Fatalf("private reaction update = %+v", rows[0])
	}
}
