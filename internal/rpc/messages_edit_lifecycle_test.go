package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/botapi"
	"telesrv/internal/domain"
)

func TestLiveLocationLifecycleRPC(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(map[bool]string{false: "expired", true: "stopped"}[stopped], func(t *testing.T) {
			r, owner, friend := newMediaTestRouter(t)
			r.clock = fixedClock{now: time.Unix(100000, 0)}
			peer := inputPeerUser(friend)
			msg := sendTestLiveLocation(t, r, owner.ID, peer, 60001, 900)
			ctx := WithUserID(context.Background(), owner.ID)
			if stopped {
				req := &tg.MessagesEditMessageRequest{Peer: peer, ID: msg.ID}
				req.SetMedia(&tg.InputMediaGeoLive{Stopped: true, GeoPoint: &tg.InputGeoPointEmpty{}})
				if _, err := r.onMessagesEditMessage(ctx, req); err != nil {
					t.Fatal(err)
				}
			} else {
				r.clock = fixedClock{now: time.Unix(100900, 0)}
			}
			for _, period := range []int{0, foreverLiveLocationPeriod} {
				req := &tg.MessagesEditMessageRequest{Peer: peer, ID: msg.ID}
				media := &tg.InputMediaGeoLive{GeoPoint: &tg.InputGeoPoint{Lat: 1, Long: 2}}
				if period != 0 {
					media.SetPeriod(period)
				}
				req.SetMedia(media)
				if _, err := r.onMessagesEditMessage(ctx, req); !tgerr.Is(err, "MESSAGE_NOT_MODIFIED") {
					t.Fatalf("period %d: got %v, want MESSAGE_NOT_MODIFIED", period, err)
				}
			}
		})
	}
}

func TestLiveLocationPeriodBoundsRPC(t *testing.T) {
	for _, tc := range []struct {
		name          string
		current, next int
		valid         bool
	}{
		{"extend one day", 900, 87300, true},
		{"too far", 900, 87301, false},
		{"ninety days", 89 * 86400, 90 * 86400, true},
		{"over ninety days", 90 * 86400, 90*86400 + 1, false},
		{"forever", 900, foreverLiveLocationPeriod, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBotAPIReceiveFixture(t, false)
			f.router.clock = fixedClock{now: time.Unix(100000, 0)}
			sent, err := f.messages.SendPrivateText(f.ctx, f.owner.ID, domain.SendPrivateTextRequest{SenderUserID: f.owner.ID, RecipientUserID: f.bot.ID, RandomID: 60002, Date: 100000, Media: &domain.MessageMedia{Kind: domain.MessageMediaKindGeoLive, GeoLive: &domain.MessageGeoLive{Geo: domain.MessageGeoPoint{Lat: 1, Long: 2}, Period: tc.current}}})
			if err != nil {
				t.Fatal(err)
			}
			req := &tg.MessagesEditMessageRequest{Peer: inputPeerUser(f.bot), ID: sent.SenderMessage.ID}
			media := &tg.InputMediaGeoLive{GeoPoint: &tg.InputGeoPoint{Lat: 3, Long: 4}}
			media.SetPeriod(tc.next)
			req.SetMedia(media)
			_, err = f.router.onMessagesEditMessage(WithUserID(f.ctx, f.owner.ID), req)
			if tc.valid && err != nil {
				t.Fatal(err)
			}
			if !tc.valid && !tgerr.Is(err, "MEDIA_INVALID") {
				t.Fatalf("got %v, want MEDIA_INVALID", err)
			}
		})
	}
}

