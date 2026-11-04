package storetest

import (
	"context"
	"reflect"
	"testing"

	"telesrv/internal/store"

	"telesrv/internal/domain"
)

// NotifySoundContract：按平台分槽的消息声音与 story 声音原样存取，每条读路径
// （单查、批量 peer、全量 peer、例外列表、话题页）都返回同一份；整行覆盖时声音一并覆盖。
// owner 是一个没有任何通知设置的用户。
func NotifySoundContract(t *testing.T, s store.NotifySettingsStore, owner int64) {
	t.Helper()
	ctx := context.Background()
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: owner + 11}
	peerScope := domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: peer}
	topicScope := domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: peer, TopicID: 5}

	want := domain.PeerNotifySettings{
		Sound: domain.NotifySounds{
			IOS:     &domain.NotificationSound{Kind: domain.NotificationSoundDefault},
			Android: &domain.NotificationSound{Kind: domain.NotificationSoundNone},
			Other:   &domain.NotificationSound{Kind: domain.NotificationSoundRingtone, RingtoneID: 77},
		},
		StoriesSound: domain.NotifySounds{
			Other: &domain.NotificationSound{Kind: domain.NotificationSoundLocal, Title: "Ding", Data: "ding.wav"},
		},
	}
	if err := s.SaveNotifySettings(ctx, owner, peerScope, want); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveNotifySettings(ctx, owner, topicScope, want); err != nil {
		t.Fatal(err)
	}

	same := func(path string, got domain.PeerNotifySettings) {
		t.Helper()
		if !reflect.DeepEqual(got.Sound, want.Sound) || !reflect.DeepEqual(got.StoriesSound, want.StoriesSound) {
			t.Fatalf("%s: sound=%+v stories=%+v, want sound=%+v stories=%+v", path, got.Sound, got.StoriesSound, want.Sound, want.StoriesSound)
		}
	}
	single, found, err := s.GetNotifySettings(ctx, owner, peerScope)
	if err != nil || !found {
		t.Fatalf("GetNotifySettings found=%v err=%v", found, err)
	}
	same("GetNotifySettings", single)

	batch, err := s.GetPeerNotifySettings(ctx, owner, []domain.Peer{peer})
	if err != nil {
		t.Fatal(err)
	}
	same("GetPeerNotifySettings", batch[peer])

	all, err := s.AllPeerNotifySettings(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	same("AllPeerNotifySettings", all[peer])

	topics, err := s.GetForumTopicNotifySettings(ctx, owner, peer, []int{5})
	if err != nil {
		t.Fatal(err)
	}
	same("GetForumTopicNotifySettings", topics[5])

	exceptions, err := s.ListNotifyExceptions(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(exceptions) != 2 {
		t.Fatalf("ListNotifyExceptions = %+v, want peer and topic", exceptions)
	}
	for _, ex := range exceptions {
		same("ListNotifyExceptions", ex.Settings)
	}

	if err := s.SaveNotifySettings(ctx, owner, peerScope, domain.PeerNotifySettings{}); err != nil {
		t.Fatal(err)
	}
	cleared, _, err := s.GetNotifySettings(ctx, owner, peerScope)
	if err != nil {
		t.Fatal(err)
	}
	if !cleared.Sound.IsZero() || !cleared.StoriesSound.IsZero() {
		t.Fatalf("after overwrite sound=%+v stories=%+v, want cleared", cleared.Sound, cleared.StoriesSound)
	}
}
