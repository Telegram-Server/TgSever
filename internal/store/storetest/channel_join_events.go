package storetest

import (
	"context"
	"errors"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"testing"
)

func ChannelJoinEventSnapshots(t *testing.T, s store.ChannelStore, users [4]int64) {
	t.Helper()
	ctx := context.Background()
	owner, first, second, public := users[0], users[1], users[2], users[3]
	created, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Join snapshots", Megagroup: true, Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Channel.ID
	snapshots := make(map[int64]*domain.BotChatJoinRequest)
	hashes := make(map[int64]string)
	for _, userID := range []int64{first, second} {
		link, err := s.ExportInvite(ctx, domain.ExportChannelInviteRequest{UserID: owner, ChannelID: id, Title: "Campaign", RequestNeeded: true, Date: baseNow()})
		if err != nil {
			t.Fatal(err)
		}
		res, err := s.ImportInvite(ctx, domain.ImportChannelInviteRequest{UserID: userID, Hash: link.Invite.Hash, Date: baseNow()})
		if !errors.Is(err, domain.ErrInviteRequestSent) || res.JoinRequest == nil || res.JoinRequest.UserID != userID || res.JoinRequest.Invite == nil || res.JoinRequest.Invite.Hash != link.Invite.Hash || res.JoinRequest.Invite.RequestedCount != 1 {
			t.Fatalf("request=%+v err=%v", res.JoinRequest, err)
		}
		snapshots[userID], hashes[userID] = res.JoinRequest, link.Invite.Hash
	}
	approved, err := s.HideAllChatJoinRequests(ctx, domain.HideChannelJoinRequestsRequest{UserID: owner, ChannelID: id, Approved: true, Date: baseNow() + 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(approved.Members) != 2 || len(approved.Joins) != 2 {
		t.Fatalf("approved members=%+v joins=%+v", approved.Members, approved.Joins)
	}
	for _, userID := range []int64{first, second} {
		join := approved.Joins[userID]
		if join.Invite == nil || join.Invite.Hash != hashes[userID] || !join.ViaJoinRequest || join.ApprovedBy != owner || join.Message.Action == nil || join.Message.Action.Type != domain.ChannelActionChatJoinedByRequest || join.Event.Pts <= 0 || approved.PreviousMember(id, userID).Status != domain.ChannelMemberLeft {
			t.Fatalf("join=%+v", join)
		}
		invite, err := s.GetExportedInvite(ctx, domain.GetChannelInviteRequest{UserID: owner, ChannelID: id, Hash: hashes[userID]})
		if err != nil || invite.RequestedCount != 0 || invite.UsageCount != 1 {
			t.Fatalf("invite=%+v err=%v", invite, err)
		}
		if snapshots[userID].Invite.RequestedCount != 1 {
			t.Fatalf("request snapshot mutated=%+v", snapshots[userID])
		}
	}
	firstJoin, secondJoin := approved.Joins[first], approved.Joins[second]
	if firstJoin.Message.ID <= 0 || secondJoin.Message.ID <= 0 || firstJoin.Message.ID == secondJoin.Message.ID || firstJoin.Event.Pts == secondJoin.Event.Pts {
		t.Fatalf("batch service messages reuse identity: first=%+v second=%+v", firstJoin, secondJoin)
	}
	history, err := s.GetChannelMessages(ctx, owner, id, []int{firstJoin.Message.ID, secondJoin.Message.ID})
	if err != nil || len(history.Messages) != 2 {
		t.Fatalf("persisted batch messages=%+v err=%v", history.Messages, err)
	}
	senders := make(map[int64]bool)
	for _, message := range history.Messages {
		if message.Action == nil || message.Action.Type != domain.ChannelActionChatJoinedByRequest {
			t.Fatalf("batch action=%+v", message.Action)
		}
		senders[message.SenderUserID] = true
	}
	if !senders[first] || !senders[second] {
		t.Fatalf("batch messages lost applicant: %v", senders)
	}
	if _, err := s.UpdateUsername(ctx, domain.UpdateChannelUsernameRequest{UserID: owner, ChannelID: id, Username: "join_events"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetJoinRequest(ctx, owner, id, true); err != nil {
		t.Fatal(err)
	}
	requested, err := s.JoinChannel(ctx, id, public, baseNow()+2)
	if !errors.Is(err, domain.ErrInviteRequestSent) || requested.JoinRequest == nil || requested.JoinRequest.Invite != nil {
		t.Fatalf("public=%+v err=%v", requested.JoinRequest, err)
	}
	result, err := s.HideChatJoinRequest(ctx, domain.HideChannelJoinRequestRequest{UserID: owner, ChannelID: id, TargetUserID: public, Approved: true, Date: baseNow() + 3})
	if err != nil {
		t.Fatal(err)
	}
	join := result.Joins[public]
	if join.Invite != nil || !join.ViaJoinRequest || join.Message.Action == nil || join.Message.Action.Type != domain.ChannelActionChatJoinedByRequest {
		t.Fatalf("public approval=%+v", join)
	}
}
