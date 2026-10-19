package storetest

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// PaidMonoforumFixture 是新建的母频道群主与两个订阅者。星星余额不在 ChannelStore 接口里，
// 由实现各自注入：GrantStars 给用户发放余额（内存实现直接写余额表，Postgres 走 EnsureGrant），
// StarsBalances 读出用户余额与频道（母广播频道）收入余额。
type PaidMonoforumFixture struct {
	Channels          store.ChannelStore
	Owner, Sub, Other int64
	GrantStars        func(t *testing.T, userID, stars int64)
	StarsBalances     func(t *testing.T, userID, channelID int64) (user, channel int64)
}

const (
	paidMonoforumPrice = 10
	// paidMonoforumChannelShare 是价格扣掉平台抽成后频道实收的星星。
	paidMonoforumChannelShare = 8
	paidMonoforumBaseDate     = 1_700_002_000
)

type paidMonoforumEnv struct {
	PaidMonoforumFixture
	broadcastID, monoID int64
	subPeer, otherPeer  domain.Peer
}

func newPaidMonoforumEnv(t *testing.T, f PaidMonoforumFixture) *paidMonoforumEnv {
	t.Helper()
	ctx := context.Background()
	broadcast, err := f.Channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: f.Owner, Title: "Paid DM", Broadcast: true, Date: paidMonoforumBaseDate})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	enabled, err := f.Channels.SetPaidMessagesPrice(ctx, f.Owner, broadcast.Channel.ID, paidMonoforumPrice, true)
	if err != nil {
		t.Fatalf("enable paid DM: %v", err)
	}
	if enabled.Channel.LinkedMonoforumID == 0 {
		t.Fatal("no monoforum created")
	}
	return &paidMonoforumEnv{
		PaidMonoforumFixture: f,
		broadcastID:          broadcast.Channel.ID,
		monoID:               enabled.Channel.LinkedMonoforumID,
		subPeer:              domain.Peer{Type: domain.PeerTypeUser, ID: f.Sub},
		otherPeer:            domain.Peer{Type: domain.PeerTypeUser, ID: f.Other},
	}
}

func (e *paidMonoforumEnv) expectBalances(t *testing.T, step string, user, wantUser, wantChannel int64) {
	t.Helper()
	gotUser, gotChannel := e.StarsBalances(t, user, e.broadcastID)
	if gotUser != wantUser || gotChannel != wantChannel {
		t.Fatalf("%s: user/channel balance = %d/%d, want %d/%d", step, gotUser, gotChannel, wantUser, wantChannel)
	}
}

// MonoforumPaidMessages 校验付费私信的账本：授权额不足时要求付款且不写消息；够额时按实际价格扣款、
// 频道得分成；同一 random_id 重放不重复扣款；管理员回复免费；余额不足时账本不变。
// 每个子测试经 newFixture 新建自己的用户与频道。
func MonoforumPaidMessages(t *testing.T, newFixture func(t *testing.T) PaidMonoforumFixture) {
	t.Helper()
	ctx := context.Background()

	t.Run("low authorization requires payment and writes nothing", func(t *testing.T) {
		e := newPaidMonoforumEnv(t, newFixture(t))
		e.GrantStars(t, e.Sub, 25)
		var required *domain.StarsPaymentRequiredError
		if _, err := e.Channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{
			MonoforumID: e.monoID, SenderUserID: e.Sub, SavedPeer: e.subPeer, RandomID: 3001, Message: "too low", AllowPaidStars: paidMonoforumPrice - 1, Date: paidMonoforumBaseDate + 1,
		}); !errors.As(err, &required) || required.Stars != paidMonoforumPrice {
			t.Fatalf("low authorization err = %v, want %d-Star payment required", err, paidMonoforumPrice)
		}
		hist, err := e.Channels.ListMonoforumHistory(ctx, domain.MonoforumHistoryFilter{MonoforumID: e.monoID, SavedPeer: e.subPeer, Limit: 10})
		if err != nil || hist.Count != 0 {
			t.Fatalf("low authorization history = %d, %v; want no message", hist.Count, err)
		}
		e.expectBalances(t, "low authorization", e.Sub, 25, 0)
	})

	t.Run("paid send charges once", func(t *testing.T) {
		e := newPaidMonoforumEnv(t, newFixture(t))
		e.GrantStars(t, e.Sub, 25)
		req := domain.SendMonoforumMessageRequest{MonoforumID: e.monoID, SenderUserID: e.Sub, SavedPeer: e.subPeer, RandomID: 3002, Message: "paid", AllowPaidStars: 99, Date: paidMonoforumBaseDate + 2}
		paid, err := e.Channels.SendMonoforumMessage(ctx, req)
		if err != nil {
			t.Fatalf("paid send: %v", err)
		}
		if paid.Message.PaidMessageStars != paidMonoforumPrice || paid.SenderStarsBalance == nil || paid.SenderStarsBalance.Balance != 15 {
			t.Fatalf("paid result = %+v balance=%+v, want actual %d and balance 15", paid.Message, paid.SenderStarsBalance, paidMonoforumPrice)
		}
		e.expectBalances(t, "paid send", e.Sub, 15, paidMonoforumChannelShare)

		replay, err := e.Channels.SendMonoforumMessage(ctx, req)
		if err != nil {
			t.Fatalf("paid replay: %v", err)
		}
		if !replay.Duplicate || replay.Message.ID != paid.Message.ID || replay.SenderStarsBalance == nil || replay.SenderStarsBalance.Balance != 15 {
			t.Fatalf("paid replay = %+v, want original message and balance 15", replay)
		}
		e.expectBalances(t, "paid replay", e.Sub, 15, paidMonoforumChannelShare)
	})

	t.Run("admin reply is free", func(t *testing.T) {
		e := newPaidMonoforumEnv(t, newFixture(t))
		admin, err := e.Channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{
			MonoforumID: e.monoID, SenderUserID: e.Owner, SavedPeer: e.subPeer, RandomID: 3003, Message: "free admin reply", AllowPaidStars: 100, Date: paidMonoforumBaseDate + 3,
		})
		if err != nil {
			t.Fatalf("admin reply: %v", err)
		}
		if admin.Message.PaidMessageStars != 0 || admin.SenderStarsBalance != nil {
			t.Fatalf("admin reply charged: message=%+v balance=%+v", admin.Message, admin.SenderStarsBalance)
		}
		e.expectBalances(t, "admin reply", e.Owner, 0, 0)
	})

	t.Run("insufficient balance leaves the ledger alone", func(t *testing.T) {
		e := newPaidMonoforumEnv(t, newFixture(t))
		e.GrantStars(t, e.Other, 5)
		if _, err := e.Channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{
			MonoforumID: e.monoID, SenderUserID: e.Other, SavedPeer: e.otherPeer, RandomID: 3004, Message: "insufficient", AllowPaidStars: paidMonoforumPrice, Date: paidMonoforumBaseDate + 4,
		}); !errors.Is(err, domain.ErrStarsInsufficient) {
			t.Fatalf("insufficient err = %v, want ErrStarsInsufficient", err)
		}
		e.expectBalances(t, "insufficient", e.Other, 5, 0)
	})
}
