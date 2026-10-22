package storetest

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// ChannelHideAllJoinRequestsExclude 校验批量处理入群申请时 ExcludeUserIDs 里的申请人被跳过：
// 不批准、不占 Limit、申请保持待处理，之后仍可被批准。users：群主、申请人 X（先申请）、申请人 Y。
func ChannelHideAllJoinRequestsExclude(t *testing.T, s store.ChannelStore, users [3]int64) {
	t.Helper()
	ctx := context.Background()
	owner, x, y := users[0], users[1], users[2]
	created, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Exclude applicants", Megagroup: true, Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Channel.ID
	invite, err := s.ExportInvite(ctx, domain.ExportChannelInviteRequest{UserID: owner, ChannelID: id, RequestNeeded: true, Title: "exclude", Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	for i, applicant := range []int64{x, y} {
		if _, err := s.ImportInvite(ctx, domain.ImportChannelInviteRequest{UserID: applicant, Hash: invite.Invite.Hash, Date: baseNow() + i}); !errors.Is(err, domain.ErrInviteRequestSent) {
			t.Fatalf("request %d = %v, want ErrInviteRequestSent", applicant, err)
		}
	}
	activeMembers := func(res domain.CreateChannelResult) map[int64]bool {
		out := map[int64]bool{}
		for _, m := range res.Members {
			if m.Status == domain.ChannelMemberActive {
				out[m.UserID] = true
			}
		}
		return out
	}

	// Limit 1：X 先申请但被排除，不能占掉这一个名额。
	first, err := s.HideAllChatJoinRequests(ctx, domain.HideChannelJoinRequestsRequest{UserID: owner, ChannelID: id, Approved: true, Limit: 1, Date: baseNow() + 5, ExcludeUserIDs: []int64{x}})
	if err != nil {
		t.Fatal(err)
	}
	if got := activeMembers(first); len(got) != 1 || !got[y] {
		t.Fatalf("approve all excluding X: members = %+v, want only Y", first.Members)
	}
	pending, err := s.ListInviteImporters(ctx, domain.ChannelInviteImportersRequest{UserID: owner, ChannelID: id, Requested: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(pending.Importers) != 1 || pending.Importers[0].UserID != x {
		t.Fatalf("pending after exclude = %+v, want X still pending", pending.Importers)
	}

	second, err := s.HideAllChatJoinRequests(ctx, domain.HideChannelJoinRequestsRequest{UserID: owner, ChannelID: id, Approved: true, Limit: 10, Date: baseNow() + 6})
	if err != nil {
		t.Fatal(err)
	}
	if got := activeMembers(second); len(got) != 1 || !got[x] {
		t.Fatalf("approve all without exclude: members = %+v, want X", second.Members)
	}
}
