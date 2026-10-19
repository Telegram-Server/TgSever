package storetest

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// PremiumBoostLists 校验 boost 列表在各后端的共同契约（#40）：store 只返回 boost
// 记录，不携带用户行（domain.PremiumBoostList / PremiumMyBoosts 已没有 Users 字段，
// 早先 postgres 在这里带出含手机号的原始 users 行）。boost 用户由 rpc 层按查看者做
// 可见性投影（手机号、头像隐私、已注销）后再输出，所以各后端返回的 boost 必须带
// booster 的 user id，且计数、过滤与管理员门槛一致。
// users：群主、booster A、booster B。
func PremiumBoostLists(t *testing.T, s store.ChannelStore, users [3]int64) {
	t.Helper()
	ctx := context.Background()
	owner, a, b := users[0], users[1], users[2]
	now := baseNow()
	premiumUntil := now + 3600
	created, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Boost lists", Broadcast: true, MemberUserIDs: []int64{a, b}, Date: now})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Channel.ID
	for _, booster := range []int64{a, b} {
		if _, err := s.ApplyPremiumBoost(ctx, booster, id, []int{domain.DefaultPremiumBoostSlotID}, now, premiumUntil); err != nil {
			t.Fatalf("apply boost as %d: %v", booster, err)
		}
	}

	list, err := s.ListPremiumBoosts(ctx, owner, id, false, "", domain.MaxPremiumBoostsListLimit, now)
	if err != nil {
		t.Fatalf("list boosts: %v", err)
	}
	if list.Count != 2 || !sameIDs(boostUserIDs(list.Boosts), []int64{a, b}) {
		t.Fatalf("list boosts = count %d users %v, want 2 boosts from %v", list.Count, boostUserIDs(list.Boosts), []int64{a, b})
	}

	userBoosts, err := s.GetPremiumUserBoosts(ctx, owner, id, a, now)
	if err != nil {
		t.Fatalf("get user boosts: %v", err)
	}
	if userBoosts.Count != 1 || !sameIDs(boostUserIDs(userBoosts.Boosts), []int64{a}) {
		t.Fatalf("user boosts = count %d users %v, want one boost from %d", userBoosts.Count, boostUserIDs(userBoosts.Boosts), a)
	}

	my, err := s.GetPremiumMyBoosts(ctx, a, now, premiumUntil)
	if err != nil {
		t.Fatalf("get my boosts: %v", err)
	}
	if !sameIDs(boostUserIDs(my.Slots), []int64{a}) || len(my.Channels) != 1 || my.Channels[0].ID != id {
		t.Fatalf("my boosts = slots %v channels %+v, want own slot on channel %d", boostUserIDs(my.Slots), my.Channels, id)
	}

	// 非管理员不能读 boost 列表。
	if _, err := s.ListPremiumBoosts(ctx, a, id, false, "", domain.MaxPremiumBoostsListLimit, now); !errors.Is(err, domain.ErrChannelAdminRequired) {
		t.Fatalf("member list boosts err = %v, want ErrChannelAdminRequired", err)
	}
	if _, err := s.GetPremiumUserBoosts(ctx, a, id, b, now); !errors.Is(err, domain.ErrChannelAdminRequired) {
		t.Fatalf("member get user boosts err = %v, want ErrChannelAdminRequired", err)
	}
}

func boostUserIDs(slots []domain.PremiumBoostSlot) []int64 {
	out := make([]int64, 0, len(slots))
	for _, slot := range slots {
		out = append(out, slot.UserID)
	}
	return out
}
