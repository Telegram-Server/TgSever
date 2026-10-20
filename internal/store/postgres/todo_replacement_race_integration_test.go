package postgres

import (
	"context"
	"slices"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func TestTodoReplacementQueuedBehindCompletionPostgres(t *testing.T) {
	for _, channel := range []bool{false, true} {
		t.Run(map[bool]string{false: "private", true: "channel"}[channel], func(t *testing.T) {
			pool := testPool(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			ids := memberRulesUsers(t, pool, 2)
			base := &domain.MessageMedia{Kind: domain.MessageMediaKindTodo, Todo: &domain.MessageTodo{
				Title: "before", OthersCanComplete: true,
				Items: []domain.MessageTodoItem{{ID: 1, Title: "keep"}, {ID: 2, Title: "remove"}},
			}}
			// The author's request predates the completion and carries no completion state.
			replacement := &domain.MessageMedia{Kind: domain.MessageMediaKindTodo, Todo: &domain.MessageTodo{
				Title: "after", OthersCanComplete: true,
				Items: []domain.MessageTodoItem{{ID: 1, Title: "renamed"}, {ID: 3, Title: "new"}},
			}}
			done := []domain.MessageTodoCompletion{{ID: 1, CompletedBy: ids[1], Date: 1700000010}, {ID: 2, CompletedBy: ids[1], Date: 1700000010}}
			completedTodo := *base.Todo
			completedTodo.Completions = done
			completed := &domain.MessageMedia{Kind: domain.MessageMediaKindTodo, Todo: &completedTodo}
			blocker, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			editor, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer editor.Release()
			var edit func() (*domain.MessageMedia, error)
			var complete func() error
			var read func() []*domain.MessageMedia
			if channel {
				channels := NewChannelStore(pool)
				created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: ids[0], MemberUserIDs: ids[1:], Title: "Todo replacement race", Megagroup: true, Date: 1700000000})
				if err != nil {
					t.Fatal(err)
				}
				channelID := created.Channel.ID
				sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: ids[0], ChannelID: channelID, RandomID: 92001, Date: 1700000000, Media: base})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := sqlcgen.New(blocker).LockChannelForMessageEdit(ctx, channelID); err != nil {
					t.Fatal(err)
				}
				edit = func() (*domain.MessageMedia, error) {
					res, err := NewChannelStore(editor).EditChannelMessage(ctx, domain.EditChannelMessageRequest{UserID: ids[0], ChannelID: channelID, ID: sent.Message.ID, EditDate: 1700000020, Media: replacement, ReplaceTodo: true})
					return res.Message.Media, err
				}
				complete = func() error {
					_, err := NewChannelStore(blocker).EditChannelMessage(ctx, domain.EditChannelMessageRequest{
						UserID: ids[1], ChannelID: channelID, ID: sent.Message.ID, EditDate: 1700000010, Media: completed, AllowTodoParticipantMutation: true,
						TodoServiceAction: &domain.ChannelMessageAction{Type: domain.ChannelActionTodoCompletions, Completed: []int{1, 2}},
					})
					return err
				}
				read = func() []*domain.MessageMedia {
					history, err := channels.GetChannelMessages(ctx, ids[1], channelID, []int{sent.Message.ID})
					if err != nil || len(history.Messages) != 1 {
						t.Fatalf("channel history = %+v, %v", history, err)
					}
					return []*domain.MessageMedia{history.Messages[0].Media}
				}
			} else {
				messages := NewMessageStore(pool)
				sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: ids[0], RecipientUserID: ids[1], RandomID: 92002, Date: 1700000000, Media: base})
				if err != nil {
					t.Fatal(err)
				}
				if err := lockUsersForUpdate(ctx, blocker, ids...); err != nil {
					t.Fatal(err)
				}
				edit = func() (*domain.MessageMedia, error) {
					res, err := NewMessageStore(editor).EditMessage(ctx, domain.EditMessageRequest{OwnerUserID: ids[0], Peer: domain.Peer{Type: domain.PeerTypeUser, ID: ids[1]}, ID: sent.SenderMessage.ID, EditDate: 1700000020, Media: replacement, ReplaceTodo: true})
					return res.Self().Message.Media, err
				}
				complete = func() error {
					_, err := NewMessageStore(blocker).EditMessage(ctx, domain.EditMessageRequest{OwnerUserID: ids[1], Peer: domain.Peer{Type: domain.PeerTypeUser, ID: ids[0]}, ID: sent.RecipientMessage.ID, EditDate: 1700000010, Media: completed, AllowTodoParticipantMutation: true})
					return err
				}
				read = func() []*domain.MessageMedia {
					var media []*domain.MessageMedia
					for _, box := range []struct {
						owner int64
						id    int
					}{{ids[0], sent.SenderMessage.ID}, {ids[1], sent.RecipientMessage.ID}} {
						history, err := messages.GetByIDs(ctx, box.owner, []int{box.id})
						if err != nil || len(history.Messages) != 1 {
							t.Fatalf("private history = %+v, %v", history, err)
						}
						media = append(media, history.Messages[0].Media)
					}
					return media
				}
			}
			type result struct {
				media *domain.MessageMedia
				err   error
			}
			finished := make(chan result, 1)
			go func() { media, err := edit(); finished <- result{media, err} }()
			waitForPostgresBackendLockWait(t, ctx, pool, int(editor.Conn().PgConn().PID()))
			// Execute the actual participant edit in the owning transaction, then
			// release the queued author's edit. A pre-lock snapshot loses this state.
			if err := complete(); err != nil {
				t.Fatal(err)
			}
			if err := blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			var edited *domain.MessageMedia
			select {
			case res := <-finished:
				if res.err != nil {
					t.Fatal(res.err)
				}
				edited = res.media
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			for _, media := range append(read(), edited) {
				if media == nil || media.Todo == nil || media.Todo.Title != "after" || !slices.Equal(media.Todo.Completions, done[:1]) {
					t.Fatalf("queued replacement overwrote participant completion: %+v", media)
				}
			}
		})
	}
}