func TestTodoReplacementRPC(t *testing.T) {
	r, owner, friend := newMediaTestRouter(t)
	peer := inputPeerUser(friend)
	msg := sendTestTodo(t, r, owner.ID, peer, 70001)
	replacement := &tg.InputMediaTodo{Todo: tg.TodoList{Title: tg.TextWithEntities{Text: "new list"}, List: []tg.TodoItem{{ID: 9, Title: tg.TextWithEntities{Text: "new task"}}}}}
	req := &tg.MessagesEditMessageRequest{Peer: peer, ID: msg.ID}
	req.SetMedia(replacement)
	updates, err := r.onMessagesEditMessage(WithUserID(context.Background(), owner.ID), req)
	if err != nil {
		t.Fatal(err)
	}
	edited := editedMessageFromUpdates(t, updates)
	if todo, ok := edited.Media.(*tg.MessageMediaToDo); !ok || todo.Todo.Title.Text != "new list" || todo.Todo.List[0].ID != 9 {
		t.Fatalf("replacement = %#v", edited.Media)
	}
	history, err := r.deps.Messages.GetHistory(context.Background(), friend.ID, domain.MessageFilter{HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}, Limit: 1})
	if err != nil || len(history.Messages) != 1 {
		t.Fatalf("history = %+v, %v", history, err)
	}
	if todo := history.Messages[0].Media.Todo; todo.Title != "new list" || todo.Items[0].ID != 9 {
		t.Fatalf("recipient todo = %+v", todo)
	}
	req = &tg.MessagesEditMessageRequest{Peer: inputPeerUser(owner), ID: history.Messages[0].ID}
	req.SetMedia(replacement)
	if _, err := r.onMessagesEditMessage(WithUserID(context.Background(), friend.ID), req); !tgerr.Is(err, "MESSAGE_AUTHOR_REQUIRED") {
		t.Fatalf("non-author replacement = %v", err)
	}
}

func TestPrivateMediaEditsReachBotAPIQueueRPC(t *testing.T) {
	for _, operation := range []string{"location", "complete", "append", "replace"} {
		t.Run(operation, func(t *testing.T) {
			f := newBotAPIReceiveFixture(t, false)
			ctx := WithUserID(f.ctx, f.owner.ID)
			peer := inputPeerUser(f.bot)
			var msg *tg.Message
			if operation == "location" {
				msg = sendTestLiveLocation(t, f.router, f.owner.ID, peer, 80001, 900)
			} else {
				msg = sendTestTodo(t, f.router, f.owner.ID, peer, 80002)
			}
			if err := f.router.BotAPIDropPendingUpdates(f.ctx, f.bot.ID); err != nil {
				t.Fatal(err)
			}
			var err error
			switch operation {
			case "complete":
				_, err = f.router.onMessagesToggleTodoCompleted(ctx, &tg.MessagesToggleTodoCompletedRequest{Peer: peer, MsgID: msg.ID, Completed: []int{1}})
			case "append":
				_, err = f.router.onMessagesAppendTodoList(ctx, &tg.MessagesAppendTodoListRequest{Peer: peer, MsgID: msg.ID, List: []tg.TodoItem{{ID: 3, Title: tg.TextWithEntities{Text: "eggs"}}}})
			default:
				req := &tg.MessagesEditMessageRequest{Peer: peer, ID: msg.ID}
				if operation == "location" {
					req.SetMedia(&tg.InputMediaGeoLive{GeoPoint: &tg.InputGeoPoint{Lat: 42, Long: 43}})
				} else {
					req.SetMedia(&tg.InputMediaTodo{Todo: tg.TodoList{Title: tg.TextWithEntities{Text: "replaced"}, List: []tg.TodoItem{{ID: 4, Title: tg.TextWithEntities{Text: "tea"}}}}})
				}
				_, err = f.router.onMessagesEditMessage(ctx, req)
			}
			if err != nil {
				t.Fatal(err)
			}
			message := editedMediaHTTPMessage(t, f)
			switch operation {
			case "location":
				location, ok := message["location"].(map[string]any)
				if !ok || location["latitude"] != float64(42) || location["longitude"] != float64(43) {
					t.Fatalf("edited location = %+v", message)
				}
			default:
				checklist, ok := message["checklist"].(map[string]any)
				if !ok {
					t.Fatalf("edited checklist = %+v", message)
				}
				tasks := checklist["tasks"].([]any)
				switch operation {
				case "complete":
					if len(tasks) != 2 {
						t.Fatalf("completed checklist tasks = %+v", tasks)
					}
					task := tasks[0].(map[string]any)
					who, completed := task["completed_by_user"].(map[string]any)
					date, _ := task["completion_date"].(float64)
					if task["id"] != float64(1) || !completed || who["id"] != float64(f.owner.ID) || date <= 0 {
						t.Fatalf("completed task = %+v", task)
					}
				case "append":
					if len(tasks) != 3 || tasks[2].(map[string]any)["id"] != float64(3) || tasks[2].(map[string]any)["text"] != "eggs" {
						t.Fatalf("appended tasks = %+v", tasks)
					}
				case "replace":
					if checklist["title"] != "replaced" || len(tasks) != 1 || tasks[0].(map[string]any)["id"] != float64(4) || tasks[0].(map[string]any)["text"] != "tea" {
						t.Fatalf("replacement = %+v", checklist)
					}
				}
			}
		})
	}
}

