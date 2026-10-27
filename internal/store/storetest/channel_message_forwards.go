package storetest

import (
	"context"
	"sync"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// ChannelMessageForwardsFixture 是一个频道主、一个读者与对应的存储；每个子测试各要一份。
// Messages 与 Channels 必须共用同一份数据（Postgres 同库；memory 由 fixture 接好）。
type ChannelMessageForwardsFixture struct {
	Messages      store.MessageStore
	Channels      store.ChannelStore
	Owner, Reader int64
}

const channelMessageForwardsRandomBase = 7_900_000

// ChannelMessageForwards 校验两种实现对帖子转发数的同一契约：转发写入带上 ForwardedPost
// 时源帖子的转发数加一，消息本体与浏览数查询都读得到；同一条转发的重放不再计数；并发转发
// 不丢计数；源消息不是帖子时不计数。
func ChannelMessageForwards(t *testing.T, newFixture func(t *testing.T) ChannelMessageForwardsFixture) {
	t.Helper()
	ctx := context.Background()
	now := baseNow()

	createChannel := func(t *testing.T, f ChannelMessageForwardsFixture, broadcast bool) domain.Channel {
		t.Helper()
		created, err := f.Channels.CreateChannel(ctx, domain.CreateChannelRequest{
			CreatorUserID: f.Owner,
			Title:         "Forwards contract",
			Broadcast:     broadcast,
			Megagroup:     !broadcast,
			MemberUserIDs: []int64{f.Reader},
			Date:          now,
		})
		if err != nil {
			t.Fatalf("CreateChannel: %v", err)
		}
		return created.Channel
	}
	sendSource := func(t *testing.T, f ChannelMessageForwardsFixture, channel domain.Channel, random int64) domain.ChannelMessage {
		t.Helper()
		res, err := f.Channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
			UserID:    f.Owner,
			ChannelID: channel.ID,
			RandomID:  random,
			Message:   "source",
			Date:      now,
		})
		if err != nil {
			t.Fatalf("SendChannelMessage source: %v", err)
		}
		return res.Message
	}
	forwardHeader := func(src domain.ChannelMessage) *domain.MessageForward {
		return &domain.MessageForward{From: domain.Peer{Type: domain.PeerTypeChannel, ID: src.ChannelID}, Date: src.Date, ChannelPost: src.ID}
	}
	ref := func(src domain.ChannelMessage) *domain.ChannelPostRef {
		return &domain.ChannelPostRef{ChannelID: src.ChannelID, MessageID: src.ID}
	}
	forwardToPrivate := func(t *testing.T, f ChannelMessageForwardsFixture, src domain.ChannelMessage, random int64) domain.SendPrivateTextResult {
		t.Helper()
		sent, err := f.Messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
			SenderUserID:    f.Reader,
			RecipientUserID: f.Owner,
			RandomID:        random,
			Message:         src.Body,
			Forward:         forwardHeader(src),
			ForwardedPost:   ref(src),
			Date:            now,
		})
		if err != nil {
			t.Errorf("SendPrivateText forward: %v", err)
		}
		return sent
	}
	forwardToChannel := func(t *testing.T, f ChannelMessageForwardsFixture, target domain.Channel, src domain.ChannelMessage, random int64) domain.SendChannelMessageResult {
		t.Helper()
		res, err := f.Channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
			UserID:        f.Owner,
			ChannelID:     target.ID,
			RandomID:      random,
			Message:       src.Body,
			Forward:       forwardHeader(src),
			ForwardedPost: ref(src),
			Date:          now,
		})
		if err != nil {
			t.Fatalf("SendChannelMessage forward: %v", err)
		}
		return res
	}
	storedForwards := func(t *testing.T, f ChannelMessageForwardsFixture, src domain.ChannelMessage) int {
		t.Helper()
		history, err := f.Channels.GetChannelMessages(ctx, f.Reader, src.ChannelID, []int{src.ID})
		if err != nil || len(history.Messages) != 1 {
			t.Fatalf("GetChannelMessages = %+v, %v", history.Messages, err)
		}
		return history.Messages[0].ForwardsCount
	}
	viewForwards := func(t *testing.T, f ChannelMessageForwardsFixture, src domain.ChannelMessage) (int, bool) {
		t.Helper()
		res, err := f.Channels.GetChannelMessageViews(ctx, domain.ChannelMessageViewsRequest{
			UserID: f.Reader, ChannelID: src.ChannelID, IDs: []int{src.ID}, Date: now,
		})
		if err != nil {
			t.Fatalf("GetChannelMessageViews: %v", err)
		}
		count, ok := res.Forwards[src.ID]
		return count, ok
	}

	t.Run("forwarding a post to a chat and to a channel counts each forward", func(t *testing.T) {
		f := newFixture(t)
		source := createChannel(t, f, true)
		target := createChannel(t, f, false)
		post := sendSource(t, f, source, channelMessageForwardsRandomBase+1)
		if got := storedForwards(t, f, post); got != 0 {
			t.Fatalf("new post forwards = %d, want 0", got)
		}
		forwardToPrivate(t, f, post, channelMessageForwardsRandomBase+2)
		forwardToChannel(t, f, target, post, channelMessageForwardsRandomBase+3)
		if got := storedForwards(t, f, post); got != 2 {
			t.Fatalf("stored forwards = %d, want 2", got)
		}
		if got, ok := viewForwards(t, f, post); !ok || got != 2 {
			t.Fatalf("views query forwards = %d (present %v), want 2", got, ok)
		}
	})

	t.Run("a replay of the same forward is not counted again", func(t *testing.T) {
		f := newFixture(t)
		source := createChannel(t, f, true)
		target := createChannel(t, f, false)
		post := sendSource(t, f, source, channelMessageForwardsRandomBase+11)
		forwardToPrivate(t, f, post, channelMessageForwardsRandomBase+12)
		if replay := forwardToPrivate(t, f, post, channelMessageForwardsRandomBase+12); !replay.Duplicate {
			t.Fatalf("private replay Duplicate = false, want true")
		}
		forwardToChannel(t, f, target, post, channelMessageForwardsRandomBase+13)
		if replay := forwardToChannel(t, f, target, post, channelMessageForwardsRandomBase+13); !replay.Duplicate {
			t.Fatalf("channel replay Duplicate = false, want true")
		}
		if got := storedForwards(t, f, post); got != 2 {
			t.Fatalf("stored forwards = %d, want 2", got)
		}
	})

	t.Run("concurrent forwards lose no count", func(t *testing.T) {
		f := newFixture(t)
		source := createChannel(t, f, true)
		target := createChannel(t, f, false)
		post := sendSource(t, f, source, channelMessageForwardsRandomBase+21)
		const perTarget = 6
		var wg sync.WaitGroup
		for i := 0; i < perTarget; i++ {
			wg.Add(2)
			go func(i int) {
				defer wg.Done()
				forwardToPrivate(t, f, post, channelMessageForwardsRandomBase+100+int64(i))
			}(i)
			go func(i int) {
				defer wg.Done()
				if _, err := f.Channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
					UserID: f.Owner, ChannelID: target.ID, RandomID: channelMessageForwardsRandomBase + 200 + int64(i),
					Message: post.Body, Forward: forwardHeader(post), ForwardedPost: ref(post), Date: now,
				}); err != nil {
					t.Errorf("SendChannelMessage forward: %v", err)
				}
			}(i)
		}
		wg.Wait()
		if t.Failed() {
			return
		}
		if got := storedForwards(t, f, post); got != 2*perTarget {
			t.Fatalf("stored forwards = %d, want %d", got, 2*perTarget)
		}
	})

	t.Run("forwarding a post to channel direct messages counts it once", func(t *testing.T) {
		f := newFixture(t)
		source := createChannel(t, f, true)
		post := sendSource(t, f, source, channelMessageForwardsRandomBase+41)
		dm, err := f.Channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: f.Owner, Title: "Forwards DM", Broadcast: true, Date: now})
		if err != nil {
			t.Fatalf("CreateChannel DM parent: %v", err)
		}
		enabled, err := f.Channels.SetPaidMessagesPrice(ctx, f.Owner, dm.Channel.ID, 0, true)
		if err != nil || enabled.Channel.LinkedMonoforumID == 0 {
			t.Fatalf("enable direct messages: %+v, %v", enabled.Channel, err)
		}
		forwardToMonoforum := func(random int64) domain.SendChannelMessageResult {
			t.Helper()
			res, err := f.Channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{
				MonoforumID:   enabled.Channel.LinkedMonoforumID,
				SenderUserID:  f.Reader,
				SavedPeer:     domain.Peer{Type: domain.PeerTypeUser, ID: f.Reader},
				RandomID:      random,
				Message:       post.Body,
				Forward:       forwardHeader(post),
				ForwardedPost: ref(post),
				Date:          now,
			})
			if err != nil {
				t.Fatalf("SendMonoforumMessage forward: %v", err)
			}
			return res
		}
		forwardToMonoforum(channelMessageForwardsRandomBase + 42)
		if replay := forwardToMonoforum(channelMessageForwardsRandomBase + 42); !replay.Duplicate {
			t.Fatalf("monoforum replay Duplicate = false, want true")
		}
		if got := storedForwards(t, f, post); got != 1 {
			t.Fatalf("stored forwards = %d, want 1", got)
		}
	})

	t.Run("a scheduled forward is counted when it is sent, a cancelled one never", func(t *testing.T) {
		f := newFixture(t)
		scheduled, ok := f.Messages.(store.ScheduledMessageStore)
		if !ok {
			t.Fatalf("%T does not store scheduled messages", f.Messages)
		}
		source := createChannel(t, f, true)
		post := sendSource(t, f, source, channelMessageForwardsRandomBase+51)
		peer := domain.Peer{Type: domain.PeerTypeUser, ID: f.Owner}
		schedule := func(random int64) domain.ScheduledMessage {
			t.Helper()
			msg, err := scheduled.CreateScheduledMessage(ctx, domain.ScheduleMessageRequest{
				OwnerUserID:   f.Reader,
				Peer:          peer,
				RandomID:      random,
				Message:       post.Body,
				Forward:       forwardHeader(post),
				ForwardedPost: ref(post),
				ScheduleDate:  now + 3600,
				Date:          now,
			})
			if err != nil {
				t.Fatalf("CreateScheduledMessage: %v", err)
			}
			return msg
		}
		kept := schedule(channelMessageForwardsRandomBase + 52)
		cancelled := schedule(channelMessageForwardsRandomBase + 53)
		if kept.ForwardedPost == nil || *kept.ForwardedPost != *ref(post) {
			t.Fatalf("scheduled ForwardedPost = %+v, want %+v", kept.ForwardedPost, ref(post))
		}
		if got := storedForwards(t, f, post); got != 0 {
			t.Fatalf("forwards after scheduling = %d, want 0 (counted only when sent)", got)
		}
		if _, err := scheduled.DeleteScheduledMessages(ctx, domain.ScheduledMessageFilter{OwnerUserID: f.Reader, Peer: peer, IDs: []int{cancelled.ID}}, now+1); err != nil {
			t.Fatalf("DeleteScheduledMessages: %v", err)
		}
		claimed, err := scheduled.ClaimScheduledMessages(ctx, domain.ScheduledMessageClaim{OwnerUserID: f.Reader, Peer: peer, IDs: []int{kept.ID, cancelled.ID}, Now: now + 3600, Limit: 10, LeaseUntil: now + 3700})
		if err != nil {
			t.Fatalf("ClaimScheduledMessages: %v", err)
		}
		if len(claimed) != 1 || claimed[0].ID != kept.ID || claimed[0].ForwardedPost == nil || *claimed[0].ForwardedPost != *ref(post) {
			t.Fatalf("claimed = %+v, want only the kept forward with its source post", claimed)
		}
		// 派发：按定时行上持久化的源帖子引用发出（rpc 的 sendScheduledMessage 同样这么做）。
		if _, err := f.Messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
			SenderUserID:    f.Reader,
			RecipientUserID: f.Owner,
			RandomID:        claimed[0].RandomID,
			Message:         claimed[0].Message,
			Forward:         claimed[0].Forward,
			ForwardedPost:   claimed[0].ForwardedPost,
			Date:            now + 3600,
		}); err != nil {
			t.Fatalf("SendPrivateText scheduled forward: %v", err)
		}
		if got := storedForwards(t, f, post); got != 1 {
			t.Fatalf("forwards after dispatch = %d, want 1", got)
		}
	})

	t.Run("a message that is not a post is not counted", func(t *testing.T) {
		f := newFixture(t)
		group := createChannel(t, f, false)
		msg := sendSource(t, f, group, channelMessageForwardsRandomBase+31)
		forwardToPrivate(t, f, msg, channelMessageForwardsRandomBase+32)
		forwardToChannel(t, f, group, msg, channelMessageForwardsRandomBase+33)
		if got := storedForwards(t, f, msg); got != 0 {
			t.Fatalf("group message forwards = %d, want 0", got)
		}
		if got, ok := viewForwards(t, f, msg); ok {
			t.Fatalf("views query forwards for group message = %d, want absent", got)
		}
	})
}
