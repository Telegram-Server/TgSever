package postgres

import (
	"context"
	"crypto/rand"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestMessageDeletionsPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	sender, err := users.Create(ctx, domain.User{AccessHash: 1201, Phone: "+178" + suffix + "01", FirstName: "DeletionSender"})
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := users.Create(ctx, domain.User{AccessHash: 1202, Phone: "+178" + suffix + "02", FirstName: "DeletionRecipient"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM message_deletions WHERE owner_user_id=ANY($1::bigint[])`, []int64{sender.ID, recipient.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=ANY($1::bigint[])`, []int64{sender.ID, recipient.ID})
	})
	storetest.MessageDeletions(t, NewMessageStore(pool), NewMessageDeletionStore(pool), sender.ID, recipient.ID)
}

func TestChannelMessageDeletionsPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	owner, err := users.Create(ctx, domain.User{AccessHash: 1205, Phone: "+178" + suffix + "05", FirstName: "ChannelDeletionOwner"})
	if err != nil {
		t.Fatal(err)
	}
	member, err := users.Create(ctx, domain.User{AccessHash: 1206, Phone: "+178" + suffix + "06", FirstName: "ChannelDeletionMember"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM message_deletions WHERE peer_type='channel' AND peer_id IN (SELECT id FROM channels WHERE creator_user_id=$1)`, owner.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM channels WHERE creator_user_id=$1`, owner.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=ANY($1::bigint[])`, []int64{owner.ID, member.ID})
	})
	storetest.ChannelMessageDeletions(t, NewChannelStore(pool), NewMessageDeletionStore(pool), owner.ID, member.ID)
}

func TestModerationChannelDeletionRecordsTheModeratorPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	owner, err := users.Create(ctx, domain.User{AccessHash: 1207, Phone: "+178" + suffix + "07", FirstName: "ModeratedOwner"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM message_deletions WHERE peer_type='channel' AND peer_id IN (SELECT id FROM channels WHERE creator_user_id=$1)`, owner.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM channels WHERE creator_user_id=$1`, owner.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, owner.ID)
	})
	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner.ID, Title: "Moderated", Megagroup: true, Date: 1_700_130_000})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: owner.ID, ChannelID: created.Channel.ID, RandomID: 1_207_001, Message: "reported", Date: 1_700_130_001,
	})
	if err != nil {
		t.Fatalf("SendChannelMessage: %v", err)
	}

	audit := domain.ModerationDeletionAudit("reviewer", "delete-channel-message:007", "moderation case 7 decision 9")
	if _, err := channels.ModerationDeleteChannelMessages(ctx, created.Channel.ID, []int{sent.Message.ID}, 1_700_130_002, audit); err != nil {
		t.Fatalf("ModerationDeleteChannelMessages: %v", err)
	}

	got, err := NewMessageDeletionStore(pool).ListMessageDeletions(ctx, domain.MessageDeletionFilter{SenderUserID: owner.ID, Limit: 10})
	if err != nil {
		t.Fatalf("ListMessageDeletions: %v", err)
	}
	if len(got) != 1 || got[0].MessageID != sent.Message.ID || got[0].Source != domain.DeletionSourceModeration ||
		got[0].ActorType != domain.DeletionActorOperator || got[0].ActorUserID != 0 || got[0].OperatorID != "reviewer" ||
		got[0].CommandID != "delete-channel-message:007" || got[0].Reason != "moderation case 7 decision 9" {
		t.Fatalf("deletions = %+v, want moderator reviewer for case 7", got)
	}
}

func TestMessageDeletionKeepsDeviceAfterLogoutPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	sender, err := users.Create(ctx, domain.User{AccessHash: 1203, Phone: "+178" + suffix + "03", FirstName: "DeviceSender"})
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := users.Create(ctx, domain.User{AccessHash: 1204, Phone: "+178" + suffix + "04", FirstName: "DeviceRecipient"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM message_deletions WHERE owner_user_id=ANY($1::bigint[])`, []int64{sender.ID, recipient.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=ANY($1::bigint[])`, []int64{sender.ID, recipient.ID})
	})
	var authKeyID [8]byte
	var authKey [256]byte
	if _, err := rand.Read(authKeyID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(authKey[:]); err != nil {
		t.Fatal(err)
	}
	if err := NewAuthKeyStore(pool).Save(ctx, store.AuthKeyData{ID: authKeyID, Value: authKey}); err != nil {
		t.Fatalf("save auth key: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM auth_keys WHERE auth_key_id = $1`, authKeyIDToInt64(authKeyID))
	})
	authorizations := NewAuthorizationStore(pool)
	if err := authorizations.Bind(ctx, domain.Authorization{
		AuthKeyID:   authKeyID,
		UserID:      sender.ID,
		Hash:        1,
		DeviceModel: "Pixel 9",
		Platform:    "android",
		IP:          "203.0.113.7",
	}); err != nil {
		t.Fatalf("bind authorization: %v", err)
	}

	messages := NewMessageStore(pool)
	sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    sender.ID,
		RecipientUserID: recipient.ID,
		RandomID:        1_204_001,
		Message:         "from my phone",
		Date:            1_700_120_000,
	})
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}
	if _, err := messages.DeleteMessages(ctx, domain.DeleteMessagesRequest{
		OwnerUserID:     sender.ID,
		IDs:             []int{sent.SenderMessage.ID},
		Date:            1_700_120_001,
		OriginAuthKeyID: authKeyID,
		OriginSessionID: 5,
	}); err != nil {
		t.Fatalf("DeleteMessages: %v", err)
	}
	// 设备退出登录后授权行消失，删除记录里的设备信息必须仍在。
	if err := authorizations.Delete(ctx, authKeyID); err != nil {
		t.Fatalf("delete authorization: %v", err)
	}

	got, err := NewMessageDeletionStore(pool).ListMessageDeletions(ctx, domain.MessageDeletionFilter{ActorUserID: sender.ID, Limit: 10})
	if err != nil {
		t.Fatalf("ListMessageDeletions: %v", err)
	}
	want := domain.MessageDeletionDevice{DeviceModel: "Pixel 9", Platform: "android", IP: "203.0.113.7"}
	if len(got) != 1 || got[0].OriginDevice != want {
		t.Fatalf("deletions = %+v, want one row with device %+v", got, want)
	}
}
