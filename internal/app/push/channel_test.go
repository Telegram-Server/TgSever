package push

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// fakeMembers 是按 user_id 升序分页的成员列表，记录每次分页请求。
type fakeMembers struct {
	mu      sync.Mutex
	members map[int64][]int64
	calls   []memberPageCall
}

type memberPageCall struct {
	channelID, after int64
	limit            int
}

func (f *fakeMembers) ListActiveChannelMemberIDsAfter(_ context.Context, channelID, afterUserID int64, limit int) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, memberPageCall{channelID, afterUserID, limit})
	ids := slices.Clone(f.members[channelID])
	slices.Sort(ids)
	out := []int64{}
	for _, id := range ids {
		if id > afterUserID && len(out) < limit {
			out = append(out, id)
		}
	}
	return out, nil
}

func (m *countingMetrics) droppedCount(kind string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dropped[kind]
}

const (
	groupID   = int64(5000001)
	channelID = int64(5000002)
	carolID   = int64(1000003)
	daveID    = int64(1000004)
	erinID    = int64(1000005)
)

type channelFixture struct {
	*serviceFixture
	members  *fakeMembers
	settings *memory.PasswordStore
	metrics  *countingMetrics
}

func newChannelFixture(t *testing.T, cfg Config) *channelFixture {
	t.Helper()
	base := newServiceFixture(t, cfg)
	f := &channelFixture{
		serviceFixture: base,
		members:        &fakeMembers{members: map[int64][]int64{}},
		settings:       memory.NewPasswordStore(),
		metrics:        &countingMetrics{dropped: map[string]int{}},
	}
	base.svc = NewService(cfg, Deps{
		Devices:  base.devices,
		Presence: base.presence,
		Users:    fakeUsers{aliceID: {ID: aliceID, FirstName: "Alice", LastName: "A"}, bobID: {ID: bobID, FirstName: "Bob"}},
		Sender:   base.sender,
		Strings:  testLangPack(),
		Policy:   NewSettingsPolicy(f.settings, func() time.Time { return time.Unix(1_800_000_000, 0) }),
		Members:  f.members,
		Metrics:  f.metrics,
	}, zaptest.NewLogger(t))
	return f
}

func (f *channelFixture) mute(owner int64, scope domain.NotifyScope) {
	f.t.Helper()
	forever := int(^uint32(0) >> 1)
	if err := f.settings.SaveNotifySettings(context.Background(), owner, scope, domain.PeerNotifySettings{MuteUntil: &forever}); err != nil {
		f.t.Fatal(err)
	}
}

func groupTextFromAlice(text string) NewMessage {
	return NewMessage{
		Chat:      Chat{Kind: ChatGroup, ID: groupID},
		ChatTitle: "Team",
		SenderID:  aliceID,
		MessageID: 9,
		Text:      text,
	}
}

func TestChannelMessagePushesMembersNotInForegroundNotMutedNotSender(t *testing.T) {
	f := newChannelFixture(t, Config{})
	f.members.members[groupID] = []int64{aliceID, bobID, carolID, daveID, erinID}
	f.register([8]byte{1}, aliceID, domain.PushTokenAPNs, "tok-alice", nil, presenceNone)            // 发送者
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", testSecret(), presenceBackground) // 有连接但已报后台
	f.register([8]byte{3}, carolID, domain.PushTokenAPNs, "tok-carol", nil, presenceForeground)      // 前台
	f.register([8]byte{4}, daveID, domain.PushTokenAPNs, "tok-dave", nil, presenceNone)              // 离线
	f.register([8]byte{5}, erinID, domain.PushTokenAPNs, "tok-erin", nil, presenceNone)              // 静音了这个群
	f.mute(erinID, domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: groupID}})
	f.start()
	if !f.svc.ChannelMessage(groupTextFromAlice("hi team")) {
		t.Fatal("ChannelMessage rejected, want queued")
	}
	f.stop()

	if got, want := f.sender.tokens(), []string{"tok-bob", "tok-dave"}; !slices.Equal(got, want) {
		t.Fatalf("pushed tokens = %v, want %v", got, want)
	}
	var bob Notification
	for _, n := range f.sender.sent {
		if n.Token == "tok-bob" {
			bob = n
		}
	}
	inner := decryptInner(t, testSecret(), bob)
	alert := inner["aps"].(map[string]any)["alert"].(map[string]any)
	if inner["channel_id"] != "5000001" || inner["chat_from_id"] != "1000001" || inner["msg_id"] != "9" ||
		alert["title"] != "Team" || alert["body"] != "Alice A: hi team" {
		t.Fatalf("decrypted payload = %v, want the rendered group message from Alice", inner)
	}
}

// 群组与频道各自的默认范围：频道默认静音不影响群组，反之亦然。
func TestChannelMessageHonoursPerKindDefaultScope(t *testing.T) {
	f := newChannelFixture(t, Config{})
	f.members.members[groupID] = []int64{aliceID, bobID}
	f.members.members[channelID] = []int64{aliceID, bobID}
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", nil, presenceNone)
	f.mute(bobID, domain.NotifyScope{Kind: domain.NotifyScopeBroadcasts})
	f.start()
	f.svc.ChannelMessage(NewMessage{Chat: Chat{Kind: ChatChannel, ID: channelID}, ChatTitle: "News", SenderID: aliceID, MessageID: 1, Text: "post"})
	f.svc.ChannelMessage(groupTextFromAlice("hi"))
	f.stop()
	if len(f.sender.sent) != 1 {
		t.Fatalf("sent %d pushes, want only the group one", len(f.sender.sent))
	}
	outer := decodeJSONObject(t, f.sender.sent[0].Payload)
	if outer["channel_id"] != "5000001" {
		t.Fatalf("pushed %v, want the group message (channels muted by default scope)", outer)
	}
}