func TestLiveLocationMarkupAndBlockRPC(t *testing.T) {
	f := newBotAPIReceiveFixture(t, false)
	ctx := WithUserID(f.ctx, f.bot.ID)
	peer := inputPeerUser(f.owner)
	msg := sendTestLiveLocation(t, f.router, f.bot.ID, peer, 90001, 900)
	req := &tg.MessagesEditMessageRequest{Peer: peer, ID: msg.ID}
	media := &tg.InputMediaGeoLive{GeoPoint: &tg.InputGeoPoint{Lat: 5, Long: 6}}
	media.SetHeading(270)
	media.SetProximityNotificationRadius(100)
	req.SetMedia(media)
	req.SetReplyMarkup(&tg.ReplyInlineMarkup{Rows: []tg.KeyboardInlineButtonRow{{Buttons: []tg.KeyboardInlineButton{{Text: "stop", Type: &tg.InlineButtonTypeCallback{Data: []byte("stop")}}}}}})
	updates, err := f.router.onMessagesEditMessage(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	edited := editedMessageFromUpdates(t, updates)
	markup, ok := edited.ReplyMarkup.(*tg.ReplyInlineMarkup)
	if !ok || markup.Rows[0].Buttons[0].Text != "stop" {
		t.Fatalf("markup = %#v", edited.ReplyMarkup)
	}
	live := edited.Media.(*tg.MessageMediaGeoLive)
	if live.Heading != 270 || live.ProximityNotificationRadius != 100 {
		t.Fatalf("live = %+v", live)
	}
	before, err := f.messages.GetHistory(f.ctx, f.owner.ID, domain.MessageFilter{HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.bot.ID}, Limit: 1})
	if err != nil || len(before.Messages) != 1 {
		t.Fatalf("recipient before blocking = %+v, %v", before, err)
	}
	sessions := &captureSessions{}
	f.router.deps.Sessions = sessions
	f.router.deps.Contacts = recipientBlocksContacts{blocker: f.owner.ID, blocked: f.bot.ID}
	media.GeoPoint = &tg.InputGeoPoint{Lat: 7, Long: 8}
	if _, err := f.router.onMessagesEditMessage(ctx, req); !tgerr.IsCode(err, 403) {
		t.Fatalf("blocked edit = %v, want permission denial", err)
	}
	history, err := f.messages.GetHistory(f.ctx, f.owner.ID, domain.MessageFilter{HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.bot.ID}, Limit: 1})
	if err != nil || len(history.Messages) != 1 {
		t.Fatalf("history = %+v, %v", history, err)
	}
	if history.Messages[0].Media.GeoLive.Geo.Lat != 5 || history.Messages[0].Pts != before.Messages[0].Pts {
		t.Fatalf("blocked recipient received a durable edit: %+v", history.Messages[0])
	}
	for _, userID := range sessions.pushedUserIDs() {
		if userID == f.owner.ID {
			t.Fatal("blocked recipient received a pushed update")
		}
	}
}

