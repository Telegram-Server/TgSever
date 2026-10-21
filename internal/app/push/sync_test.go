package push

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"telesrv/internal/domain"
)

func decryptSyncPayload(t *testing.T, secret []byte, n Notification) (outer, inner map[string]any) {
	t.Helper()
	outer = decodeJSONObject(t, n.Payload)
	p, _ := outer["p"].(string)
	if p == "" {
		t.Fatalf("payload %s: want encrypted p", n.Payload)
	}
	plain, _, err := nseDecrypt(secret, p)
	if err != nil {
		t.Fatalf("decrypt p: %v", err)
	}
	return outer, decodeJSONObject(t, plain)
}

func assertSyncEnvelope(t *testing.T, n Notification, outer map[string]any, wantCollapse string) {
	t.Helper()
	if n.Type != PushTypeAlert || n.Priority != PriorityLow || n.CollapseID != wantCollapse {
		t.Fatalf("notification type=%v priority=%d collapse=%q, want alert/5/%q", n.Type, n.Priority, n.CollapseID, wantCollapse)
	}
	aps, _ := outer["aps"].(map[string]any)
	if aps["mutable-content"] != float64(1) {
		t.Fatalf("outer aps = %v, want mutable-content 1 so the NSE runs", aps)
	}
	if alert, _ := aps["alert"].(map[string]any); alert["body"] != "You have a new message" {
		t.Fatalf("outer alert = %v, want the generic text", aps["alert"])
	}
	if _, ok := aps["sound"]; ok {
		t.Fatalf("outer aps = %v, sync pushes must not play a sound", aps)
	}
}

func TestBuildSyncNotificationReadHistoryPrivate(t *testing.T) {
	secret := testSecret()
	n, err := buildSyncNotification(SyncPush{Key: KeyReadHistory, Chat: Chat{Kind: ChatPrivate, ID: 1000042}, MaxID: 77}, secret, "You have a new message")
	if err != nil {
		t.Fatal(err)
	}
	outer, inner := decryptSyncPayload(t, secret, n)
	assertSyncEnvelope(t, n, outer, "read_u1000042")
	if len(inner) != 3 || inner["loc-key"] != "READ_HISTORY" || inner["from_id"] != "1000042" || inner["max_id"] != "77" {
		t.Fatalf("inner = %v, want exactly loc-key READ_HISTORY, string from_id and max_id", inner)
	}
}

func TestBuildSyncNotificationReadHistoryChannel(t *testing.T) {
	secret := testSecret()
	n, err := buildSyncNotification(SyncPush{Key: KeyReadHistory, Chat: Chat{Kind: ChatGroup, ID: 555}, MaxID: 9}, secret, "You have a new message")
	if err != nil {
		t.Fatal(err)
	}
	outer, inner := decryptSyncPayload(t, secret, n)
	assertSyncEnvelope(t, n, outer, "read_c555")
	if len(inner) != 3 || inner["loc-key"] != "READ_HISTORY" || inner["channel_id"] != "555" || inner["max_id"] != "9" {
		t.Fatalf("inner = %v, want loc-key READ_HISTORY, channel_id and max_id", inner)
	}
}

func TestBuildSyncNotificationMessageDeleted(t *testing.T) {
	secret := testSecret()
	n, err := buildSyncNotification(SyncPush{Key: KeyMessageDeleted, Chat: Chat{Kind: ChatPrivate, ID: 1000042}, MessageIDs: []int{3, 1, 2}}, secret, "You have a new message")
	if err != nil {
		t.Fatal(err)
	}
	outer, inner := decryptSyncPayload(t, secret, n)
	assertSyncEnvelope(t, n, outer, "del_u1000042_1_3")
	// NSE：messages 是逗号分隔、无空格的字符串；没有 aps（处理后不展示）。
	if len(inner) != 3 || inner["loc-key"] != "MESSAGE_DELETED" || inner["from_id"] != "1000042" || inner["messages"] != "1,2,3" {
		t.Fatalf("inner = %v, want loc-key MESSAGE_DELETED, from_id and messages \"1,2,3\"", inner)
	}
}

func TestBuildSyncNotificationRequiresSecret(t *testing.T) {
	// 没有 secret 时 NSE 无法处理（它只认 p），外层通用文案会被直接展示成一条假通知。
	if _, err := buildSyncNotification(SyncPush{Key: KeyReadHistory, Chat: Chat{ID: 1}, MaxID: 1}, nil, "x"); err == nil {
		t.Fatal("built a sync push without a secret, want an error")
	}
}

func TestBuildSyncNotificationRejectsMissingPeerOrIDs(t *testing.T) {
	secret := testSecret()
	cases := []SyncPush{
		{Key: KeyReadHistory, MaxID: 5},
		{Key: KeyReadHistory, Chat: Chat{ID: 1}},
		{Key: KeyMessageDeleted, MessageIDs: []int{1}},
		{Key: KeyMessageDeleted, Chat: Chat{ID: 1}},
		{Key: KeyMessageText, Chat: Chat{ID: 1}, MaxID: 1},
	}
	for _, c := range cases {
		if _, err := buildSyncNotification(c, secret, "x"); err == nil {
			t.Fatalf("built %+v, want an error", c)
		}
	}
}

