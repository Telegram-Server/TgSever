package push

import (
	"slices"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// albumConfig 用很短的去抖时间，测试不必等真实的 1s/3s。
func albumConfig() Config {
	return Config{AlbumDebounce: 30 * time.Millisecond, AlbumMaxWait: 300 * time.Millisecond}
}

// albumFromAlice 是 alice 发给 bob 的一组相册消息（msg_id 从 first 起递增），每项一种媒体。
func albumFromAlice(groupedID int64, first int, media ...*domain.MessageMedia) []NewMessage {
	out := make([]NewMessage, 0, len(media))
	for i, m := range media {
		msg := textFromAlice("")
		msg.MessageID, msg.Media, msg.GroupedID = first+i, m, groupedID
		out = append(out, msg)
	}
	return out
}

// waitSettled 等推送数达到 want，再多等几个去抖周期确认没有更多推送。
func waitSettled(t *testing.T, f *serviceFixture, want int) {
	t.Helper()
	waitUntil(t, func() bool { return f.sentCount() >= want })
	time.Sleep(100 * time.Millisecond)
	if got := f.sentCount(); got != want {
		t.Fatalf("pushes = %d, want %d", got, want)
	}
}

func (f *serviceFixture) sentSnapshot() []Notification {
	f.sender.mu.Lock()
	defer f.sender.mu.Unlock()
	return append([]Notification(nil), f.sender.sent...)
}

func decryptAlert(t *testing.T, n Notification) (map[string]any, map[string]any) {
	t.Helper()
	inner := decryptInner(t, testSecret(), n)
	return inner, inner["aps"].(map[string]any)["alert"].(map[string]any)
}

// 私聊相册（同一 grouped_id 的 3 张图片）只推一次：msg_id 是最后一项，文案是“📷 3 photos”。
func TestPrivateAlbumPushesOnce(t *testing.T) {
	f := newServiceFixture(t, albumConfig())
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", testSecret(), presenceNone)
	f.start()
	for _, msg := range albumFromAlice(42, 77, photoMedia(), photoMedia(), photoMedia()) {
		if !f.svc.NewMessage(msg) {
			t.Fatal("NewMessage rejected, want buffered")
		}
	}
	waitSettled(t, f, 1)
	inner, alert := decryptAlert(t, f.sentSnapshot()[0])
	if inner["msg_id"] != "79" || alert["title"] != "Alice A" || alert["body"] != "📷 3 photos" {
		t.Fatalf("album push = %v, want msg_id 79 and Alice A / 📷 3 photos", inner)
	}
}

// plainAlert 读出未加密 payload（设备没有 secret）里的 msg_id 与文案。
func plainAlert(t *testing.T, n Notification) (msgID string, alert map[string]any) {
	t.Helper()
	outer := decodeJSONObject(t, n.Payload)
	alert, _ = outer["aps"].(map[string]any)["alert"].(map[string]any)
	msgID, _ = outer["msg_id"].(string)
	return msgID, alert
}

// 群组相册在成员扇出之前合并：每个成员只收到一条“📹 2 videos”，msg_id 是最后一项；
// 通知规则照常：静音的成员不推，关闭预览的成员收到 *_NOTEXT。
func TestGroupAlbumPushesOncePerMember(t *testing.T) {
	f := newChannelFixture(t, albumConfig())
	f.members.members[groupID] = []int64{aliceID, bobID, carolID, daveID}
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", nil, presenceNone)
	f.register([8]byte{3}, carolID, domain.PushTokenAPNs, "tok-carol", nil, presenceNone)
	f.register([8]byte{4}, daveID, domain.PushTokenAPNs, "tok-dave", nil, presenceNone)
	groupScope := domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: groupID}}
	f.mute(carolID, groupScope)
	hide := false
	if err := f.settings.SaveNotifySettings(t.Context(), daveID, groupScope, domain.PeerNotifySettings{ShowPreviews: &hide}); err != nil {
		t.Fatal(err)
	}
	f.start()
	for i, media := range []*domain.MessageMedia{videoMedia(), videoMedia()} {
		msg := groupTextFromAlice("")
		msg.MessageID, msg.Media, msg.GroupedID = 20+i, media, 7
		if !f.svc.ChannelMessage(msg) {
			t.Fatal("ChannelMessage rejected, want buffered")
		}
	}
	waitSettled(t, f.serviceFixture, 2)
	got := map[string]string{}
	for _, n := range f.sentSnapshot() {
		msgID, alert := plainAlert(t, n)
		if msgID != "21" || alert["title"] != "Team" {
			t.Fatalf("%s push = msg %s %v, want msg 21 titled Team", n.Token, msgID, alert)
		}
		got[n.Token], _ = alert["body"].(string)
	}
	if got["tok-bob"] != "Alice A: 📹 2 videos" || got["tok-dave"] != "Alice A: New message" {
		t.Fatalf("album bodies = %v, want bob 📹 2 videos and dave the no-preview text", got)
	}
	if f.members.callCount() != 1 {
		t.Fatalf("member pages read = %d, want one fan-out for the whole album", f.members.callCount())
	}
}