func TestInlineLiveLocationLifecycleRPC(t *testing.T) {
	f := newInlineBotRPCTestFixture(t)
	f.router.clock = fixedClock{now: time.Unix(200000, 0)}
	sessions := &captureSessions{}
	f.router.deps.Sessions = sessions
	ctx := context.Background()
	botCtx := WithUserID(ctx, f.bot.ID)
	queryID, _ := f.router.inlines.registerWithCacheKey(f.router.clock.Now(), f.bot.ID, f.owner.ID, domain.Peer{Type: domain.PeerTypeUser, ID: f.peer.ID}, inlineCacheKey{query: "live"})
	result := inlineGeoResultWithPeriod("live", 1, 2).(*tg.InputBotInlineResult)
	// Telegram only exposes an editable inline_message_id for messages sent
	// with an inline keyboard; request that capability on the actual result.
	result.SendMessage.(*tg.InputBotInlineMessageMediaGeo).SetReplyMarkup(&tg.ReplyInlineMarkup{
		Rows: []tg.KeyboardInlineButtonRow{{Buttons: []tg.KeyboardInlineButton{{
			Text: "stop", Type: &tg.InlineButtonTypeCallback{Data: []byte("stop")},
		}}}},
	})
	if ok, err := f.router.onMessagesSetInlineBotResults(botCtx, &tg.MessagesSetInlineBotResultsRequest{QueryID: queryID, Results: []tg.InputBotInlineResultClass{result}}); !ok || err != nil {
		t.Fatalf("set results = %v, %v", ok, err)
	}
	if _, err := f.router.onMessagesSendInlineBotResult(WithUserID(ctx, f.owner.ID), &tg.MessagesSendInlineBotResultRequest{Peer: inputPeerUser(f.peer), RandomID: 991099, QueryID: queryID, ID: "live"}); err != nil {
		t.Fatal(err)
	}
	id, ok := inlineSendFeedbackFromSessions(t, sessions, f.bot.ID).GetMsgID()
	if !ok {
		t.Fatal("missing inline id")
	}
	req := &tg.MessagesEditInlineBotMessageRequest{ID: id}
	media := &tg.InputMediaGeoLive{GeoPoint: &tg.InputGeoPoint{Lat: 3, Long: 4}}
	media.SetPeriod(86460)
	req.SetMedia(media)
	req.SetReplyMarkup(&tg.ReplyInlineMarkup{Rows: []tg.KeyboardInlineButtonRow{{Buttons: []tg.KeyboardInlineButton{{Text: "updated", Type: &tg.InlineButtonTypeCallback{Data: []byte("updated")}}}}}})
	if ok, err := f.router.onMessagesEditInlineBotMessage(botCtx, req); !ok || err != nil {
		t.Fatalf("inline edit = %v, %v", ok, err)
	}
	for _, viewer := range []struct{ user, peer int64 }{{f.owner.ID, f.peer.ID}, {f.peer.ID, f.owner.ID}} {
		history, err := f.router.deps.Messages.GetHistory(ctx, viewer.user, domain.MessageFilter{HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: viewer.peer}, Limit: 1})
		if err != nil || len(history.Messages) != 1 {
			t.Fatalf("history = %+v, %v", history, err)
		}
		msg := history.Messages[0]
		if msg.Media.GeoLive.Geo.Lat != 3 || msg.Media.GeoLive.Period != 86460 || msg.ReplyMarkup.Inline[0][0].Text != "updated" {
			t.Fatalf("inline snapshot = %+v", msg)
		}
	}
	req.SetMedia(&tg.InputMediaGeoLive{Stopped: true, GeoPoint: &tg.InputGeoPointEmpty{}})
	if ok, err := f.router.onMessagesEditInlineBotMessage(botCtx, req); !ok || err != nil {
		t.Fatalf("inline stop = %v, %v", ok, err)
	}
	for _, viewer := range []struct{ user, peer int64 }{{f.owner.ID, f.peer.ID}, {f.peer.ID, f.owner.ID}} {
		history, err := f.router.deps.Messages.GetHistory(ctx, viewer.user, domain.MessageFilter{HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: viewer.peer}, Limit: 1})
		if err != nil || len(history.Messages) != 1 {
			t.Fatalf("stopped history = %+v, %v", history, err)
		}
		msg := history.Messages[0]
		if !msg.Media.GeoLive.Stopped || msg.Date+msg.Media.GeoLive.Period > int(f.router.clock.Now().Unix()) {
			t.Fatalf("inline location remained live for viewer %d: %+v", viewer.user, msg)
		}
	}
	req.SetMedia(media)
	if _, err := f.router.onMessagesEditInlineBotMessage(botCtx, req); !tgerr.Is(err, "MESSAGE_NOT_MODIFIED") {
		t.Fatalf("inline revival = %v", err)
	}
}

