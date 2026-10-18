package storetest

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// ChannelMessageDeletions 校验两种实现对群/频道删除记录的同一契约：群消息没有
// 逐人副本，每条被删消息留一行，范围是 everyone，peer 是频道本身。
func ChannelMessageDeletions(t *testing.T, channels store.ChannelStore, deletions store.MessageDeletionStore, owner, member int64) {
	t.Helper()
	ctx := context.Background()
	now := baseNow()
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner,
		Title:         "Deletion log contract",
		Megagroup:     true,
		MemberUserIDs: []int64{member},
		Date:          now,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	channel := created.Channel
	channelPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: channel.ID}
	nextRandom := int64(now) * 10
	send := func(t *testing.T, from int64, body string) domain.ChannelMessage {
		t.Helper()
		nextRandom++
		res, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
			UserID:    from,
			ChannelID: channel.ID,
			RandomID:  nextRandom,
			Message:   body,
			Date:      now,
		})
		if err != nil {
			t.Fatalf("SendChannelMessage: %v", err)
		}
		return res.Message
	}
	latest := func(t *testing.T, actor int64) []domain.MessageDeletion {
		t.Helper()
		got, err := deletions.ListMessageDeletions(ctx, domain.MessageDeletionFilter{ActorUserID: actor, Limit: 50})
		if err != nil {
			t.Fatalf("ListMessageDeletions: %v", err)
		}
		return latestBatch(got)
	}

	t.Run("a member deleting their own message records one row for everyone", func(t *testing.T) {
		msg := send(t, member, "my mistake")
		if _, err := channels.DeleteChannelMessages(ctx, domain.DeleteChannelMessagesRequest{
			UserID:    member,
			ChannelID: channel.ID,
			IDs:       []int{msg.ID},
			Date:      now + 1,
		}); err != nil {
			t.Fatalf("DeleteChannelMessages: %v", err)
		}

		got := latest(t, member)
		if len(got) != 1 {
			t.Fatalf("latest batch = %+v, want the one deleted message", got)
		}
		want := domain.MessageDeletion{
			ID:           got[0].ID,
			BatchID:      got[0].BatchID,
			Date:         now + 1,
			Source:       domain.DeletionSourceUser,
			Scope:        domain.DeletionScopeEveryone,
			ActorType:    domain.DeletionActorUser,
			ActorUserID:  member,
			OwnerUserID:  0,
			Peer:         channelPeer,
			MessageID:    msg.ID,
			SenderUserID: member,
			MessageDate:  now,
			Body:         "my mistake",
		}
		if got[0] != want {
			t.Fatalf("deletion = %+v, want %+v", got[0], want)
		}
	})

	t.Run("an admin wiping a member's messages records participant_history", func(t *testing.T) {
		first := send(t, member, "spam a")
		second := send(t, member, "spam b")
		if _, err := channels.DeleteChannelParticipantHistory(ctx, domain.DeleteChannelParticipantHistoryRequest{
			UserID:            owner,
			ChannelID:         channel.ID,
			ParticipantUserID: member,
			Date:              now + 2,
		}); err != nil {
			t.Fatalf("DeleteChannelParticipantHistory: %v", err)
		}
		got := latest(t, owner)
		ids := map[int]bool{first.ID: false, second.ID: false}
		for _, row := range got {
			if row.Source != domain.DeletionSourceParticipantHistory || row.ActorUserID != owner ||
				row.SenderUserID != member || row.Scope != domain.DeletionScopeEveryone {
				t.Fatalf("deletion = %+v, want owner wiping member's messages", row)
			}
			ids[row.MessageID] = true
		}
		if len(got) != 2 || !ids[first.ID] || !ids[second.ID] {
			t.Fatalf("latest batch = %+v, want messages %d and %d", got, first.ID, second.ID)
		}
	})

	t.Run("clearing for everyone records history_clear, a local clear records nothing", func(t *testing.T) {
		kept := send(t, owner, "visible to others")
		before := latest(t, member)
		if _, err := channels.DeleteChannelHistory(ctx, domain.DeleteChannelHistoryRequest{
			UserID:    member,
			ChannelID: channel.ID,
			Date:      now + 3,
		}); err != nil {
			t.Fatalf("DeleteChannelHistory local: %v", err)
		}
		if after := latest(t, member); len(after) != len(before) || (len(after) > 0 && after[0].BatchID != before[0].BatchID) {
			t.Fatalf("local clear wrote deletions %+v; it removes nothing for anyone else", after)
		}

		if _, err := channels.DeleteChannelHistory(ctx, domain.DeleteChannelHistoryRequest{
			UserID:      owner,
			ChannelID:   channel.ID,
			ForEveryone: true,
			Date:        now + 4,
		}); err != nil {
			t.Fatalf("DeleteChannelHistory for everyone: %v", err)
		}
		got := latest(t, owner)
		if len(got) != 1 || got[0].MessageID != kept.ID || got[0].Source != domain.DeletionSourceHistoryClear ||
			got[0].Body != "visible to others" || got[0].Date != now+4 {
			t.Fatalf("latest batch = %+v, want history_clear of message %d", got, kept.ID)
		}
	})

	t.Run("an explicit system audit is not attributed to the channel owner", func(t *testing.T) {
		msg := send(t, member, "expiring")
		if _, err := channels.DeleteChannelMessages(ctx, domain.DeleteChannelMessagesRequest{
			UserID:    owner,
			ChannelID: channel.ID,
			IDs:       []int{msg.ID},
			Date:      now + 5,
			Audit:     domain.SystemDeletionAudit(domain.DeletionSourceTTL),
		}); err != nil {
			t.Fatalf("DeleteChannelMessages ttl: %v", err)
		}
		got, err := deletions.ListMessageDeletions(ctx, domain.MessageDeletionFilter{SenderUserID: member, Limit: 50})
		if err != nil {
			t.Fatalf("ListMessageDeletions: %v", err)
		}
		if len(got) == 0 || got[0].MessageID != msg.ID || got[0].Source != domain.DeletionSourceTTL ||
			got[0].ActorType != domain.DeletionActorSystem || got[0].ActorUserID != 0 {
			t.Fatalf("newest deletion of member's messages = %+v, want system ttl of %d", got, msg.ID)
		}
	})

	t.Run("deleting a post records its discussion root as discussion_cascade", func(t *testing.T) {
		broadcast, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
			CreatorUserID: owner, Title: "Deletion log broadcast", Broadcast: true, Date: now,
		})
		if err != nil {
			t.Fatalf("CreateChannel broadcast: %v", err)
		}
		group, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
			CreatorUserID: owner, Title: "Deletion log comments", Megagroup: true, Date: now,
		})
		if err != nil {
			t.Fatalf("CreateChannel discussion: %v", err)
		}
		if _, err := channels.SetDiscussionGroup(ctx, owner, broadcast.Channel.ID, group.Channel.ID); err != nil {
			t.Fatalf("SetDiscussionGroup: %v", err)
		}
		nextRandom++
		post, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
			UserID: owner, ChannelID: broadcast.Channel.ID, RandomID: nextRandom, Message: "announcement", Date: now,
		})
		if err != nil {
			t.Fatalf("SendChannelMessage post: %v", err)
		}
		if post.Message.Discussion == nil {
			t.Fatalf("post = %+v, want a discussion root in the linked group", post.Message)
		}
		if _, err := channels.DeleteChannelMessages(ctx, domain.DeleteChannelMessagesRequest{
			UserID: owner, ChannelID: broadcast.Channel.ID, IDs: []int{post.Message.ID}, Date: now + 7,
		}); err != nil {
			t.Fatalf("DeleteChannelMessages post: %v", err)
		}

		got, err := deletions.ListMessageDeletions(ctx, domain.MessageDeletionFilter{ActorUserID: owner, Limit: 2})
		if err != nil {
			t.Fatalf("ListMessageDeletions: %v", err)
		}
		bySource := make(map[domain.DeletionSource]domain.MessageDeletion, len(got))
		for _, row := range got {
			bySource[row.Source] = row
		}
		postRow, rootRow := bySource[domain.DeletionSourceUser], bySource[domain.DeletionSourceDiscussionCascade]
		if postRow.MessageID != post.Message.ID || postRow.Peer.ID != broadcast.Channel.ID {
			t.Fatalf("deletions = %+v, want the post deleted by the owner", got)
		}
		if rootRow.MessageID != post.Message.Discussion.MessageID || rootRow.Peer.ID != group.Channel.ID ||
			rootRow.ActorUserID != owner || rootRow.BatchID == postRow.BatchID {
			t.Fatalf("deletions = %+v, want the discussion root as its own cascade batch by the owner", got)
		}
	})

	t.Run("deleting a forum topic records topic_history", func(t *testing.T) {
		forum, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
			CreatorUserID: owner,
			Title:         "Deletion log forum",
			Megagroup:     true,
			Forum:         true,
			MemberUserIDs: []int64{member},
			Date:          now,
		})
		if err != nil {
			t.Fatalf("CreateChannel forum: %v", err)
		}
		nextRandom++
		topic, err := channels.CreateForumTopic(ctx, domain.CreateChannelForumTopicRequest{
			UserID:    owner,
			ChannelID: forum.Channel.ID,
			Title:     "Doomed topic",
			RandomID:  nextRandom,
			Date:      now,
		})
		if err != nil {
			t.Fatalf("CreateForumTopic: %v", err)
		}
		if _, err := channels.DeleteForumTopicHistory(ctx, domain.DeleteChannelForumTopicHistoryRequest{
			UserID:    owner,
			ChannelID: forum.Channel.ID,
			TopicID:   topic.Topic.TopicID,
			Date:      now + 6,
		}); err != nil {
			t.Fatalf("DeleteForumTopicHistory: %v", err)
		}
		got := latest(t, owner)
		if len(got) != 1 || got[0].Source != domain.DeletionSourceTopicHistory ||
			got[0].Peer != (domain.Peer{Type: domain.PeerTypeChannel, ID: forum.Channel.ID}) || got[0].MessageID != topic.Message.ID {
			t.Fatalf("latest batch = %+v, want topic_history of root %d", got, topic.Message.ID)
		}
	})
}
