package storetest

import (
	"context"
	"errors"
	"slices"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// ChannelMessageDeletionFixture 是新建的群主、成员与对应的存储；每个子测试各要一份。
type ChannelMessageDeletionFixture struct {
	Channels      store.ChannelStore
	Deletions     store.MessageDeletionStore
	Owner, Member int64
}

// ChannelMessageDeletions 校验两种实现对群/频道删除记录的同一契约：群消息没有
// 逐人副本，每条被删消息留一行，范围是 everyone，peer 是频道本身。每个子测试经
// newFixture 取得自己的用户并新建自己的群，可以单独运行。
func ChannelMessageDeletions(t *testing.T, newFixture func(t *testing.T) ChannelMessageDeletionFixture) {
	t.Helper()
	ctx := context.Background()
	now := baseNow()
	type env struct {
		ChannelMessageDeletionFixture
		channel     domain.Channel
		channelPeer domain.Peer
		nextRandom  func() int64
		send        func(t *testing.T, from int64, body string) domain.ChannelMessage
		list        func(t *testing.T, filter domain.MessageDeletionFilter) []domain.MessageDeletion
		latest      func(t *testing.T, actor int64) []domain.MessageDeletion
	}
	setup := func(t *testing.T) env {
		t.Helper()
		f := newFixture(t)
		created, err := f.Channels.CreateChannel(ctx, domain.CreateChannelRequest{
			CreatorUserID: f.Owner,
			Title:         "Deletion log contract",
			Megagroup:     true,
			MemberUserIDs: []int64{f.Member},
			Date:          now,
		})
		if err != nil {
			t.Fatalf("CreateChannel: %v", err)
		}
		e := env{ChannelMessageDeletionFixture: f, channel: created.Channel, channelPeer: domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}}
		random := int64(deletionRandomBase)
		e.nextRandom = func() int64 {
			random++
			return random
		}
		e.send = func(t *testing.T, from int64, body string) domain.ChannelMessage {
			t.Helper()
			res, err := f.Channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
				UserID:    from,
				ChannelID: e.channel.ID,
				RandomID:  e.nextRandom(),
				Message:   body,
				Date:      now,
			})
			if err != nil {
				t.Fatalf("SendChannelMessage: %v", err)
			}
			return res.Message
		}
		e.list = func(t *testing.T, filter domain.MessageDeletionFilter) []domain.MessageDeletion {
			t.Helper()
			got, err := f.Deletions.ListMessageDeletions(ctx, filter)
			if err != nil {
				t.Fatalf("ListMessageDeletions: %v", err)
			}
			return got
		}
		e.latest = func(t *testing.T, actor int64) []domain.MessageDeletion {
			t.Helper()
			return latestBatch(e.list(t, domain.MessageDeletionFilter{ActorUserID: actor, Limit: 50}))
		}
		return e
	}

	for _, tc := range []struct {
		name        string
		skip        bool
		adminRights domain.ChannelAdminRights
		onlyOther   bool
		onlyMissing bool
		invalidID   int
		wantErr     error
		wantOwn     bool
		wantOther   bool
	}{
		{name: "default mixed batch is atomic", wantErr: domain.ErrChannelAdminRequired},
		{name: "skip preserves other authors", skip: true, wantOwn: true},
		{name: "delete admin skip deletes other authors", skip: true, adminRights: domain.ChannelAdminRights{DeleteMessages: true}, wantOwn: true, wantOther: true},
		{name: "non delete admin skip preserves other authors", skip: true, adminRights: domain.ChannelAdminRights{ChangeInfo: true}, wantOwn: true},
		{name: "skip does not hide invalid ids", skip: true, invalidID: -1, wantErr: domain.ErrMessageIDInvalid},
		{name: "skip does not hide out of range ids", skip: true, invalidID: domain.MaxMessageBoxID + 1, wantErr: domain.ErrMessageIDInvalid},
		{name: "default validation failure is atomic", invalidID: -1, onlyMissing: true, wantErr: domain.ErrMessageIDInvalid},
		{name: "skip all unauthorized is a no op", skip: true, onlyOther: true},
		{name: "skip missing messages is a no op", skip: true, onlyMissing: true},
		{name: "default missing messages do not reject own message", wantOwn: true, onlyMissing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t)
			if tc.adminRights != (domain.ChannelAdminRights{}) {
				if _, err := e.Channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{
					UserID: e.Owner, ChannelID: e.channel.ID, MemberID: e.Member, AdminRights: tc.adminRights, Date: now,
				}); err != nil {
					t.Fatalf("promote member: %v", err)
				}
			}
			own := e.send(t, e.Member, "own message")
			other := e.send(t, e.Owner, "other author's message")
			ids := []int{own.ID, other.ID, own.ID, domain.MaxMessageBoxID}
			if tc.onlyOther {
				ids = []int{other.ID, domain.MaxMessageBoxID}
			}
			if tc.onlyMissing {
				ids = []int{domain.MaxMessageBoxID}
				if tc.wantOwn || tc.invalidID != 0 {
					ids = append([]int{own.ID}, ids...)
				}
			}
			if tc.invalidID != 0 {
				ids = append(ids, tc.invalidID)
			}
			before, err := e.Channels.GetChannel(ctx, e.Owner, e.channel.ID)
			if err != nil {
				t.Fatal(err)
			}
			authKeyID := [8]byte{48}
			result, err := e.Channels.DeleteChannelMessages(ctx, domain.DeleteChannelMessagesRequest{
				UserID: e.Member, ChannelID: e.channel.ID, IDs: ids, Date: now + 1,
				SkipUnauthorized: tc.skip, OriginAuthKeyID: authKeyID, OriginSessionID: 48,
			})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("delete error = %v, want %v", err, tc.wantErr)
			}
			var wantDeleted, wantVisible []int
			for _, item := range []struct {
				id      int
				deleted bool
			}{{own.ID, tc.wantOwn}, {other.ID, tc.wantOther}} {
				if item.deleted {
					wantDeleted = append(wantDeleted, item.id)
				} else {
					wantVisible = append(wantVisible, item.id)
				}
			}
			if !slices.Equal(result.DeletedIDs, wantDeleted) {
				t.Fatalf("deleted ids = %v, want %v", result.DeletedIDs, wantDeleted)
			}
			history, err := e.Channels.GetChannelMessages(ctx, e.Owner, e.channel.ID, []int{own.ID, other.ID})
			if err != nil {
				t.Fatal(err)
			}
			var visible []int
			for _, msg := range history.Messages {
				visible = append(visible, msg.ID)
			}
			slices.Sort(visible)
			if !slices.Equal(visible, wantVisible) {
				t.Fatalf("visible messages = %v, want %v", visible, wantVisible)
			}
			after, err := e.Channels.GetChannel(ctx, e.Owner, e.channel.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantPts := before.Channel.Pts + len(wantDeleted)
			if after.Channel.Pts != wantPts {
				t.Fatalf("pts = %d, want %d", after.Channel.Pts, wantPts)
			}
			if len(wantDeleted) == 0 {
				if result.Event.PtsCount != 0 || result.Event.Pts != 0 {
					t.Fatalf("empty delete event = %+v", result.Event)
				}
			} else if result.Event.Pts != wantPts || result.Channel.Pts != wantPts ||
				result.Event.PtsCount != len(wantDeleted) || !slices.Equal(result.Event.MessageIDs, wantDeleted) {
				t.Fatalf("delete event = %+v, want ids %v at pts %d", result.Event, wantDeleted, wantPts)
			}
			diff, err := e.Channels.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{
				UserID: e.Owner, ChannelID: e.channel.ID, Pts: before.Channel.Pts, Limit: 100,
			})
			if err != nil {
				t.Fatal(err)
			}
			wantEvents := 0
			if len(wantDeleted) > 0 {
				wantEvents = 1
			}
			if diff.Pts != wantPts || len(diff.OtherUpdates) != wantEvents || len(diff.NewMessages) != 0 {
				t.Fatalf("durable difference = %+v, want %d deletion event(s) at pts %d", diff, wantEvents, wantPts)
			}
			if wantEvents == 1 && (!slices.Equal(diff.OtherUpdates[0].MessageIDs, wantDeleted) ||
				diff.OtherUpdates[0].PtsCount != len(wantDeleted) || diff.OtherUpdates[0].Pts != wantPts) {
				t.Fatalf("durable deletion = %+v, want ids %v", diff.OtherUpdates[0], wantDeleted)
			}
			rows := e.list(t, domain.MessageDeletionFilter{ActorUserID: e.Member, Limit: 50})
			var audited []int
			for _, row := range rows {
				audited = append(audited, row.MessageID)
				if row.OriginAuthKeyID != authKeyID || row.OriginSessionID != 48 || row.Date != now+1 ||
					row.Source != domain.DeletionSourceUser || row.Scope != domain.DeletionScopeEveryone ||
					row.Peer != e.channelPeer || row.ActorUserID != e.Member {
					t.Fatalf("deletion audit = %+v", row)
				}
			}
			slices.Sort(audited)
			if !slices.Equal(audited, wantDeleted) {
				t.Fatalf("audited ids = %v, want %v", audited, wantDeleted)
			}
			log, err := e.Channels.ListAdminLog(ctx, domain.ChannelAdminLogRequest{
				UserID: e.Owner, ChannelID: e.channel.ID, Filter: domain.ChannelAdminLogFilter{Delete: true}, Limit: 50,
			})
			if err != nil {
				t.Fatal(err)
			}
			var logged []int
			for _, event := range log.Events {
				if event.Type != domain.ChannelAdminLogDeleteMessage || event.UserID != e.Member || event.Message == nil {
					t.Fatalf("delete admin log = %+v", event)
				}
				logged = append(logged, event.Message.ID)
			}
			slices.Sort(logged)
			if !slices.Equal(logged, wantDeleted) {
				t.Fatalf("admin logged ids = %v, want %v", logged, wantDeleted)
			}
			if len(wantDeleted) > 0 {
				repeated, err := e.Channels.DeleteChannelMessages(ctx, domain.DeleteChannelMessagesRequest{
					UserID: e.Member, ChannelID: e.channel.ID, IDs: wantDeleted, SkipUnauthorized: tc.skip, Date: now + 2,
				})
				if err != nil || len(repeated.DeletedIDs) != 0 || repeated.Event.Pts != 0 || repeated.Channel.Pts != wantPts {
					t.Fatalf("repeated deletion = %+v, %v; want no new deletion or pts", repeated, err)
				}
				if afterRows := e.list(t, domain.MessageDeletionFilter{ActorUserID: e.Member, Limit: 50}); len(afterRows) != len(rows) {
					t.Fatalf("repeated deletion audit rows = %+v, want unchanged %+v", afterRows, rows)
				}
			}
		})
	}

	t.Run("a member deleting their own message records one row for everyone", func(t *testing.T) {
		e := setup(t)
		member := e.Member
		msg := e.send(t, member, "my mistake")
		if _, err := e.Channels.DeleteChannelMessages(ctx, domain.DeleteChannelMessagesRequest{
			UserID:    member,
			ChannelID: e.channel.ID,
			IDs:       []int{msg.ID},
			Date:      now + 1,
		}); err != nil {
			t.Fatalf("DeleteChannelMessages: %v", err)
		}

		got := e.latest(t, member)
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
			Peer:         e.channelPeer,
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
		e := setup(t)
		owner, member := e.Owner, e.Member
		first := e.send(t, member, "spam a")
		second := e.send(t, member, "spam b")
		if _, err := e.Channels.DeleteChannelParticipantHistory(ctx, domain.DeleteChannelParticipantHistoryRequest{
			UserID:            owner,
			ChannelID:         e.channel.ID,
			ParticipantUserID: member,
			Date:              now + 2,
		}); err != nil {
			t.Fatalf("DeleteChannelParticipantHistory: %v", err)
		}
		got := e.latest(t, owner)
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
		e := setup(t)
		owner, member := e.Owner, e.Member
		kept := e.send(t, owner, "visible to others")
		if _, err := e.Channels.DeleteChannelHistory(ctx, domain.DeleteChannelHistoryRequest{
			UserID:    member,
			ChannelID: e.channel.ID,
			Date:      now + 3,
		}); err != nil {
			t.Fatalf("DeleteChannelHistory local: %v", err)
		}
		if after := e.list(t, domain.MessageDeletionFilter{ActorUserID: member, Limit: 50}); len(after) != 0 {
			t.Fatalf("local clear wrote deletions %+v; it removes nothing for anyone else", after)
		}

		if _, err := e.Channels.DeleteChannelHistory(ctx, domain.DeleteChannelHistoryRequest{
			UserID:      owner,
			ChannelID:   e.channel.ID,
			ForEveryone: true,
			Date:        now + 4,
		}); err != nil {
			t.Fatalf("DeleteChannelHistory for everyone: %v", err)
		}
		got := e.latest(t, owner)
		if len(got) != 1 || got[0].MessageID != kept.ID || got[0].Source != domain.DeletionSourceHistoryClear ||
			got[0].Body != "visible to others" || got[0].Date != now+4 {
			t.Fatalf("latest batch = %+v, want history_clear of message %d", got, kept.ID)
		}
	})

	t.Run("an explicit system audit is not attributed to the channel owner", func(t *testing.T) {
		e := setup(t)
		owner, member := e.Owner, e.Member
		msg := e.send(t, member, "expiring")
		if _, err := e.Channels.DeleteChannelMessages(ctx, domain.DeleteChannelMessagesRequest{
			UserID:    owner,
			ChannelID: e.channel.ID,
			IDs:       []int{msg.ID},
			Date:      now + 5,
			Audit:     domain.SystemDeletionAudit(domain.DeletionSourceTTL),
		}); err != nil {
			t.Fatalf("DeleteChannelMessages ttl: %v", err)
		}
		got := e.list(t, domain.MessageDeletionFilter{SenderUserID: member, Limit: 50})
		if len(got) != 1 || got[0].MessageID != msg.ID || got[0].Source != domain.DeletionSourceTTL ||
			got[0].ActorType != domain.DeletionActorSystem || got[0].ActorUserID != 0 {
			t.Fatalf("deletions of member's messages = %+v, want only system ttl of %d", got, msg.ID)
		}
		if rows := e.list(t, domain.MessageDeletionFilter{ActorUserID: owner, Limit: 50}); len(rows) != 0 {
			t.Fatalf("system deletion attributed to the owner: %+v", rows)
		}
	})

	for _, tc := range []struct {
		name string
		skip bool
	}{
		{name: "deleting a post records its discussion root as discussion_cascade"},
		{name: "skip deletion preserves discussion cascade", skip: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t)
			owner := e.Owner
			broadcast, err := e.Channels.CreateChannel(ctx, domain.CreateChannelRequest{
				CreatorUserID: owner, Title: "Deletion log broadcast", Broadcast: true, Date: now,
			})
			if err != nil {
				t.Fatalf("CreateChannel broadcast: %v", err)
			}
			group, err := e.Channels.CreateChannel(ctx, domain.CreateChannelRequest{
				CreatorUserID: owner, Title: "Deletion log comments", Megagroup: true, Date: now,
			})
			if err != nil {
				t.Fatalf("CreateChannel discussion: %v", err)
			}
			if _, err := e.Channels.SetDiscussionGroup(ctx, owner, broadcast.Channel.ID, group.Channel.ID); err != nil {
				t.Fatalf("SetDiscussionGroup: %v", err)
			}
			post, err := e.Channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
				UserID: owner, ChannelID: broadcast.Channel.ID, RandomID: e.nextRandom(), Message: "announcement", Date: now,
			})
			if err != nil {
				t.Fatalf("SendChannelMessage post: %v", err)
			}
			if post.Message.Discussion == nil {
				t.Fatalf("post = %+v, want a discussion root in the linked group", post.Message)
			}
			deleted, err := e.Channels.DeleteChannelMessages(ctx, domain.DeleteChannelMessagesRequest{
				UserID: owner, ChannelID: broadcast.Channel.ID, IDs: []int{post.Message.ID}, Date: now + 7, SkipUnauthorized: tc.skip,
			})
			if err != nil {
				t.Fatalf("DeleteChannelMessages post: %v", err)
			}
			if !slices.Equal(deleted.DeletedIDs, []int{post.Message.ID}) || deleted.Event.PtsCount != 1 ||
				len(deleted.DiscussionDeletes) != 1 || deleted.DiscussionDeletes[0].Channel.ID != group.Channel.ID ||
				deleted.DiscussionDeletes[0].Event.PtsCount != 1 ||
				!slices.Equal(deleted.DiscussionDeletes[0].Event.MessageIDs, []int{post.Message.Discussion.MessageID}) {
				t.Fatalf("delete cascade result = %+v, want post and discussion root", deleted)
			}
			roots, err := e.Channels.GetChannelMessages(ctx, owner, group.Channel.ID, []int{post.Message.Discussion.MessageID})
			if err != nil || len(roots.Messages) != 0 {
				t.Fatalf("discussion root after deletion = %+v, %v; want removed", roots, err)
			}

			got := e.list(t, domain.MessageDeletionFilter{ActorUserID: owner, Limit: 50})
			if len(got) != 2 {
				t.Fatalf("deletions = %+v, want the post and its discussion root", got)
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
	}

	t.Run("deleting a forum topic records topic_history", func(t *testing.T) {
		e := setup(t)
		owner, member := e.Owner, e.Member
		forum, err := e.Channels.CreateChannel(ctx, domain.CreateChannelRequest{
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
		topic, err := e.Channels.CreateForumTopic(ctx, domain.CreateChannelForumTopicRequest{
			UserID:    owner,
			ChannelID: forum.Channel.ID,
			Title:     "Doomed topic",
			RandomID:  e.nextRandom(),
			Date:      now,
		})
		if err != nil {
			t.Fatalf("CreateForumTopic: %v", err)
		}
		if _, err := e.Channels.DeleteForumTopicHistory(ctx, domain.DeleteChannelForumTopicHistoryRequest{
			UserID:    owner,
			ChannelID: forum.Channel.ID,
			TopicID:   topic.Topic.TopicID,
			Date:      now + 6,
		}); err != nil {
			t.Fatalf("DeleteForumTopicHistory: %v", err)
		}
		got := e.latest(t, owner)
		if len(got) != 1 || got[0].Source != domain.DeletionSourceTopicHistory ||
			got[0].Peer != (domain.Peer{Type: domain.PeerTypeChannel, ID: forum.Channel.ID}) || got[0].MessageID != topic.Message.ID {
			t.Fatalf("latest batch = %+v, want topic_history of root %d", got, topic.Message.ID)
		}
	})
}