func TestTodoParticipantEditsNotifyAuthorBotRPC(t *testing.T) {
	for _, operation := range []string{"complete", "append"} {
		t.Run(operation, func(t *testing.T) {
			f := newBotAPIReceiveFixture(t, false)
			sent := sendTestTodoWithFlags(t, f.router, f.bot.ID, inputPeerUser(f.owner), 99001, true, true)
			history, err := f.messages.GetHistory(f.ctx, f.owner.ID, domain.MessageFilter{HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.bot.ID}, Limit: 1})
			if err != nil || len(history.Messages) != 1 {
				t.Fatalf("history = %+v, %v", history, err)
			}
			id := history.Messages[0].ID
			ctx := WithUserID(f.ctx, f.owner.ID)
			if operation == "complete" {
				_, err = f.router.onMessagesToggleTodoCompleted(ctx, &tg.MessagesToggleTodoCompletedRequest{Peer: inputPeerUser(f.bot), MsgID: id, Completed: []int{1}})
			} else {
				_, err = f.router.onMessagesAppendTodoList(ctx, &tg.MessagesAppendTodoListRequest{Peer: inputPeerUser(f.bot), MsgID: id, List: []tg.TodoItem{{ID: 3, Title: tg.TextWithEntities{Text: "eggs"}}}})
			}
			if err != nil {
				t.Fatal(err)
			}
			message := editedMediaHTTPMessage(t, f)
			if message["message_id"] != float64(sent.ID) {
				t.Fatalf("author bot edit = %+v", message)
			}
			checklist := message["checklist"].(map[string]any)
			tasks := checklist["tasks"].([]any)
			if operation == "complete" {
				if len(tasks) != 2 {
					t.Fatalf("participant checklist tasks = %+v", tasks)
				}
				task := tasks[0].(map[string]any)
				who, completed := task["completed_by_user"].(map[string]any)
				date, _ := task["completion_date"].(float64)
				if task["id"] != float64(1) || !completed || who["id"] != float64(f.owner.ID) || date <= 0 {
					t.Fatalf("participant completion = %+v", task)
				}
			} else if len(tasks) != 3 || tasks[2].(map[string]any)["id"] != float64(3) || tasks[2].(map[string]any)["text"] != "eggs" {
				t.Fatalf("participant append = %+v", tasks)
			}
		})
	}
}

func TestTodoReplacementChannelAuthorOnlyRPC(t *testing.T) {
	f := newBotAPIReceiveFixture(t, false)
	peer := &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}
	sent := sendTestTodo(t, f.router, f.bot.ID, peer, 99002)
	req := &tg.MessagesEditMessageRequest{Peer: peer, ID: sent.ID}
	req.SetMedia(&tg.InputMediaTodo{Todo: tg.TodoList{Title: tg.TextWithEntities{Text: "new"}, List: []tg.TodoItem{{ID: 7, Title: tg.TextWithEntities{Text: "new task"}}}}})
	if _, err := f.router.onMessagesEditMessage(WithUserID(f.ctx, f.owner.ID), req); !tgerr.Is(err, "MESSAGE_AUTHOR_REQUIRED") {
		t.Fatalf("creator replacing other's checklist = %v", err)
	}
	if _, err := f.router.onMessagesEditMessage(WithUserID(f.ctx, f.bot.ID), req); err != nil {
		t.Fatal(err)
	}
	history, err := f.channels.GetMessages(f.ctx, f.owner.ID, f.channel.ID, []int{sent.ID})
	if err != nil || len(history.Messages) != 1 || history.Messages[0].Media.Todo.Title != "new" {
		t.Fatalf("replacement = %+v, %v", history, err)
	}
}

