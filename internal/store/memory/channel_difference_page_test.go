package memory

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// 客户端 limit 超过服务端单页上限时分页返回 final=false；只有 pts 差距超过客户端自己的
// limit 才 tooLong（TDLib bot 用 limit=100000，td/td/telegram/MessagesManager.h:1676-1678）。
func TestChannelDifferenceLargeClientLimitPagesInsteadOfTooLong(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	created, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1, Title: "difference pages", Megagroup: true, Date: 1_700_000_100,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	startPts := created.Channel.Pts
	const sent = domain.MaxChannelDifferenceLimit + 20
	channelPts := startPts
	for i := 0; i < sent; i++ {
		res, err := store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
			UserID: 1, ChannelID: created.Channel.ID, RandomID: int64(500 + i), Message: "page", Date: 1_700_000_101 + i,
		})
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		channelPts = res.Channel.Pts
	}
	diff := func(pts, limit int) domain.ChannelDifference {
		t.Helper()
		out, err := store.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{
			UserID: 1, ChannelID: created.Channel.ID, Pts: pts, Limit: limit,
		})
		if err != nil {
			t.Fatalf("difference pts=%d limit=%d: %v", pts, limit, err)
		}
		return out
	}

	if got := diff(startPts, domain.MaxChannelDifferenceLimit); !got.TooLong {
		t.Fatalf("limit=%d over a %d-pts gap = %+v, want tooLong (unchanged for TDesktop/Android)", domain.MaxChannelDifferenceLimit, sent, got)
	}
	if got := diff(startPts, sent-1); !got.TooLong {
		t.Fatalf("limit=%d over a %d-pts gap = final=%v events=%d, want tooLong", sent-1, sent, got.Final, len(got.Events))
	}
	first := diff(startPts, 100000)
	if first.TooLong || first.Final || len(first.Events) != domain.MaxChannelDifferenceLimit || first.Pts != startPts+domain.MaxChannelDifferenceLimit {
		t.Fatalf("limit=100000 first page = tooLong=%v final=%v events=%d pts=%d, want final=false page of %d to pts %d",
			first.TooLong, first.Final, len(first.Events), first.Pts, domain.MaxChannelDifferenceLimit, startPts+domain.MaxChannelDifferenceLimit)
	}
	second := diff(first.Pts, 100000)
	if second.TooLong || !second.Final || len(second.Events) != sent-domain.MaxChannelDifferenceLimit || second.Pts != channelPts {
		t.Fatalf("limit=100000 second page = tooLong=%v final=%v events=%d pts=%d, want final page of %d to pts %d",
			second.TooLong, second.Final, len(second.Events), second.Pts, sent-domain.MaxChannelDifferenceLimit, channelPts)
	}
}
