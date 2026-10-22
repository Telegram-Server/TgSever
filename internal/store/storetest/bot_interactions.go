package storetest

import (
	"context"
	"sync"
	"sync/atomic"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"testing"
	"time"
)

type BotInteractionsFixture struct {
	Queries                                store.BotGuestQueryStore
	Feedback                               store.BotInlineFeedbackStore
	Buttons                                store.BotStore
	BotID, OtherBotID, UserID, OtherUserID int64
}

func BotInteractions(t *testing.T, newFixture func(*testing.T) BotInteractionsFixture) {
	t.Helper()
	ctx := context.Background()
	t.Run("guest grants bind bot expire exactly and consume once", func(t *testing.T) {
		f := newFixture(t)
		now := time.Now().UTC()
		q := domain.BotGuestQuery{ID: 7001, BotUserID: f.BotID, CallerUserID: f.UserID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.OtherUserID}, MessageID: 12, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
		if err := f.Queries.CreateGuestQuery(ctx, q); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := f.Queries.ConsumeGuestQuery(ctx, f.OtherBotID, q.ID, now); err != nil || ok {
			t.Fatalf("foreign bot claim=%v %v", ok, err)
		}
		got, ok, err := f.Queries.GetGuestQuery(ctx, f.BotID, q.ID, now)
		if err != nil || !ok || got.CallerUserID != q.CallerUserID || got.Peer != q.Peer || got.MessageID != 12 {
			t.Fatalf("grant=%+v %v %v", got, ok, err)
		}
		var wins atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, ok, err := f.Queries.ConsumeGuestQuery(ctx, f.BotID, q.ID, now)
				if err != nil {
					t.Error(err)
				}
				if ok {
					wins.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		if wins.Load() != 1 {
			t.Fatalf("single-use winners=%d", wins.Load())
		}
		q.ID++
		if err := f.Queries.CreateGuestQuery(ctx, q); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := f.Queries.GetGuestQuery(ctx, f.BotID, q.ID, q.ExpiresAt); err != nil || ok {
			t.Fatalf("boundary lookup=%v %v", ok, err)
		}
		if _, ok, err := f.Queries.ConsumeGuestQuery(ctx, f.BotID, q.ID, q.ExpiresAt); err != nil || ok {
			t.Fatalf("boundary consume=%v %v", ok, err)
		}
	})
	t.Run("prepared buttons isolate users bots filters decorations and consumption", func(t *testing.T) {
		f := newFixture(t)
		now := time.Now().UTC()
		filter := &domain.BotRequestPeerFilter{UserIsBotSet: true, UserIsBot: false}
		button := domain.BotRequestedWebViewButton{WebAppReqID: "prepared-contract", BotUserID: f.BotID, UserID: f.UserID, ButtonID: 0, Text: "Share", PeerType: "user", MaxQuantity: 1, PeerFilter: filter, NameRequested: true, Style: domain.MarkupButtonStylePrimary, IconCustomEmojiID: 51, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
		if err := f.Buttons.SaveRequestedWebViewButton(ctx, button); err != nil {
			t.Fatal(err)
		}
		filter.UserIsBot = true
		for _, binding := range [][2]int64{{f.BotID, f.OtherUserID}, {f.OtherBotID, f.UserID}} {
			if _, ok, err := f.Buttons.ConsumeRequestedWebViewButton(ctx, binding[0], binding[1], button.WebAppReqID); err != nil || ok {
				t.Fatalf("foreign consume=%v %v", ok, err)
			}
		}
		got, ok, err := f.Buttons.GetRequestedWebViewButton(ctx, f.BotID, f.UserID, button.WebAppReqID)
		if err != nil || !ok || got.PeerFilter.UserIsBot || got.Style != button.Style || got.IconCustomEmojiID != 51 || !got.NameRequested {
			t.Fatalf("stored button=%+v %v %v", got, ok, err)
		}
		got.PeerFilter.UserIsBot = true
		var wins atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				v, ok, err := f.Buttons.ConsumeRequestedWebViewButton(ctx, f.BotID, f.UserID, button.WebAppReqID)
				if err != nil {
					t.Error(err)
				}
				if ok {
					if v.PeerFilter.UserIsBot {
						t.Error("read snapshot mutated stored filter")
					}
					wins.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		if wins.Load() != 1 {
			t.Fatalf("prepared winners=%d", wins.Load())
		}
		button.WebAppReqID = "prepared-expired"
		button.ExpiresAt = now.Add(-time.Second)
		if err := f.Buttons.SaveRequestedWebViewButton(ctx, button); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := f.Buttons.ConsumeRequestedWebViewButton(ctx, f.BotID, f.UserID, button.WebAppReqID); err != nil || ok {
			t.Fatalf("expired prepared=%v %v", ok, err)
		}
		button.WebAppReqID = "prepared-managed"
		button.PeerType = "managed_bot"
		button.ExpiresAt = now.Add(time.Hour)
		button.PeerFilter = nil
		button.SuggestedName = "Managed name"
		button.SuggestedUsername = "suggested_managed_bot"
		if err := f.Buttons.SaveRequestedWebViewButton(ctx, button); err != nil {
			t.Fatal(err)
		}
		got, ok, err = f.Buttons.GetRequestedWebViewButton(ctx, f.BotID, f.UserID, button.WebAppReqID)
		if err != nil || !ok || got.SuggestedName != button.SuggestedName || got.SuggestedUsername != button.SuggestedUsername || got.PeerType != "managed_bot" {
			t.Fatalf("managed snapshot=%+v %v %v", got, ok, err)
		}
	})
	t.Run("feedback setting changes persist and reject invalid percentages", func(t *testing.T) {
		f := newFixture(t)
		if err := f.Feedback.SetInlineFeedback(ctx, f.BotID, 100); err != nil {
			t.Fatal(err)
		}
		if err := f.Feedback.SetInlineFeedback(ctx, f.BotID, 101); err == nil {
			t.Fatal("accepted invalid feedback rate")
		}
		v, err := f.Feedback.GetInlineFeedback(ctx, f.BotID)
		if err != nil || v != 100 {
			t.Fatalf("setting=%d %v", v, err)
		}
		if err := f.Feedback.SetInlineFeedback(ctx, f.BotID, 0); err != nil {
			t.Fatal(err)
		}
		v, err = f.Feedback.GetInlineFeedback(ctx, f.BotID)
		if err != nil || v != 0 {
			t.Fatalf("disabled setting=%d %v", v, err)
		}
	})
}
