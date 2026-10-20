package storetest

import (
	"context"
	"errors"
	"slices"
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
	t.Run("todo replacement keeps current completions", func(t *testing.T) {
		f := newFixture(t)
		ctx := context.Background()
		base := &domain.MessageMedia{Kind: domain.MessageMediaKindTodo, Todo: &domain.MessageTodo{
			Title: "before", OthersCanComplete: true,
			Items: []domain.MessageTodoItem{{ID: 1, Title: "keep"}, {ID: 2, Title: "remove"}},
		}}
		sent, err := f.Messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: f.Sender, RecipientUserID: f.Recipient, RandomID: 91002, Date: 1700000000, Media: base})
		if err != nil {
			t.Fatal(err)
		}
		// Prepare the replacement before a participant completes tasks. Its snapshot
		// cannot know the completion that must survive at the actual write boundary.
		replacement := &domain.MessageMedia{Kind: domain.MessageMediaKindTodo, Todo: &domain.MessageTodo{
			Title: "after", OthersCanComplete: true,
			Items:       []domain.MessageTodoItem{{ID: 1, Title: "renamed"}, {ID: 3, Title: "new"}},
			Completions: []domain.MessageTodoCompletion{{ID: 3, CompletedBy: f.Sender, Date: 1700000001}},
		}}
		req := domain.EditMessageRequest{OwnerUserID: f.Recipient, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.Sender}, ID: sent.RecipientMessage.ID, EditDate: 1700000003, Media: replacement, ReplaceTodo: true, AllowTodoParticipantMutation: true}
		if _, err = f.Messages.EditMessage(ctx, req); !errors.Is(err, domain.ErrMessageAuthorRequired) {
			t.Fatalf("non-author replacement = %v", err)
		}
		done := []domain.MessageTodoCompletion{
			{ID: 1, CompletedBy: f.Recipient, Date: 1700000002},
			{ID: 2, CompletedBy: f.Recipient, Date: 1700000002},
		}
		completed := *base.Todo
		completed.Completions = done
		participant := domain.EditMessageRequest{
			OwnerUserID: f.Recipient, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.Sender},
			ID: sent.RecipientMessage.ID, EditDate: 1700000002, AllowTodoParticipantMutation: true,
			Media: &domain.MessageMedia{Kind: domain.MessageMediaKindTodo, Todo: &completed},
		}
		if _, err := f.Messages.EditMessage(ctx, participant); err != nil {
			t.Fatal(err)
		}
		req.OwnerUserID, req.Peer.ID, req.ID = f.Sender, f.Recipient, sent.SenderMessage.ID
		req.AllowTodoParticipantMutation = false
		edited, err := f.Messages.EditMessage(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		check := func(msg domain.Message) {
			t.Helper()
			todo := msg.Media.Todo
			if todo.Title != "after" || len(todo.Items) != 2 || todo.Items[0].ID != 1 || todo.Items[1].ID != 3 || !slices.Equal(todo.Completions, done[:1]) {
				t.Fatalf("replacement lost current completion or retained removed/forged completion: %+v", todo)
			}
		}
		for _, item := range edited.Edited {
			check(item.Message)
			check(item.Event.Message)
		}
		for _, box := range []struct {
			owner int64
			id    int
		}{{f.Sender, sent.SenderMessage.ID}, {f.Recipient, sent.RecipientMessage.ID}} {
			list, err := f.Messages.GetByIDs(ctx, box.owner, []int{box.id})
			if err != nil || len(list.Messages) != 1 {
				t.Fatalf("box = %+v, %v", list, err)
			}
			check(list.Messages[0])
		}
		// Toggle is not a definition replacement: explicitly clearing completions
		// must remain possible after replacement.
		toggled := *edited.Self().Message.Media.Todo
		toggled.Completions = nil
		participant.Media = &domain.MessageMedia{Kind: domain.MessageMediaKindTodo, Todo: &toggled}
		participant.EditDate++
		result, err := f.Messages.EditMessage(ctx, participant)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range result.Edited {
			if len(item.Message.Media.Todo.Completions) != 0 {
				t.Fatalf("toggle treated as replacement: %+v", item.Message.Media.Todo)
			}
		}
	})
}
