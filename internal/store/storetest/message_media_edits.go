package storetest

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

type MessageMediaEditFixture struct {
	Messages          store.MessageStore
	Sender, Recipient int64
}

func MessageMediaEdits(t *testing.T, newFixture func(*testing.T) MessageMediaEditFixture) {
	t.Helper()
	for _, stopped := range []bool{false, true} {
		t.Run(map[bool]string{false: "expired live location", true: "same second stopped live location"}[stopped], func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			const date = 1700000000
			sent, err := f.Messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: f.Sender, RecipientUserID: f.Recipient, RandomID: 91001, Date: date, Media: &domain.MessageMedia{Kind: domain.MessageMediaKindGeoLive, GeoLive: &domain.MessageGeoLive{Geo: domain.MessageGeoPoint{Lat: 1, Long: 2}, Period: 900}}})
			if err != nil {
				t.Fatal(err)
			}
			req := domain.EditMessageRequest{OwnerUserID: f.Sender, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.Recipient}, ID: sent.SenderMessage.ID, EditDate: date + 900, Media: &domain.MessageMedia{Kind: domain.MessageMediaKindGeoLive, GeoLive: &domain.MessageGeoLive{Geo: domain.MessageGeoPoint{Lat: 3, Long: 4}, Period: 0x7fffffff}}}
			if stopped {
				req.EditDate = date
				req.Media = &domain.MessageMedia{Kind: domain.MessageMediaKindGeoLive, GeoLive: &domain.MessageGeoLive{Geo: domain.MessageGeoPoint{Lat: 1, Long: 2}, Period: 1, Stopped: true}}
				if _, err = f.Messages.EditMessage(ctx, req); err != nil {
					t.Fatal(err)
				}
				req.Media = &domain.MessageMedia{Kind: domain.MessageMediaKindGeoLive, GeoLive: &domain.MessageGeoLive{Geo: domain.MessageGeoPoint{Lat: 3, Long: 4}, Period: 0x7fffffff}}
			}
			if _, err = f.Messages.EditMessage(ctx, req); !errors.Is(err, domain.ErrMessageNotModified) {
				t.Fatalf("revival = %v", err)
			}
			for _, box := range []struct {
				owner int64
				id    int
			}{{f.Sender, sent.SenderMessage.ID}, {f.Recipient, sent.RecipientMessage.ID}} {
				list, err := f.Messages.GetByIDs(ctx, box.owner, []int{box.id})
				if err != nil || len(list.Messages) != 1 {
					t.Fatalf("box = %+v, %v", list, err)
				}
				live := list.Messages[0].Media.GeoLive
				if live.Geo.Lat != 1 || live.Stopped != stopped {
					t.Fatalf("snapshot = %+v", live)
				}
			}
		})
	}
	t.Run("todo replacement owner and recipient", func(t *testing.T) {
		f := newFixture(t)
		ctx := context.Background()
		media := func(title string) *domain.MessageMedia {
			return &domain.MessageMedia{Kind: domain.MessageMediaKindTodo, Todo: &domain.MessageTodo{Title: title, Items: []domain.MessageTodoItem{{ID: 1, Title: "task"}}}}
		}
		sent, err := f.Messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: f.Sender, RecipientUserID: f.Recipient, RandomID: 91002, Date: 1700000000, Media: media("before")})
		if err != nil {
			t.Fatal(err)
		}
		req := domain.EditMessageRequest{OwnerUserID: f.Recipient, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.Sender}, ID: sent.RecipientMessage.ID, EditDate: 1700000001, Media: media("forged")}
		if _, err = f.Messages.EditMessage(ctx, req); !errors.Is(err, domain.ErrMessageAuthorRequired) {
			t.Fatalf("non-author replacement = %v", err)
		}
		req.OwnerUserID, req.Peer.ID, req.ID = f.Sender, f.Recipient, sent.SenderMessage.ID
		req.Media = media("after")
		if _, err = f.Messages.EditMessage(ctx, req); err != nil {
			t.Fatal(err)
		}
		list, err := f.Messages.GetByIDs(ctx, f.Recipient, []int{sent.RecipientMessage.ID})
		if err != nil || len(list.Messages) != 1 || list.Messages[0].Media.Todo.Title != "after" {
			t.Fatalf("recipient replacement = %+v, %v", list, err)
		}
	})
}