func TestBuildSyncNotificationLeaksNothingOutside(t *testing.T) {
	n, err := buildSyncNotification(SyncPush{Key: KeyMessageDeleted, Chat: Chat{ID: 1000042}, MessageIDs: []int{424242}}, testSecret(), "You have a new message")
	if err != nil {
		t.Fatal(err)
	}
	if s := string(n.Payload); strings.Contains(s, "424242") || strings.Contains(s, "MESSAGE_DELETED") {
		t.Fatalf("outer payload %s leaks the sync content", s)
	}
}

type muteAllPolicy struct{ calls int }

func (p *muteAllPolicy) Decide(context.Context, int64, Chat) (NotifyDecision, error) {
	p.calls++
	return NotifyDecision{Mute: true, HidePreview: true}, nil
}

func TestReadHistoryPushesOnlyDevicesNotInForegroundEvenWhenMuted(t *testing.T) {
	f := newServiceFixture(t, Config{})
	policy := &muteAllPolicy{}
	f.svc.policy = policy
	secret := testSecret()
	f.register([8]byte{1}, bobID, domain.PushTokenAPNs, "tok-foreground", secret, presenceForeground)
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-background", secret, presenceBackground)
	f.register([8]byte{3}, bobID, domain.PushTokenAPNs, "tok-offline", secret, presenceNone)
	// 没有 secret 的登记无法静默处理同步推送，不发。
	f.register([8]byte{4}, bobID, domain.PushTokenAPNs, "tok-plain", nil, presenceNone)
	f.register([8]byte{5}, bobID, domain.PushTokenVoIP, "tok-voip", secret, presenceNone)
	f.register([8]byte{6}, aliceID, domain.PushTokenAPNs, "tok-alice", secret, presenceNone)
	f.start()
	if !f.svc.ReadHistory(ReadHistory{UserID: bobID, Chat: Chat{Kind: ChatPrivate, ID: aliceID}, MaxID: 77}) {
		t.Fatal("ReadHistory rejected, want queued")
	}
	f.stop()

	if got := f.sender.tokens(); !slices.Equal(got, []string{"tok-background", "tok-offline"}) {
		t.Fatalf("pushed tokens = %v, want the two devices not in the foreground", got)
	}
	if policy.calls != 0 {
		t.Fatalf("notify policy consulted %d times; sync pushes clear notifications and ignore mute", policy.calls)
	}
	for _, n := range f.sender.sent {
		_, inner := decryptSyncPayload(t, secret, n)
		if inner["loc-key"] != "READ_HISTORY" || inner["from_id"] != "1000001" || inner["max_id"] != "77" || n.Priority != PriorityLow {
			t.Fatalf("push to %s = %v (priority %d), want READ_HISTORY for Alice's chat", n.Token, inner, n.Priority)
		}
	}
}

func TestDeleteMessagesPushesNewestIDsInBatches(t *testing.T) {
	f := newServiceFixture(t, Config{})
	f.svc.policy = &muteAllPolicy{}
	secret := testSecret()
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", secret, presenceBackground)
	f.register([8]byte{1}, bobID, domain.PushTokenAPNs, "tok-foreground", secret, presenceForeground)
	f.start()
	ids := make([]int, 0, 450)
	for id := 450; id >= 1; id-- {
		ids = append(ids, id)
	}
	if !f.svc.DeleteMessages(DeletedMessages{UserID: bobID, Chat: Chat{Kind: ChatPrivate, ID: aliceID}, MessageIDs: ids}) {
		t.Fatal("DeleteMessages rejected, want queued")
	}
	f.stop()

	// 每条最多 100 个 id，最多 3 条，只保留最新的 300 个（通知栏里只会有最近的消息）。
	if len(f.sender.sent) != 3 {
		t.Fatalf("sent %d pushes, want 3 batches", len(f.sender.sent))
	}
	var batches []string
	for _, n := range f.sender.sent {
		if n.Token != "tok-bob" {
			t.Fatalf("pushed to %s, want only the background device", n.Token)
		}
		_, inner := decryptSyncPayload(t, secret, n)
		if inner["loc-key"] != "MESSAGE_DELETED" || inner["from_id"] != "1000001" {
			t.Fatalf("inner = %v", inner)
		}
		msgs, _ := inner["messages"].(string)
		parts := strings.Split(msgs, ",")
		batches = append(batches, parts[0]+"-"+parts[len(parts)-1]+"/"+strconv.Itoa(len(parts)))
	}
	slices.Sort(batches)
	if want := []string{"151-250/100", "251-350/100", "351-450/100"}; !slices.Equal(batches, want) {
		t.Fatalf("batches = %v, want %v", batches, want)
	}
}

func TestSyncPushesIgnoreEmptyInput(t *testing.T) {
	f := newServiceFixture(t, Config{})
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", testSecret(), presenceNone)
	f.start()
	if f.svc.ReadHistory(ReadHistory{UserID: bobID, Chat: Chat{ID: aliceID}}) {
		t.Fatal("queued a read push without max_id")
	}
	if f.svc.DeleteMessages(DeletedMessages{UserID: bobID, MessageIDs: []int{1}}) {
		t.Fatal("queued a delete push without a peer")
	}
	f.stop()
	if got := f.sender.tokens(); len(got) != 0 {
		t.Fatalf("pushed %v, want none", got)
	}
}
