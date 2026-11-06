package storetest

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// ChannelAdmitSkipsJoinRequest 校验系统直接加入（AdmitToChannel，新账号默认入驻用）：开启了入群
// 审批的群也直接加入、不留入群申请，群里照常出现"加入了群组"的服务消息；已在群里返回
// ErrUserAlreadyParticipant，被踢的成员返回 ErrChannelUserBanned。users：群主、新成员、被踢的成员。
func ChannelAdmitSkipsJoinRequest(t *testing.T, s store.ChannelStore, users [3]int64) {
	t.Helper()
	ctx := context.Background()
	owner, newcomer, kicked := users[0], users[1], users[2]
	created, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Admit", Megagroup: true, MemberUserIDs: []int64{kicked}, Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Channel.ID
	if _, err := s.UpdateUsername(ctx, domain.UpdateChannelUsernameRequest{UserID: owner, ChannelID: id, Username: fmt.Sprintf("admit%d", id)}); err != nil {
		t.Fatalf("set username: %v", err)
	}
	if _, err := s.SetJoinRequest(ctx, owner, id, true); err != nil {
		t.Fatalf("enable join requests: %v", err)
	}

	res, err := s.AdmitToChannel(ctx, id, newcomer, baseNow()+1)
	if err != nil {
		t.Fatalf("admit to a join-request channel: %v", err)
	}
	if len(res.Members) != 1 || res.Members[0].UserID != newcomer || res.Members[0].Status != domain.ChannelMemberActive {
		t.Fatalf("admit members = %+v, want the newcomer active", res.Members)
	}
	if res.Message.Action.Type != domain.ChannelActionChatJoined {
		t.Fatalf("admit service message = %+v, want chat joined", res.Message)
	}
	member, err := s.GetParticipant(ctx, owner, id, newcomer)
	if err != nil || member.Status != domain.ChannelMemberActive {
		t.Fatalf("newcomer participant = %+v err=%v, want active", member, err)
	}
	pending, err := s.ListInviteImporters(ctx, domain.ChannelInviteImportersRequest{UserID: owner, ChannelID: id, Requested: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(pending.Importers) != 0 {
		t.Fatalf("pending join requests = %+v, want none", pending.Importers)
	}

	if _, err := s.AdmitToChannel(ctx, id, newcomer, baseNow()+2); !errors.Is(err, domain.ErrUserAlreadyParticipant) {
		t.Fatalf("admit an active member err=%v, want ErrUserAlreadyParticipant", err)
	}
	if _, err := s.EditChannelBanned(ctx, domain.EditChannelBannedRequest{
		UserID: owner, ChannelID: id, Participant: domain.Peer{Type: domain.PeerTypeUser, ID: kicked}, Date: baseNow() + 3,
		BannedRights: domain.ChannelBannedRights{ViewMessages: true},
	}); err != nil {
		t.Fatalf("kick member: %v", err)
	}
	if _, err := s.AdmitToChannel(ctx, id, kicked, baseNow()+4); !errors.Is(err, domain.ErrChannelUserBanned) {
		t.Fatalf("admit a kicked member err=%v, want ErrChannelUserBanned", err)
	}
}
