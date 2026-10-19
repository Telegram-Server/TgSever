package storetest

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// MonoforumAlbumGrouping 校验两套实现在频道私信(monoforum)写入 channel_messages 时保留相册
// grouped_id：同一相册各条共享非零值，发送回声与读历史都可见，单条恒 0。
// owner 是母广播频道创建者，sub 是订阅者；返回创建的频道 id 供调用方清理。
func MonoforumAlbumGrouping(t *testing.T, s store.ChannelStore, owner, sub int64, title string) []int64 {
	t.Helper()
	ctx := context.Background()
	broadcast, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: title, Broadcast: true, Date: baseNow()})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelIDs := []int64{broadcast.Channel.ID}
	enabled, err := s.SetPaidMessagesPrice(ctx, owner, broadcast.Channel.ID, 0, true)
	if err != nil {
		t.Fatalf("enable DM: %v", err)
	}
	monoID := enabled.Channel.LinkedMonoforumID
	if monoID == 0 {
		t.Fatalf("no monoforum created")
	}
	channelIDs = append(channelIDs, monoID)

	subPeer := domain.Peer{Type: domain.PeerTypeUser, ID: sub}
	const groupedID = int64(55443322110)
	for i, randomID := range []int64{9101, 9102} {
		res, err := s.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{
			MonoforumID: monoID, SenderUserID: sub, SavedPeer: subPeer, RandomID: randomID,
			Message: "album", GroupedID: groupedID, Date: baseNow() + 1 + i,
		})
		if err != nil {
			t.Fatalf("send album item %d: %v", i, err)
		}
		if res.Message.GroupedID != groupedID {
			t.Fatalf("album item %d grouped_id = %d, want %d", i, res.Message.GroupedID, groupedID)
		}
	}
	single, err := s.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{
		MonoforumID: monoID, SenderUserID: sub, SavedPeer: subPeer, RandomID: 9103,
		Message: "single", Date: baseNow() + 3,
	})
	if err != nil {
		t.Fatalf("send single: %v", err)
	}
	hist, err := s.ListChannelHistory(ctx, sub, domain.ChannelHistoryFilter{ChannelID: monoID, Limit: 10})
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist.Messages) != 3 {
		t.Fatalf("history len = %d, want 3", len(hist.Messages))
	}
	for _, msg := range hist.Messages {
		want := groupedID
		if msg.ID == single.Message.ID {
			want = 0
		}
		if msg.GroupedID != want {
			t.Fatalf("history message %d grouped_id = %d, want %d", msg.ID, msg.GroupedID, want)
		}
	}
	return channelIDs
}