func TestTodoReplacementPreservesParticipantCompletionAndBotEditRPC(t *testing.T) {
	for _, channel := range []bool{false, true} {
		t.Run(map[bool]string{false: "private", true: "channel"}[channel], func(t *testing.T) {
			f := newBotAPIReceiveFixture(t, true)
			f.router.clock = fixedClock{now: time.Unix(1700000000, 0)}
			var peer tg.InputPeerClass = inputPeerUser(f.bot)
			var participantPeer tg.InputPeerClass = inputPeerUser(f.owner)
			if channel {
				peer = &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}
				participantPeer = peer
			}
			sent := sendTestTodoWithFlags(t, f.router, f.owner.ID, peer, 99301, true, true)
			participantID := sent.ID
			if !channel {
				participantID = privateMessageForPeer(t, f.router, f.bot.ID, domain.Peer{Type: domain.PeerTypeUser, ID: f.owner.ID}).ID
			}
			f.router.clock = fixedClock{now: time.Unix(1700000010, 0)}
			if _, err := f.router.onMessagesToggleTodoCompleted(WithUserID(f.ctx, f.bot.ID), &tg.MessagesToggleTodoCompletedRequest{
				Peer: participantPeer, MsgID: participantID, Completed: []int{1, 2},
			}); err != nil {
				t.Fatal(err)
			}
			if err := f.router.BotAPIDropPendingUpdates(f.ctx, f.bot.ID); err != nil {
				t.Fatal(err)
			}
			f.router.clock = fixedClock{now: time.Unix(1700000020, 0)}
			req := &tg.MessagesEditMessageRequest{Peer: peer, ID: sent.ID}
			req.SetMedia(&tg.InputMediaTodo{Todo: tg.TodoList{
				Title: tg.TextWithEntities{Text: "corrected list"}, OthersCanAppend: true, OthersCanComplete: true,
				List: []tg.TodoItem{{ID: 1, Title: tg.TextWithEntities{Text: "corrected item"}}, {ID: 3, Title: tg.TextWithEntities{Text: "new item"}}},
			}})
			updates, err := f.router.onMessagesEditMessage(WithUserID(f.ctx, f.owner.ID), req)
			if err != nil {
				t.Fatal(err)
			}
			var edited *tg.Message
			if channel {
				edited = findUpdate[*tg.UpdateEditChannelMessage](t, updates).Message.(*tg.Message)
			} else {
				edited = editedMessageFromUpdates(t, updates)
			}
			completions, _ := edited.Media.(*tg.MessageMediaToDo).GetCompletions()
			if len(completions) != 1 || completions[0].ID != 1 || completions[0].Date != 1700000010 {
				t.Fatalf("replacement completions = %+v", completions)
			}
			if who, ok := completions[0].CompletedBy.(*tg.PeerUser); !ok || who.UserID != f.bot.ID {
				t.Fatalf("replacement completed_by = %#v", completions[0].CompletedBy)
			}
			message := editedMediaHTTPMessage(t, f)
			checklist := message["checklist"].(map[string]any)
			tasks := checklist["tasks"].([]any)
			if checklist["title"] != "corrected list" || len(tasks) != 2 {
				t.Fatalf("replacement checklist = %+v", checklist)
			}
			first, second := tasks[0].(map[string]any), tasks[1].(map[string]any)
			who, completed := first["completed_by_user"].(map[string]any)
			if first["id"] != float64(1) || first["text"] != "corrected item" || !completed || who["id"] != float64(f.bot.ID) || first["completion_date"] != float64(1700000010) || second["id"] != float64(3) || second["text"] != "new item" {
				t.Fatalf("replacement task transitions = %+v", tasks)
			}
			if _, completed := second["completion_date"]; completed {
				t.Fatalf("new task inherited deleted completion: %+v", second)
			}
			want := []domain.MessageTodoCompletion{{ID: 1, CompletedBy: f.bot.ID, Date: 1700000010}}
			// Appending must not discard completions, and toggling must still clear
			// them rather than being mistaken for a definition replacement.
			if _, err := f.router.onMessagesAppendTodoList(WithUserID(f.ctx, f.bot.ID), &tg.MessagesAppendTodoListRequest{
				Peer: participantPeer, MsgID: participantID, List: []tg.TodoItem{{ID: 4, Title: tg.TextWithEntities{Text: "appended"}}},
			}); err != nil {
				t.Fatal(err)
			}
			peerDomain := domain.Peer{Type: domain.PeerTypeUser, ID: f.owner.ID}
			if channel {
				peerDomain = domain.Peer{Type: domain.PeerTypeChannel, ID: f.channel.ID}
			}
			current, found := f.router.loadTodoMessage(f.ctx, f.bot.ID, peerDomain, participantID)
			if !found || len(current.media.Todo.Items) != 3 || current.media.Todo.Items[2].ID != 4 || !slices.Equal(current.media.Todo.Completions, want) {
				t.Fatalf("append snapshot = %+v, found=%v", current, found)
			}
			if _, err := f.router.onMessagesToggleTodoCompleted(WithUserID(f.ctx, f.bot.ID), &tg.MessagesToggleTodoCompletedRequest{
				Peer: participantPeer, MsgID: participantID, Incompleted: []int{1},
			}); err != nil {
				t.Fatal(err)
			}
			current, found = f.router.loadTodoMessage(f.ctx, f.bot.ID, peerDomain, participantID)
			if !found || len(current.media.Todo.Completions) != 0 {
				t.Fatalf("toggle retained completion after replacement: %+v, found=%v", current, found)
			}
		})
	}
}

func editedMediaHTTPMessage(t *testing.T, f botAPIReceiveFixture) map[string]any {
	t.Helper()
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	server, err := botapi.Start(ctx, "127.0.0.1:0", f.router.deps.Bots.(botapi.BotsService), nil, nil, nil, f.router, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/bot%d:secret/getUpdates", f.bot.ID), strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	var body struct {
		OK     bool             `json:"ok"`
		Result []map[string]any `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || !body.OK || len(body.Result) != 1 {
		t.Fatalf("HTTP getUpdates = %s, err=%v", response.Body.String(), err)
	}
	message, ok := body.Result[0]["edited_message"].(map[string]any)
	if !ok {
		t.Fatalf("missing edited_message = %+v", body.Result[0])
	}
	return message
}