func TestChannelMessageChannelPayloadShowsChannelTitle(t *testing.T) {
	f := newChannelFixture(t, Config{})
	f.members.members[channelID] = []int64{aliceID, bobID}
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", nil, presenceNone)
	f.start()
	f.svc.ChannelMessage(NewMessage{Chat: Chat{Kind: ChatChannel, ID: channelID}, ChatTitle: "News", SenderID: aliceID, MessageID: 4, Text: "breaking"})
	f.stop()
	if len(f.sender.sent) != 1 {
		t.Fatalf("sent %d pushes, want 1", len(f.sender.sent))
	}
	outer := decodeJSONObject(t, f.sender.sent[0].Payload)
	alert := outer["aps"].(map[string]any)["alert"].(map[string]any)
	if outer["channel_id"] != "5000002" || outer["msg_id"] != "4" || alert["title"] != "News" || alert["body"] != "breaking" {
		t.Fatalf("payload = %v, want the channel post titled by the channel", outer)
	}
	if _, ok := outer["chat_from_id"]; ok {
		t.Fatalf("payload = %v, channel post must not name the sender", outer)
	}
}

// 匿名管理员（或以频道身份发言）：文案用 AuthorName，不暴露真实发送者。
func TestChannelMessageAnonymousAuthorHidesSender(t *testing.T) {
	f := newChannelFixture(t, Config{})
	f.members.members[groupID] = []int64{aliceID, bobID}
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", nil, presenceNone)
	f.start()
	msg := groupTextFromAlice("from the admins")
	msg.AuthorName = "Team"
	f.svc.ChannelMessage(msg)
	f.stop()
	if len(f.sender.sent) != 1 {
		t.Fatalf("sent %d pushes, want 1", len(f.sender.sent))
	}
	outer := decodeJSONObject(t, f.sender.sent[0].Payload)
	alert := outer["aps"].(map[string]any)["alert"].(map[string]any)
	if alert["body"] != "Team: from the admins" {
		t.Fatalf("alert = %v, want the anonymous author name", alert)
	}
	if _, ok := outer["chat_from_id"]; ok {
		t.Fatalf("payload = %v, anonymous post must not carry chat_from_id", outer)
	}
}

// 成员分批读取：每批 MemberBatch 个，按 user_id keyset 往后翻，所有批次都推送。
func TestChannelMessagePagesMembersInBatches(t *testing.T) {
	f := newChannelFixture(t, Config{MemberBatch: 2})
	var ids []int64
	for i := range 7 {
		id := int64(2000000 + i)
		ids = append(ids, id)
		f.register([8]byte{byte(10 + i)}, id, domain.PushTokenAPNs, "tok-"+string(rune('a'+i)), nil, presenceNone)
	}
	f.members.members[groupID] = ids
	f.start()
	f.svc.ChannelMessage(groupTextFromAlice("hi"))
	// 关停时不再翻页：等所有批次发完再停。
	waitUntil(t, func() bool { return f.sentCount() == 7 })
	f.stop()

	if got := len(f.sender.sent); got != 7 {
		t.Fatalf("sent %d pushes, want one per member (7)", got)
	}
	f.members.mu.Lock()
	defer f.members.mu.Unlock()
	wantAfter := []int64{0, 2000001, 2000003, 2000005}
	if len(f.members.calls) != len(wantAfter) {
		t.Fatalf("member pages = %+v, want %d pages of 2", f.members.calls, len(wantAfter))
	}
	for i, c := range f.members.calls {
		if c.channelID != groupID || c.limit != 2 || c.after != wantAfter[i] {
			t.Fatalf("member page %d = %+v, want channel %d after %d limit 2", i, c, groupID, wantAfter[i])
		}
	}
}

// 调用方只入队：成员很多、发送器卡住时也立即返回；队列满时丢弃并计数。
func TestChannelMessageDoesNotBlockAndCountsDrops(t *testing.T) {
	f := newChannelFixture(t, Config{QueueSize: 1, Workers: 1, MemberBatch: 1, SendTimeout: time.Hour})
	f.sender.block = make(chan struct{})
	var ids []int64
	for i := range 50 {
		id := int64(3000000 + i)
		ids = append(ids, id)
		f.register([8]byte{byte(20 + i)}, id, domain.PushTokenAPNs, "tok", nil, presenceNone)
	}
	f.members.members[groupID] = ids
	f.start()

	done := make(chan int)
	go func() {
		accepted := 0
		for range 20 {
			if f.svc.ChannelMessage(groupTextFromAlice("x")) {
				accepted++
			}
		}
		done <- accepted
	}()
	select {
	case accepted := <-done:
		if accepted >= 20 {
			t.Fatalf("accepted = %d, want the bounded queue to drop the overflow", accepted)
		}
		if dropped := f.metrics.droppedCount("channel"); dropped == 0 {
			t.Fatalf("dropped = %d, want the overflow counted", dropped)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ChannelMessage blocked behind a stuck sender")
	}
	close(f.sender.block)
}
