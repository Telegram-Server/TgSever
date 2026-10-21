package push

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// ioCounter 统计扇出过程中的存储 / 语言包调用次数。
type ioCounter struct {
	mu                     sync.Mutex
	authLookups            int
	settingsSingle         int
	settingsBatch          int
	langPackLookups        int
	userLookups            int
	authorizationsDelegate AuthorizationLookup
	usersDelegate          UserLookup
	settingsDelegate       *memory.PasswordStore
	langPackDelegate       LangPackSource
}

func (c *ioCounter) inc(n *int) {
	c.mu.Lock()
	*n++
	c.mu.Unlock()
}

type countingAuths struct{ c *ioCounter }

func (a countingAuths) ByAuthKey(ctx context.Context, id [8]byte) (domain.Authorization, bool, error) {
	a.c.inc(&a.c.authLookups)
	return a.c.authorizationsDelegate.ByAuthKey(ctx, id)
}

type countingSettings struct{ c *ioCounter }

func (s countingSettings) GetNotifySettings(ctx context.Context, owner int64, scope domain.NotifyScope) (domain.PeerNotifySettings, bool, error) {
	s.c.inc(&s.c.settingsSingle)
	return s.c.settingsDelegate.GetNotifySettings(ctx, owner, scope)
}

func (s countingSettings) GetNotifySettingsForOwners(ctx context.Context, owners []int64, scope domain.NotifyScope) (map[int64]domain.PeerNotifySettings, error) {
	s.c.inc(&s.c.settingsBatch)
	return s.c.settingsDelegate.GetNotifySettingsForOwners(ctx, owners, scope)
}

type countingUsers struct{ c *ioCounter }

func (u countingUsers) ByID(ctx context.Context, viewerID, userID int64) (domain.User, bool, error) {
	u.c.inc(&u.c.userLookups)
	return u.c.usersDelegate.ByID(ctx, viewerID, userID)
}

type countingLangPack struct{ c *ioCounter }

func (l countingLangPack) GetStrings(ctx context.Context, pack, lang string, keys []string) (domain.LangPack, error) {
	l.c.inc(&l.c.langPackLookups)
	return l.c.langPackDelegate.GetStrings(ctx, pack, lang, keys)
}

// 一页成员的推送按页批量读取：授权随设备登记一起读出（不逐台查询），通知设置每个作用域
// 一次批量查询（不逐个收件人查询），文案模板在一个任务里按语言只查一次。
func TestChannelFanOutBatchesPerPageIO(t *testing.T) {
	base := newServiceFixture(t, Config{MemberBatch: 50})
	c := &ioCounter{authorizationsDelegate: base.auths, settingsDelegate: memory.NewPasswordStore(), langPackDelegate: testLangPack()}
	members := &fakeMembers{members: map[int64][]int64{}}
	base.svc = NewService(Config{MemberBatch: 50}, Deps{
		Devices:        base.devices,
		Authorizations: countingAuths{c},
		Presence:       base.presence,
		Users:          fakeUsers{aliceID: {ID: aliceID, FirstName: "Alice"}},
		Sender:         base.sender,
		Strings:        countingLangPack{c},
		Policy:         NewSettingsPolicy(countingSettings{c}, func() time.Time { return time.Unix(1_800_000_000, 0) }),
		Members:        members,
	}, zaptest.NewLogger(t))
	const n = 20
	var ids []int64
	for i := range n {
		id := int64(4200000 + i)
		ids = append(ids, id)
		// 每人两台设备：逐台、逐人查询会是 40 次授权、40 次（话题时 60 次）设置、80 次以上模板查询。
		base.register([8]byte{byte(i), 1}, id, domain.PushTokenAPNs, "tok-a", testSecret(), presenceNone)
		base.register([8]byte{byte(i), 2}, id, domain.PushTokenAPNs, "tok-b", testSecret(), presenceNone)
	}
	members.members[groupID] = ids
	base.start()
	msg := groupTextFromAlice("hi")
	msg.Chat.TopicID = 3
	base.svc.ChannelMessage(msg)
	waitUntil(t, func() bool { return base.sentCount() == 2*n })
	base.stop()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.authLookups != 0 {
		t.Errorf("authorization lookups = %d, want 0 (loaded with the registrations)", c.authLookups)
	}
	if c.settingsSingle != 0 || c.settingsBatch > 3 {
		t.Errorf("notify settings queries = %d single + %d batch, want at most 3 batch (topic, group, default)", c.settingsSingle, c.settingsBatch)
	}
	// 两个键（CHAT_MESSAGE_TEXT、ENCRYPTED_MESSAGE），语言包缺键时各回落一次：上限 4。
	if c.langPackLookups > 4 {
		t.Errorf("langpack lookups = %d, want at most 4 for one language", c.langPackLookups)
	}
}

// 广播频道的文案只用频道名：扇出时不查发送者的显示名；群组文案带发送者，照常查询。
func TestChannelFanOutLooksUpSenderOnlyWhenTheAlertShowsIt(t *testing.T) {
	for _, tc := range []struct {
		name        string
		chat        Chat
		wantLookups bool
	}{
		{name: "broadcast", chat: Chat{Kind: ChatChannel, ID: channelID}, wantLookups: false},
		{name: "group", chat: Chat{Kind: ChatGroup, ID: groupID}, wantLookups: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := newServiceFixture(t, Config{})
			c := &ioCounter{usersDelegate: fakeUsers{aliceID: {ID: aliceID, FirstName: "Alice"}}}
			members := &fakeMembers{members: map[int64][]int64{}}
			base.svc = NewService(Config{}, Deps{
				Devices:  base.devices,
				Presence: base.presence,
				Users:    countingUsers{c},
				Sender:   base.sender,
				Strings:  testLangPack(),
				Policy:   NewSettingsPolicy(memory.NewPasswordStore(), func() time.Time { return time.Unix(1_800_000_000, 0) }),
				Members:  members,
			}, zaptest.NewLogger(t))
			const n = 5
			ids := []int64{aliceID}
			for i := range n {
				id := int64(4300000 + i)
				ids = append(ids, id)
				base.register([8]byte{byte(i), 3}, id, domain.PushTokenAPNs, "tok", testSecret(), presenceNone)
			}
			members.members[tc.chat.ID] = ids
			base.start()
			base.svc.ChannelMessage(NewMessage{Chat: tc.chat, ChatTitle: "News", SenderID: aliceID, MessageID: 7, Text: "post"})
			waitUntil(t, func() bool { return base.sentCount() == n })
			base.stop()

			c.mu.Lock()
			defer c.mu.Unlock()
			if got := c.userLookups > 0; got != tc.wantLookups {
				t.Fatalf("sender lookups = %d, want lookups=%v", c.userLookups, tc.wantLookups)
			}
		})
	}
}
