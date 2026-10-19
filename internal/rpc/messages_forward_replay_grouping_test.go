package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"

	appmessages "telesrv/internal/app/messages"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
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

// 频道里的同一路径（继承已重放条目的分组）由纯函数测试 TestForwardGroupedIDs 覆盖；
// 这里保留私聊与 monoforum 两个调用点，各自走真实的重放查询。

func TestForwardAlbumPartialReplayKeepsOneGroupPrivate(t *testing.T) {
	messages := appmessages.NewService(memory.NewMessageStore(), nil)
	f := newForwardCaptionFixtureWithMessages(t, messages)
	owner, peer := f.owner, f.recipientPeer()
	sourceIDs := make([]int, 0, 3)
	for i := int64(0); i < 3; i++ {
		res, err := messages.SendPrivateText(f.ctx, owner.ID, domain.SendPrivateTextRequest{
			SenderUserID: owner.ID, RecipientUserID: f.recipient.ID,
			RandomID: 81001 + i, Message: "album", GroupedID: replaySourceGroup, Date: 1001 + int(i),
		})
		if err != nil {
			t.Fatalf("seed source album %d: %v", i, err)
		}
		sourceIDs = append(sourceIDs, res.SenderMessage.ID)
	}
	randomIDs := []int64{81101, 81102, 81103}
	first, full := forwardReplayRequest(peer, peer, sourceIDs, randomIDs)
	ownerCtx := WithUserID(f.ctx, owner.ID)
	if _, err := f.router.onMessagesForwardMessages(ownerCtx, first); err != nil {
		t.Fatalf("forward first item: %v", err)
	}
	if _, err := f.router.onMessagesForwardMessages(ownerCtx, full); err != nil {
		t.Fatalf("retry whole forward: %v", err)
	}
	history, err := messages.GetHistory(f.ctx, owner.ID, domain.MessageFilter{
		HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.recipient.ID}, Limit: 50,
	})
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	groups := make([]int64, 0, 3)
	for _, msg := range history.Messages {
		if msg.RandomID >= randomIDs[0] && msg.RandomID <= randomIDs[len(randomIDs)-1] {
			groups = append(groups, msg.GroupedID)
		}
	}
	assertOneGroup(t, "private partial replay", groups, 3)
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
