package storetest

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// BotForum verifies the state and event boundaries used by both protocol adapters.
func BotForum(t *testing.T, s store.ChannelStore, reopen func() store.ChannelStore, users []int64) {
	t.Helper()
	ctx := context.Background()
	owner, manager, pinner, member := users[0], users[1], users[2], users[3]
	created, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Forum contract", Megagroup: true, Forum: true, MemberUserIDs: users[1:], Date: 1700000100})
	if err != nil {
		t.Fatal(err)
	}
	channel := created.Channel.ID
	for _, admin := range []struct {
		id     int64
		rights domain.ChannelAdminRights
	}{{manager, domain.ChannelAdminRights{ManageTopics: true}}, {pinner, domain.ChannelAdminRights{PinMessages: true}}} {
		if _, err := s.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: owner, ChannelID: channel, MemberID: admin.id, AdminRights: admin.rights, Date: 1700000101}); err != nil {
			t.Fatal(err)
		}
	}
	create := func(name string, rid int64) domain.ChannelForumTopic {
		t.Helper()
		res, err := s.CreateForumTopic(ctx, domain.CreateChannelForumTopicRequest{UserID: owner, ChannelID: channel, Title: name, RandomID: rid, Date: 1700000102})
		if err != nil {
			t.Fatal(err)
		}
		if res.Topic.TopicID <= 1 || res.Topic.TopicID != res.Message.ID || res.Event.Message.Action == nil || res.Event.Message.Action.Type != domain.ChannelActionTopicCreate {
			t.Fatalf("invalid topic identity/event: %+v", res)
		}
		return res.Topic
	}
	a, b := create("Alpha", 1001), create("Beta", 1002)
	// General history exists both without a thread marker and with top_id=1.
	// A reply's actual destination wins over a forged client topic hint.
	generalTargets := make([]int, 0, 2)
	for i, thread := range []int{0, domain.ForumGeneralTopicID} {
		req := domain.SendChannelMessageRequest{UserID: owner, ChannelID: channel, RandomID: int64(1800 + i), Message: "General history", Date: 1700000103}
		if thread != 0 {
			req.ReplyTo = &domain.MessageReply{TopMessageID: thread, ForumTopic: true}
		}
		sent, err := s.SendChannelMessage(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		generalTargets = append(generalTargets, sent.Message.ID)
	}
	replyAttempt := int64(1900)
	assertGeneralRepliesBlocked := func() {
		t.Helper()
		for _, id := range generalTargets {
			for _, hint := range []int{0, b.TopicID} {
				replyAttempt++
				_, err := s.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: member, ChannelID: channel, RandomID: replyAttempt, Message: "reply into General", ReplyTo: &domain.MessageReply{MessageID: id, TopMessageID: hint}, Date: 1700000106})
				if !errors.Is(err, domain.ErrChannelWriteForbidden) {
					t.Fatalf("General reply id=%d client hint=%d bypassed state: %v", id, hint, err)
				}
			}
		}
	}
	title := "Renamed Alpha"
	if _, err := s.EditForumTopic(ctx, domain.EditChannelForumTopicRequest{UserID: pinner, ChannelID: channel, TopicID: a.TopicID, Title: &title, Date: 1700000103}); !errors.Is(err, domain.ErrChannelAdminRequired) {
		t.Fatalf("pin right authorized topic management: %v", err)
	}
	closed := true
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for _, req := range []domain.EditChannelForumTopicRequest{{UserID: manager, ChannelID: channel, TopicID: a.TopicID, Title: &title, Date: 1700000104}, {UserID: manager, ChannelID: channel, TopicID: a.TopicID, Closed: &closed, Date: 1700000105}} {
		wg.Add(1)
		go func(req domain.EditChannelForumTopicRequest) {
			defer wg.Done()
			res, err := s.EditForumTopic(ctx, req)
			if err == nil && (res.Message.Action == nil || res.Event.PtsCount != 1) {
				err = errors.New("missing topic edit service event")
			}
			failures <- err
		}(req)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	s = reopen()
	topics, err := s.GetForumTopicsByID(ctx, owner, channel, []int{a.TopicID})
	if err != nil || len(topics.Topics) != 1 || topics.Topics[0].Title != title || !topics.Topics[0].Closed {
		t.Fatalf("lost concurrent metadata: %+v, %v", topics, err)
	}
	if _, err := s.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: member, ChannelID: channel, RandomID: 2000, Message: "closed topic", ReplyTo: &domain.MessageReply{TopMessageID: a.TopicID, ForumTopic: true}, Date: 1700000106}); !errors.Is(err, domain.ErrChannelWriteForbidden) {
		t.Fatalf("member posted in closed topic: %v", err)
	}
	hidden := true
	if _, err := s.EditForumTopic(ctx, domain.EditChannelForumTopicRequest{UserID: manager, ChannelID: channel, TopicID: a.TopicID, Hidden: &hidden, Date: 1700000107}); !errors.Is(err, domain.ErrChannelInvalid) {
		t.Fatalf("hid non-General: %v", err)
	}
	for _, user := range []int64{pinner, member} {
		if _, err := s.EditForumTopic(ctx, domain.EditChannelForumTopicRequest{UserID: user, ChannelID: channel, TopicID: 1, Hidden: &hidden, Date: 1700000108}); !errors.Is(err, domain.ErrChannelAdminRequired) {
			t.Fatalf("non-manager hid General: %v", err)
		}
	}
	editGeneral := func(name *string, close, hide *bool) domain.EditChannelForumTopicResult {
		t.Helper()
		res, err := s.EditForumTopic(ctx, domain.EditChannelForumTopicRequest{UserID: manager, ChannelID: channel, TopicID: 1, Title: name, Closed: close, Hidden: hide, Date: 1700000109})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	generalName := "Lobby"
	editGeneral(&generalName, nil, &hidden)
	s = reopen()
	general, err := s.GeneralForumTopic(ctx, member, channel)
	if err != nil || general.Title != generalName || !general.Hidden || !general.Closed {
		t.Fatalf("hidden General state: %+v, %v", general, err)
	}
	if _, err := s.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: member, ChannelID: channel, RandomID: 2001, Message: "hidden General", Date: 1700000110}); !errors.Is(err, domain.ErrChannelWriteForbidden) {
		t.Fatalf("member posted in hidden General: %v", err)
	}
	assertGeneralRepliesBlocked()
	if _, err := s.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: member, ChannelID: channel, RandomID: 2100, Message: "Beta is still open", ReplyTo: &domain.MessageReply{TopMessageID: b.TopicID, ForumTopic: true}, Date: 1700000110}); err != nil {
		t.Fatalf("General hidden blocked another topic: %v", err)
	}
	hidden = false
	unhidden := editGeneral(nil, nil, &hidden)
	if unhidden.Topic.Hidden || !unhidden.Topic.Closed {
		t.Fatalf("unhide reopened General: %+v", unhidden.Topic)
	}
	assertGeneralRepliesBlocked()
	hidden = true
	editGeneral(nil, nil, &hidden)
	closed = false
	opened := editGeneral(nil, &closed, nil)
	if opened.Topic.Hidden || opened.Topic.Closed || opened.Message.Action.Closed == nil || opened.Message.Action.Hidden == nil {
		t.Fatalf("reopen did not unhide General/event: %+v", opened)
	}
	for i, id := range generalTargets {
		sent, err := s.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: member, ChannelID: channel, RandomID: int64(2200 + i), Message: "reply after reopen", ReplyTo: &domain.MessageReply{MessageID: id, TopMessageID: b.TopicID}, Date: 1700000110})
		if err != nil || sent.Message.ReplyTo == nil || sent.Message.ReplyTo.TopMessageID != domain.ForumGeneralTopicID || !sent.Message.ReplyTo.ForumTopic {
			t.Fatalf("reopened General reply resolved wrong destination: %+v, %v", sent.Message.ReplyTo, err)
		}
	}
	if _, err := s.DeleteForumTopicHistory(ctx, domain.DeleteChannelForumTopicHistoryRequest{UserID: owner, ChannelID: channel, TopicID: 1}); !errors.Is(err, domain.ErrMessageIDInvalid) {
		t.Fatalf("deleted General: %v", err)
	}
	list, err := s.ListForumTopics(ctx, member, domain.ChannelForumTopicFilter{ChannelID: channel, Limit: 100})
	if err != nil || list.Count != 2 {
		t.Fatalf("General metadata duplicated topic list: %+v, %v", list, err)
	}
	send := func(topic int, rid int64) int {
		t.Helper()
		req := domain.SendChannelMessageRequest{UserID: owner, ChannelID: channel, RandomID: rid, Message: "pin candidate", Date: 1700000111}
		if topic != 0 {
			req.ReplyTo = &domain.MessageReply{TopMessageID: topic, ForumTopic: true}
		}
		res, err := s.SendChannelMessage(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpdatePinnedMessage(ctx, domain.UpdateChannelPinnedMessageRequest{UserID: owner, ChannelID: channel, MessageID: res.Message.ID, Pinned: true, Date: 1700000112}); err != nil {
			t.Fatal(err)
		}
		return res.Message.ID
	}
	aPin, bPin, gPin := send(a.TopicID, 3001), send(b.TopicID, 3002), send(0, 3003)
	unpin, err := s.UnpinAllChannelMessages(ctx, domain.UnpinAllChannelMessagesRequest{UserID: pinner, ChannelID: channel, TopicID: a.TopicID, Date: 1700000113})
	if err != nil || !reflect.DeepEqual(unpin.Event.MessageIDs, []int{aPin}) || unpin.Event.Pinned || unpin.Channel.PinnedMessageID != gPin {
		t.Fatalf("topic unpin escaped scope: %+v, %v", unpin, err)
	}
	unpin, err = s.UnpinAllChannelMessages(ctx, domain.UnpinAllChannelMessagesRequest{UserID: pinner, ChannelID: channel, TopicID: 1, Date: 1700000114})
	if err != nil || !reflect.DeepEqual(unpin.Event.MessageIDs, []int{gPin}) || unpin.Channel.PinnedMessageID != bPin {
		t.Fatalf("General unpin escaped scope: %+v, %v", unpin, err)
	}
	if _, err := s.UnpinAllChannelMessages(ctx, domain.UnpinAllChannelMessagesRequest{UserID: pinner, ChannelID: channel, TopicID: 999999}); !errors.Is(err, domain.ErrMessageIDInvalid) {
		t.Fatalf("unpin missing topic: %v", err)
	}
	deleted, err := s.DeleteForumTopicHistory(ctx, domain.DeleteChannelForumTopicHistoryRequest{UserID: owner, ChannelID: channel, TopicID: a.TopicID, Date: 1700000115})
	if err != nil || deleted.Offset != 0 {
		t.Fatalf("delete topic: %+v, %v", deleted, err)
	}
	s = reopen()
	remaining, err := s.GetChannelMessages(ctx, owner, channel, []int{aPin, bPin, gPin})
	if err != nil {
		t.Fatal(err)
	}
	found := map[int]bool{}
	for _, message := range remaining.Messages {
		if !message.Deleted {
			found[message.ID] = true
		}
		if message.ID == bPin && !message.Pinned {
			t.Fatal("deleting Alpha cleared Beta pin")
		}
	}
	if found[aPin] || !found[bPin] || !found[gPin] {
		t.Fatalf("delete escaped topic: %v", found)
	}
}
