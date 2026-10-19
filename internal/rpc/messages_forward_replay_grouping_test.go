package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// 转发相册的部分重放：同一请求重试时，已发过的条目走重放（不再加载源消息），剩下的条目
// 必须落进已发出条目所在的同一个分组，而不是按"剩余条目里第一条"另起一组把相册拆成两半。
// 测试先只转发相册第一条（逐条指纹与整批请求里的同一条相同），再用整批请求重试。

const replaySourceGroup = int64(515151)

// forwardReplayRequest 是同一批转发的整批请求；first 只带第一条。
func forwardReplayRequest(from, to tg.InputPeerClass, ids []int, randomIDs []int64) (first, full *tg.MessagesForwardMessagesRequest) {
	first = &tg.MessagesForwardMessagesRequest{FromPeer: from, ToPeer: to, ID: ids[:1], RandomID: randomIDs[:1]}
	full = &tg.MessagesForwardMessagesRequest{FromPeer: from, ToPeer: to, ID: ids, RandomID: randomIDs}
	return first, full
}

func assertOneGroup(t *testing.T, label string, groups []int64, want int) {
	t.Helper()
	if len(groups) != want {
		t.Fatalf("%s: got %d forwarded items %v, want %d", label, len(groups), groups, want)
	}
	for _, group := range groups {
		if group == 0 || group != groups[0] {
			t.Fatalf("%s: grouped ids = %v, want one shared non-zero group", label, groups)
		}
	}
}

func TestForwardAlbumPartialReplayKeepsOneGroupPrivate(t *testing.T) {
	f := newScheduledFidelityFixture(t)
	ctx := context.Background()
	sourceIDs := make([]int, 0, 3)
	for i := int64(0); i < 3; i++ {
		res, err := f.messages.SendPrivateText(ctx, f.owner.ID, domain.SendPrivateTextRequest{
			SenderUserID: f.owner.ID, RecipientUserID: f.friend.ID,
			RandomID: 81001 + i, Message: "album", GroupedID: replaySourceGroup, Date: 1001 + int(i),
		})
		if err != nil {
			t.Fatalf("seed source album %d: %v", i, err)
		}
		sourceIDs = append(sourceIDs, res.SenderMessage.ID)
	}
	randomIDs := []int64{81101, 81102, 81103}
	first, full := forwardReplayRequest(f.friendPeer(), f.friendPeer(), sourceIDs, randomIDs)
	if _, err := f.router.onMessagesForwardMessages(f.ctx(f.owner), first); err != nil {
		t.Fatalf("forward first item: %v", err)
	}
	if _, err := f.router.onMessagesForwardMessages(f.ctx(f.owner), full); err != nil {
		t.Fatalf("retry whole forward: %v", err)
	}
	groups := make([]int64, 0, 3)
	for _, randomID := range randomIDs {
		if msg, ok := f.privateByRandomID(randomID); ok {
			groups = append(groups, msg.GroupedID)
		}
	}
	assertOneGroup(t, "private partial replay", groups, 3)
}

func TestForwardAlbumPartialReplayKeepsOneGroupChannel(t *testing.T) {
	f := newScheduledFidelityFixture(t)
	ctx := context.Background()
	group := f.createGroup("Replay Album")
	sourceIDs := make([]int, 0, 3)
	for i := int64(0); i < 3; i++ {
		res, err := f.channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
			UserID: f.owner.ID, ChannelID: group.ID,
			RandomID: 82001 + i, Message: "album", GroupedID: replaySourceGroup, Date: 1001 + int(i),
		})
		if err != nil {
			t.Fatalf("seed source album %d: %v", i, err)
		}
		sourceIDs = append(sourceIDs, res.Message.ID)
	}
	randomIDs := []int64{82101, 82102, 82103}
	first, full := forwardReplayRequest(inputPeerChannel(group), inputPeerChannel(group), sourceIDs, randomIDs)
	if _, err := f.router.onMessagesForwardMessages(f.ctx(f.owner), first); err != nil {
		t.Fatalf("forward first item: %v", err)
	}
	if _, err := f.router.onMessagesForwardMessages(f.ctx(f.owner), full); err != nil {
		t.Fatalf("retry whole forward: %v", err)
	}
	groups := make([]int64, 0, 3)
	for _, randomID := range randomIDs {
		if msg, ok := f.channelByRandomID(group, randomID); ok {
			groups = append(groups, msg.GroupedID)
		}
	}
	assertOneGroup(t, "channel partial replay", groups, 3)
}

func TestForwardAlbumPartialReplayKeepsOneGroupMonoforum(t *testing.T) {
	f := newAlbumMonoforumFixture(t)
	ctx := context.Background()
	subPeer := domain.Peer{Type: domain.PeerTypeUser, ID: f.sub.ID}
	sourceIDs := make([]int, 0, 3)
	for i := int64(0); i < 3; i++ {
		res, err := f.channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{
			MonoforumID: f.monoID, SenderUserID: f.sub.ID, SavedPeer: subPeer,
			RandomID: 83001 + i, Message: "album", GroupedID: replaySourceGroup, Date: 1001 + int(i),
		})
		if err != nil {
			t.Fatalf("seed source album %d: %v", i, err)
		}
		sourceIDs = append(sourceIDs, res.Message.ID)
	}
	randomIDs := []int64{83101, 83102, 83103}
	first, full := forwardReplayRequest(f.monoInput, f.monoInput, sourceIDs, randomIDs)
	subCtx := WithUserID(ctx, f.sub.ID)
	if _, err := f.router.onMessagesForwardMessages(subCtx, first); err != nil {
		t.Fatalf("forward first item: %v", err)
	}
	if _, err := f.router.onMessagesForwardMessages(subCtx, full); err != nil {
		t.Fatalf("retry whole forward: %v", err)
	}
	assertOneGroup(t, "monoforum partial replay", f.monoforumHistoryGroups(t, 83101, 83103), 3)
}
