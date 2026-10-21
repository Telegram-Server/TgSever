package storetest

import (
	"context"
	"slices"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// ChannelMemberPagerStore 是契约用到的频道存储：分页接口，加上建群与退群。
type ChannelMemberPagerStore interface {
	store.ChannelMemberPager
	CreateChannel(ctx context.Context, req domain.CreateChannelRequest) (domain.CreateChannelResult, error)
	LeaveChannel(ctx context.Context, channelID, userID int64, date int) (domain.CreateChannelResult, error)
}

// ChannelMemberPagerFixture 提供频道存储与一组可用作成员的用户 id（postgres 下是已存在的用户）。
type ChannelMemberPagerFixture struct {
	Channels ChannelMemberPagerStore
	// Users 至少 6 个互不相同的用户 id。
	Users []int64
}

// ChannelMemberPagerContract 是频道成员 keyset 分页的契约：按 user_id 升序、只返回 afterUserID
// 之后的、每页最多 limit 个；只含活跃成员（退群的不在内）；不混入其他频道的成员。
func ChannelMemberPagerContract(t *testing.T, newFixture func(t *testing.T) ChannelMemberPagerFixture) {
	t.Helper()
	ctx := context.Background()

	t.Run("pages active members in user id order", func(t *testing.T) {
		f := newFixture(t)
		if len(f.Users) < 6 {
			t.Fatalf("fixture has %d users, want at least 6", len(f.Users))
		}
		creator, members, outsider := f.Users[0], f.Users[1:5], f.Users[5]
		created, err := f.Channels.CreateChannel(ctx, domain.CreateChannelRequest{
			CreatorUserID: creator, Title: "pager", Megagroup: true, MemberUserIDs: members, Date: 1_700_000_100,
		})
		if err != nil {
			t.Fatalf("create channel: %v", err)
		}
		if _, err := f.Channels.CreateChannel(ctx, domain.CreateChannelRequest{
			CreatorUserID: outsider, Title: "other", Megagroup: true, Date: 1_700_000_101,
		}); err != nil {
			t.Fatalf("create other channel: %v", err)
		}
		left := members[1]
		if _, err := f.Channels.LeaveChannel(ctx, created.Channel.ID, left, 1_700_000_200); err != nil {
			t.Fatalf("leave channel: %v", err)
		}
		want := []int64{creator}
		for _, id := range members {
			if id != left {
				want = append(want, id)
			}
		}
		slices.Sort(want)

		var got []int64
		after := int64(0)
		for pages := 0; ; pages++ {
			if pages > len(want)+1 {
				t.Fatalf("paging did not terminate: %v", got)
			}
			page, err := f.Channels.ListActiveChannelMemberIDsAfter(ctx, created.Channel.ID, after, 2)
			if err != nil {
				t.Fatalf("list page after %d: %v", after, err)
			}
			if len(page) > 2 {
				t.Fatalf("page after %d = %v, want at most 2", after, page)
			}
			if len(page) == 0 {
				break
			}
			got = append(got, page...)
			after = page[len(page)-1]
		}
		if !slices.Equal(got, want) {
			t.Fatalf("paged members = %v, want active members %v in order", got, want)
		}
	})

	t.Run("unknown channel has no members", func(t *testing.T) {
		f := newFixture(t)
		page, err := f.Channels.ListActiveChannelMemberIDsAfter(ctx, 9_000_000_000_001, 0, 10)
		if err != nil {
			t.Fatalf("list unknown channel: %v", err)
		}
		if len(page) != 0 {
			t.Fatalf("unknown channel members = %v, want none", page)
		}
	})
}