func (f *fakeMembers) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// 频道里的文件相册：“posted 2 files”；私聊里图片和视频混合：“📷📹 Album”。
func TestAlbumKeysByContent(t *testing.T) {
	t.Run("channel docs", func(t *testing.T) {
		f := newChannelFixture(t, albumConfig())
		f.members.members[channelID] = []int64{aliceID, bobID}
		f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", nil, presenceNone)
		f.start()
		for i := range 2 {
			f.svc.ChannelMessage(NewMessage{Chat: Chat{Kind: ChatChannel, ID: channelID}, ChatTitle: "News", SenderID: aliceID,
				MessageID: 30 + i, Media: documentMedia("application/pdf"), GroupedID: 8})
		}
		waitSettled(t, f.serviceFixture, 1)
		if msgID, alert := plainAlert(t, f.sentSnapshot()[0]); msgID != "31" || alert["title"] != "News" || alert["body"] != "posted 2 files" {
			t.Fatalf("channel album = msg %s %v, want msg 31 News / posted 2 files", msgID, alert)
		}
	})
	t.Run("private mixed", func(t *testing.T) {
		f := newServiceFixture(t, albumConfig())
		f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", nil, presenceNone)
		f.start()
		for _, msg := range albumFromAlice(9, 40, photoMedia(), videoMedia(), photoMedia()) {
			f.svc.NewMessage(msg)
		}
		waitSettled(t, f, 1)
		if msgID, alert := plainAlert(t, f.sentSnapshot()[0]); msgID != "42" || alert["body"] != "📷📹 Album" {
			t.Fatalf("mixed album = msg %s %v, want msg 42 📷📹 Album", msgID, alert)
		}
	})
}

// 只有一项的相册照原样推送那一项（带说明的图片仍是“📷 说明”）。
func TestOneItemAlbumPushesTheItem(t *testing.T) {
	f := newServiceFixture(t, albumConfig())
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", nil, presenceNone)
	f.start()
	msg := albumFromAlice(5, 50, photoMedia())[0]
	msg.Text = "sunset"
	f.svc.NewMessage(msg)
	waitSettled(t, f, 1)
	if msgID, alert := plainAlert(t, f.sentSnapshot()[0]); msgID != "50" || alert["body"] != "📷 sunset" {
		t.Fatalf("one-item album = msg %s %v, want the item itself", msgID, alert)
	}
}

// 不同的 grouped_id、不同的收件人各自合并，互不吞掉。
func TestAlbumsAreKeyedByGroupAndRecipient(t *testing.T) {
	f := newServiceFixture(t, albumConfig())
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", nil, presenceNone)
	f.register([8]byte{3}, aliceID, domain.PushTokenAPNs, "tok-alice", nil, presenceNone)
	f.start()
	toBob := albumFromAlice(1, 60, photoMedia(), photoMedia())
	otherAlbum := albumFromAlice(2, 62, photoMedia(), photoMedia())
	// 同一 grouped_id 的另一个收件人（alice 收到 bob 的消息）：私聊按收件人分开合并。
	toAlice := albumFromAlice(1, 70, photoMedia(), photoMedia())
	for i := range toAlice {
		toAlice[i].Chat, toAlice[i].SenderID, toAlice[i].Recipients = Chat{Kind: ChatPrivate, ID: bobID}, bobID, []int64{aliceID}
	}
	for _, msg := range slices.Concat(toBob, otherAlbum, toAlice) {
		f.svc.NewMessage(msg)
	}
	waitSettled(t, f, 3)
	var ids []string
	for _, n := range f.sentSnapshot() {
		msgID, _ := plainAlert(t, n)
		ids = append(ids, n.Token+"/"+msgID)
	}
	slices.Sort(ids)
	if want := []string{"tok-alice/71", "tok-bob/61", "tok-bob/63"}; !slices.Equal(ids, want) {
		t.Fatalf("album pushes = %v, want %v", ids, want)
	}
}

// 去抖有上限：项一直在到达时，从第一项起 AlbumMaxWait 到了就先推一次，不无限推迟。
func TestAlbumDebounceIsCappedByMaxWait(t *testing.T) {
	f := newServiceFixture(t, Config{AlbumDebounce: 60 * time.Millisecond, AlbumMaxWait: 150 * time.Millisecond})
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", nil, presenceNone)
	f.start()
	start := time.Now()
	var pushedWhileArriving bool
	for i := range 20 {
		msg := albumFromAlice(3, 80+i, photoMedia())[0]
		f.svc.NewMessage(msg)
		time.Sleep(20 * time.Millisecond)
		if f.sentCount() > 0 && !pushedWhileArriving {
			pushedWhileArriving = true
			if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
				t.Fatalf("first album push after %v, want about AlbumMaxWait (150ms)", elapsed)
			}
		}
	}
	if !pushedWhileArriving {
		t.Fatal("no push while items kept arriving, want one after AlbumMaxWait")
	}
}

// 关停时缓冲区里的相册立即发出（在关停期限内），不等去抖到期，也不丢。
func TestShutdownFlushesPendingAlbums(t *testing.T) {
	f := newChannelFixture(t, Config{AlbumDebounce: time.Hour, AlbumMaxWait: time.Hour})
	f.members.members[groupID] = []int64{aliceID, bobID}
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", nil, presenceNone)
	f.register([8]byte{3}, carolID, domain.PushTokenAPNs, "tok-carol", nil, presenceNone)
	f.start()
	for i := range 3 {
		msg := groupTextFromAlice("")
		msg.MessageID, msg.Media, msg.GroupedID = 90+i, photoMedia(), 11
		f.svc.ChannelMessage(msg)
	}
	private := albumFromAlice(12, 95, photoMedia(), photoMedia())
	for i := range private {
		private[i].Recipients = []int64{carolID}
	}
	for _, msg := range private {
		f.svc.NewMessage(msg)
	}
	stopWithin(t, f.serviceFixture, time.Second)
	got := map[string]string{}
	for _, n := range f.sentSnapshot() {
		_, alert := plainAlert(t, n)
		got[n.Token], _ = alert["body"].(string)
	}
	if got["tok-bob"] != "Alice A: 📷 3 photos" || got["tok-carol"] != "📷 2 photos" || len(got) != 2 {
		t.Fatalf("pushes on shutdown = %v, want both pending albums flushed", got)
	}
}
