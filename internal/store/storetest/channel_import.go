package storetest

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// ChannelImportFixture 是一个频道主、一个读者与对应的存储；每个子测试各要一份。
// Channels 与 Imports 是同一个后端对象的两个接口。
type ChannelImportFixture struct {
	Channels      store.ChannelStore
	Imports       store.ChannelImportStore
	Owner, Reader int64
}

// ChannelImport 校验两种实现对频道导入的同一契约：来源消息 id 与日期原样落地（含跳号）、
// 重发幂等、内容变化按编辑处理、导入之后普通发送不撞 id、本地发过帖之后来源的新帖经映射表
// 拿到本地 id、回填阶段不写逐条事件而镜像阶段写。
func ChannelImport(t *testing.T, newFixture func(t *testing.T) ChannelImportFixture) {
	t.Helper()
	ctx := context.Background()
	now := baseNow()
	// 来源日期在几年前：导入必须原样保留，而不是改成当前时间。
	const past = 1_500_000_000

	createChannel := func(t *testing.T, f ChannelImportFixture, req domain.CreateChannelRequest) domain.Channel {
		t.Helper()
		req.CreatorUserID = f.Owner
		if req.Title == "" {
			req.Title = "Import contract"
		}
		if !req.Megagroup {
			req.Broadcast = true
		}
		if req.Date == 0 {
			req.Date = now
		}
		created, err := f.Channels.CreateChannel(ctx, req)
		if err != nil {
			t.Fatalf("CreateChannel: %v", err)
		}
		return created.Channel
	}
	createImport := func(t *testing.T, f ChannelImportFixture, channel domain.Channel) domain.ChannelImport {
		t.Helper()
		imp, err := f.Imports.CreateChannelImport(ctx, domain.ChannelImport{ChannelID: channel.ID, SenderUserID: f.Owner})
		if err != nil {
			t.Fatalf("CreateChannelImport: %v", err)
		}
		if imp.State != domain.ChannelImportImporting || imp.ID == 0 {
			t.Fatalf("new import = %+v, want importing with an id", imp)
		}
		return imp
	}
	importBatch := func(t *testing.T, f ChannelImportFixture, imp domain.ChannelImport, messages ...domain.ImportedChannelMessage) domain.ImportChannelMessagesResult {
		t.Helper()
		res, err := f.Imports.ImportChannelMessages(ctx, domain.ImportChannelMessagesRequest{ImportID: imp.ID, Messages: messages, Now: now})
		if err != nil {
			t.Fatalf("ImportChannelMessages: %v", err)
		}
		return res
	}
	loadMessages := func(t *testing.T, f ChannelImportFixture, channel domain.Channel, ids ...int) map[int]domain.ChannelMessage {
		t.Helper()
		history, err := f.Channels.GetChannelMessages(ctx, f.Owner, channel.ID, ids)
		if err != nil {
			t.Fatalf("GetChannelMessages: %v", err)
		}
		out := make(map[int]domain.ChannelMessage, len(history.Messages))
		for _, msg := range history.Messages {
			out[msg.ID] = msg
		}
		return out
	}
	text := func(sourceID int, body string) domain.ImportedChannelMessage {
		return domain.ImportedChannelMessage{SourceID: sourceID, Date: past + sourceID, Body: body}
	}

	t.Run("backfill keeps source ids, gaps and dates", func(t *testing.T) {
		f := newFixture(t)
		channel := createChannel(t, f, domain.CreateChannelRequest{MemberUserIDs: []int64{f.Reader}})
		imp := createImport(t, f, channel)
		ptsBefore := channel.Pts

		second := text(3, "second")
		second.Views, second.Forwards = 1200, 34
		pinned := text(5, "pinned")
		pinned.Pinned = true
		reply := text(9, "reply")
		reply.ReplyToSourceID = 3
		orphan := text(12, "reply to a deleted post")
		orphan.ReplyToSourceID = 7
		res := importBatch(t, f, imp, text(2, "first"), second, pinned, reply, orphan)
		if res.Written != 5 || res.Edited != 0 || res.Unchanged != 0 {
			t.Fatalf("counts = written %d edited %d unchanged %d, want 5/0/0", res.Written, res.Edited, res.Unchanged)
		}
		for _, id := range []int{2, 3, 5, 9, 12} {
			if res.LocalIDs[id] != id {
				t.Fatalf("local id for source %d = %d, want the source id", id, res.LocalIDs[id])
			}
		}
		if len(res.Events) != 0 {
			t.Fatalf("backfill produced %d events, want none", len(res.Events))
		}
		if res.Import.MaxSourceID != 12 {
			t.Fatalf("max source id = %d, want 12", res.Import.MaxSourceID)
		}
		if res.Channel.TopMessageID != 12 || res.Channel.PinnedMessageID != 5 {
			t.Fatalf("channel top/pinned = %d/%d, want 12/5", res.Channel.TopMessageID, res.Channel.PinnedMessageID)
		}
		if res.Channel.Pts != ptsBefore+1 {
			t.Fatalf("channel pts = %d, want one step past %d for the whole batch", res.Channel.Pts, ptsBefore)
		}

		got := loadMessages(t, f, channel, 2, 3, 4, 5, 9, 12)
		if len(got) != 5 {
			t.Fatalf("loaded %d messages, want 5 (id 4 is a gap)", len(got))
		}
		if got[3].Date != past+3 || got[3].Body != "second" || !got[3].Post {
			t.Fatalf("message 3 = %+v, want the source date and body on a post", got[3])
		}
		if got[3].ViewsCount != 1200 || got[3].ForwardsCount != 34 {
			t.Fatalf("message 3 counters = %d/%d, want 1200/34", got[3].ViewsCount, got[3].ForwardsCount)
		}
		if !got[5].Pinned {
			t.Fatal("message 5 is not pinned")
		}
		if got[9].ReplyTo == nil || got[9].ReplyTo.MessageID != 3 {
			t.Fatalf("message 9 reply = %+v, want message 3", got[9].ReplyTo)
		}
		if got[12].ReplyTo == nil || got[12].ReplyTo.MessageID != 7 {
			t.Fatalf("message 12 reply = %+v, want the unmapped source id 7 kept as is", got[12].ReplyTo)
		}
		if got[2].ExpiresAt != 0 {
			t.Fatalf("message 2 expires at %d, want no expiry", got[2].ExpiresAt)
		}

		// 游标还停在回填之前的客户端拿到 tooLong，而不是一串逐条事件。
		diff, err := f.Channels.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{UserID: f.Reader, ChannelID: channel.ID, Pts: ptsBefore, Limit: 100})
		if err != nil {
			t.Fatalf("ListChannelDifference: %v", err)
		}
		if !diff.TooLong || diff.Pts != res.Channel.Pts {
			t.Fatalf("difference = tooLong %v pts %d, want tooLong at pts %d", diff.TooLong, diff.Pts, res.Channel.Pts)
		}

		stats, err := f.Imports.ChannelImportStats(ctx, imp.ID)
		if err != nil {
			t.Fatalf("ChannelImportStats: %v", err)
		}
		if stats.Messages != 5 {
			t.Fatalf("stats messages = %d, want 5", stats.Messages)
		}
	})

	t.Run("resend is a no-op and changed content is an edit", func(t *testing.T) {
		f := newFixture(t)
		channel := createChannel(t, f, domain.CreateChannelRequest{})
		imp := createImport(t, f, channel)
		first := importBatch(t, f, imp, text(2, "one"), text(3, "two"))

		again := importBatch(t, f, imp, text(2, "one"), text(3, "two"))
		if again.Written != 0 || again.Edited != 0 || again.Unchanged != 2 {
			t.Fatalf("resend counts = %d/%d/%d, want 0/0/2", again.Written, again.Edited, again.Unchanged)
		}
		if again.Channel.Pts != first.Channel.Pts {
			t.Fatalf("resend moved pts %d -> %d", first.Channel.Pts, again.Channel.Pts)
		}

		// 只有计数变化：不算编辑，但计数只增不减地刷新。
		counted := text(2, "one")
		counted.Views = 50
		res := importBatch(t, f, imp, counted)
		if res.Unchanged != 1 || res.Edited != 0 {
			t.Fatalf("counter refresh counts = edited %d unchanged %d, want 0/1", res.Edited, res.Unchanged)
		}
		lower := text(2, "one")
		lower.Views = 10
		importBatch(t, f, imp, lower)
		if got := loadMessages(t, f, channel, 2)[2].ViewsCount; got != 50 {
			t.Fatalf("views = %d, want 50 (never lowered)", got)
		}

		edited := text(3, "two, edited")
		edited.EditDate = past + 500
		res = importBatch(t, f, imp, edited)
		if res.Edited != 1 || res.Written != 0 {
			t.Fatalf("edit counts = written %d edited %d, want 0/1", res.Written, res.Edited)
		}
		got := loadMessages(t, f, channel, 3)[3]
		if got.Body != "two, edited" || got.EditDate != past+500 || got.Date != past+3 {
			t.Fatalf("edited message = body %q edit %d date %d", got.Body, got.EditDate, got.Date)
		}
	})

	t.Run("local posts never collide and later source posts are remapped", func(t *testing.T) {
		f := newFixture(t)
		channel := createChannel(t, f, domain.CreateChannelRequest{MemberUserIDs: []int64{f.Reader}})
		imp := createImport(t, f, channel)
		importBatch(t, f, imp, text(2, "a"), text(40, "b"))

		sent, err := f.Channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: f.Owner, ChannelID: channel.ID, RandomID: 91, Message: "local", Date: now})
		if err != nil {
			t.Fatalf("SendChannelMessage after import: %v", err)
		}
		if sent.Message.ID != 41 {
			t.Fatalf("local post id = %d, want 41 (right after the imported maximum)", sent.Message.ID)
		}

		if _, err := f.Imports.SetChannelImportState(ctx, imp.ID, domain.ChannelImportMirroring); err != nil {
			t.Fatalf("SetChannelImportState: %v", err)
		}
		ptsBefore := sent.Channel.Pts
		follow := text(42, "source reply")
		follow.ReplyToSourceID = 41
		res := importBatch(t, f, imp, text(41, "source post"), follow)
		if res.Written != 2 {
			t.Fatalf("mirrored written = %d, want 2", res.Written)
		}
		// 来源 41 的号已被本地帖子占用：它拿到下一个本地 id，来源 42 也不再沿用自己的号。
		if res.LocalIDs[41] != 42 || res.LocalIDs[42] != 43 {
			t.Fatalf("local ids = %v, want 41->42 and 42->43", res.LocalIDs)
		}
		if len(res.Events) != 2 {
			t.Fatalf("mirroring produced %d events, want 2", len(res.Events))
		}
		for i, event := range res.Events {
			if event.Type != domain.ChannelUpdateNewMessage || event.Date != now || event.Pts != ptsBefore+i+1 {
				t.Fatalf("event %d = type %s date %d pts %d, want new message at server time with consecutive pts", i, event.Type, event.Date, event.Pts)
			}
			if event.Message.Date == now {
				t.Fatalf("event %d message date is the server time, want the source date", i)
			}
		}
		got := loadMessages(t, f, channel, 42, 43)
		if got[43].ReplyTo == nil || got[43].ReplyTo.MessageID != 42 {
			t.Fatalf("remapped reply = %+v, want local message 42", got[43].ReplyTo)
		}
		resolved, err := f.Imports.ResolveChannelImportMessageIDs(ctx, imp.ID, []int{2, 41, 42, 77})
		if err != nil {
			t.Fatalf("ResolveChannelImportMessageIDs: %v", err)
		}
		if len(resolved) != 3 || resolved[2] != 2 || resolved[41] != 42 || resolved[42] != 43 {
			t.Fatalf("resolved = %v, want 2->2 41->42 42->43 and no entry for 77", resolved)
		}

		// 镜像阶段的事件能被游标落后的成员按序补拉到。
		diff, err := f.Channels.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{UserID: f.Reader, ChannelID: channel.ID, Pts: ptsBefore, Limit: 100})
		if err != nil {
			t.Fatalf("ListChannelDifference: %v", err)
		}
		if diff.TooLong || len(diff.NewMessages) != 2 {
			t.Fatalf("difference = tooLong %v with %d new messages, want 2 new messages", diff.TooLong, len(diff.NewMessages))
		}

		// 镜像阶段的编辑同样产生事件。
		edited := text(41, "source post, edited")
		edited.EditDate = past + 900
		res = importBatch(t, f, imp, edited)
		if res.Edited != 1 || len(res.Events) != 1 || res.Events[0].Type != domain.ChannelUpdateEditMessage || res.Events[0].Message.ID != 42 {
			t.Fatalf("mirrored edit = edited %d events %+v, want one edit event for local message 42", res.Edited, res.Events)
		}
	})

	t.Run("order and target rules", func(t *testing.T) {
		f := newFixture(t)
		channel := createChannel(t, f, domain.CreateChannelRequest{})
		imp := createImport(t, f, channel)
		importBatch(t, f, imp, text(5, "five"), text(8, "eight"))

		if _, err := f.Imports.ImportChannelMessages(ctx, domain.ImportChannelMessagesRequest{ImportID: imp.ID, Messages: []domain.ImportedChannelMessage{text(10, "x"), text(9, "y")}}); !errors.Is(err, domain.ErrChannelImportOrder) {
			t.Fatalf("descending batch err = %v, want ErrChannelImportOrder", err)
		}
		// 6 从未写入过，却排在已写入的最大来源 id 8 之前。
		if _, err := f.Imports.ImportChannelMessages(ctx, domain.ImportChannelMessagesRequest{ImportID: imp.ID, Messages: []domain.ImportedChannelMessage{text(6, "late")}}); !errors.Is(err, domain.ErrChannelImportOrder) {
			t.Fatalf("late message err = %v, want ErrChannelImportOrder", err)
		}
		// 失败的一批不留痕迹：同批里排在前面的合法消息也没有写入。
		if _, err := f.Imports.ImportChannelMessages(ctx, domain.ImportChannelMessagesRequest{ImportID: imp.ID, Messages: []domain.ImportedChannelMessage{text(6, "late"), text(20, "fine")}}); !errors.Is(err, domain.ErrChannelImportOrder) {
			t.Fatalf("mixed batch err = %v, want ErrChannelImportOrder", err)
		}
		if got := loadMessages(t, f, channel, 6, 20); len(got) != 0 {
			t.Fatalf("rejected batch left %d messages behind", len(got))
		}
		if _, err := f.Imports.ImportChannelMessages(ctx, domain.ImportChannelMessagesRequest{ImportID: imp.ID, Messages: []domain.ImportedChannelMessage{{SourceID: 30, Date: past}}}); !errors.Is(err, domain.ErrChannelInvalid) {
			t.Fatalf("empty message err = %v, want ErrChannelInvalid", err)
		}

		if _, err := f.Imports.CreateChannelImport(ctx, domain.ChannelImport{ChannelID: channel.ID, SenderUserID: f.Owner}); !errors.Is(err, domain.ErrChannelImportExists) {
			t.Fatalf("second import err = %v, want ErrChannelImportExists", err)
		}
		group := createChannel(t, f, domain.CreateChannelRequest{Megagroup: true})
		if _, err := f.Imports.CreateChannelImport(ctx, domain.ChannelImport{ChannelID: group.ID, SenderUserID: f.Owner}); !errors.Is(err, domain.ErrChannelImportTarget) {
			t.Fatalf("megagroup import err = %v, want ErrChannelImportTarget", err)
		}
		used := createChannel(t, f, domain.CreateChannelRequest{})
		if _, err := f.Channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: f.Owner, ChannelID: used.ID, RandomID: 92, Message: "already posted", Date: now}); err != nil {
			t.Fatalf("SendChannelMessage: %v", err)
		}
		if _, err := f.Imports.CreateChannelImport(ctx, domain.ChannelImport{ChannelID: used.ID, SenderUserID: f.Owner}); !errors.Is(err, domain.ErrChannelImportTarget) {
			t.Fatalf("non-empty channel import err = %v, want ErrChannelImportTarget", err)
		}

		if _, err := f.Imports.SetChannelImportState(ctx, imp.ID, domain.ChannelImportDetached); err != nil {
			t.Fatalf("SetChannelImportState detached: %v", err)
		}
		if _, err := f.Imports.ImportChannelMessages(ctx, domain.ImportChannelMessagesRequest{ImportID: imp.ID, Messages: []domain.ImportedChannelMessage{text(50, "after detach")}}); !errors.Is(err, domain.ErrChannelImportState) {
			t.Fatalf("detached import err = %v, want ErrChannelImportState", err)
		}
		if _, err := f.Imports.ImportChannelMessages(ctx, domain.ImportChannelMessagesRequest{ImportID: imp.ID + 1000, Messages: []domain.ImportedChannelMessage{text(50, "x")}}); !errors.Is(err, domain.ErrChannelImportNotFound) {
			t.Fatalf("unknown import err = %v, want ErrChannelImportNotFound", err)
		}
	})

	t.Run("source uniqueness and media map", func(t *testing.T) {
		f := newFixture(t)
		// 每份 fixture 的 owner id 不同，用它派生来源频道 id，避免共用库的后端互相撞。
		source := 5_000_000_000 + f.Owner
		first := createChannel(t, f, domain.CreateChannelRequest{})
		imp, err := f.Imports.CreateChannelImport(ctx, domain.ChannelImport{ChannelID: first.ID, SenderUserID: f.Owner, SourceChannelID: source, SourceUsername: "source"})
		if err != nil {
			t.Fatalf("CreateChannelImport: %v", err)
		}
		found, ok, err := f.Imports.FindChannelImportBySource(ctx, source)
		if err != nil || !ok || found.ID != imp.ID || found.SourceUsername != "source" {
			t.Fatalf("FindChannelImportBySource = %+v ok %v err %v, want import %d", found, ok, err, imp.ID)
		}
		second := createChannel(t, f, domain.CreateChannelRequest{})
		if _, err := f.Imports.CreateChannelImport(ctx, domain.ChannelImport{ChannelID: second.ID, SenderUserID: f.Owner, SourceChannelID: source}); !errors.Is(err, domain.ErrChannelImportExists) {
			t.Fatalf("duplicate source err = %v, want ErrChannelImportExists", err)
		}
		// detached 之后同一个来源可以重新克隆。
		if _, err := f.Imports.SetChannelImportState(ctx, imp.ID, domain.ChannelImportDetached); err != nil {
			t.Fatalf("SetChannelImportState: %v", err)
		}
		if _, ok, err := f.Imports.FindChannelImportBySource(ctx, source); err != nil || ok {
			t.Fatalf("detached import still found by source: ok %v err %v", ok, err)
		}
		if _, err := f.Imports.CreateChannelImport(ctx, domain.ChannelImport{ChannelID: second.ID, SenderUserID: f.Owner, SourceChannelID: source}); err != nil {
			t.Fatalf("re-import after detach: %v", err)
		}

		if _, ok, err := f.Imports.GetChannelImportMedia(ctx, imp.ID, "file-a"); err != nil || ok {
			t.Fatalf("unknown media: ok %v err %v", ok, err)
		}
		put, err := f.Imports.PutChannelImportMedia(ctx, imp.ID, "file-a", domain.ChannelImportMedia{Kind: domain.ChannelImportMediaPhoto, MediaID: 111})
		if err != nil || put.MediaID != 111 {
			t.Fatalf("PutChannelImportMedia = %+v err %v", put, err)
		}
		// 同一个来源文件第二次上传：返回第一次的映射。
		put, err = f.Imports.PutChannelImportMedia(ctx, imp.ID, "file-a", domain.ChannelImportMedia{Kind: domain.ChannelImportMediaPhoto, MediaID: 222})
		if err != nil || put.MediaID != 111 {
			t.Fatalf("second PutChannelImportMedia = %+v err %v, want the first media id", put, err)
		}
		got, ok, err := f.Imports.GetChannelImportMedia(ctx, imp.ID, "file-a")
		if err != nil || !ok || got.MediaID != 111 || got.Kind != domain.ChannelImportMediaPhoto {
			t.Fatalf("GetChannelImportMedia = %+v ok %v err %v", got, ok, err)
		}
		stats, err := f.Imports.ChannelImportStats(ctx, imp.ID)
		if err != nil || stats.Media != 1 {
			t.Fatalf("stats = %+v err %v, want one media", stats, err)
		}
	})

	t.Run("channel ttl does not expire imported posts", func(t *testing.T) {
		f := newFixture(t)
		channel := createChannel(t, f, domain.CreateChannelRequest{TTLPeriod: 3600})
		imp := createImport(t, f, channel)
		importBatch(t, f, imp, text(2, "old post"))
		got := loadMessages(t, f, channel, 2)[2]
		if got.ExpiresAt != 0 || got.TTLPeriod != 0 {
			t.Fatalf("imported post ttl = %d expires %d, want none", got.TTLPeriod, got.ExpiresAt)
		}
	})

	t.Run("create message is rewritten and members are marked read", func(t *testing.T) {
		f := newFixture(t)
		channel := createChannel(t, f, domain.CreateChannelRequest{Title: "Current title", MemberUserIDs: []int64{f.Reader}})
		imp := createImport(t, f, channel)
		if err := f.Imports.RewriteChannelCreateMessage(ctx, domain.RewriteChannelCreateRequest{ChannelID: channel.ID, Date: past, Title: "Original title"}); err != nil {
			t.Fatalf("RewriteChannelCreateMessage: %v", err)
		}
		created := loadMessages(t, f, channel, 1)[1]
		if created.Date != past || created.Action == nil || created.Action.Type != domain.ChannelActionCreate || created.Action.Title != "Original title" {
			t.Fatalf("create message = date %d action %+v, want the source date and title", created.Date, created.Action)
		}
		stored, err := f.Channels.GetChannelByID(ctx, channel.ID)
		if err != nil {
			t.Fatalf("GetChannelByID: %v", err)
		}
		if stored.Date != past || stored.Title != "Current title" {
			t.Fatalf("channel = date %d title %q, want the source date and the current title", stored.Date, stored.Title)
		}

		// 镜像来源的标题和简介：不产生服务消息，也不推进 pts。
		title, about := "Renamed at source", "about text"
		renamed, err := f.Imports.SetChannelImportMeta(ctx, channel.ID, &title, &about)
		if err != nil {
			t.Fatalf("SetChannelImportMeta: %v", err)
		}
		if renamed.Title != title || renamed.About != about || renamed.Pts != stored.Pts || renamed.TopMessageID != stored.TopMessageID {
			t.Fatalf("meta update = title %q about %q pts %d top %d, want the new text with pts %d and top %d untouched",
				renamed.Title, renamed.About, renamed.Pts, renamed.TopMessageID, stored.Pts, stored.TopMessageID)
		}
		if kept, err := f.Imports.SetChannelImportMeta(ctx, channel.ID, nil, nil); err != nil || kept.Title != title || kept.About != about {
			t.Fatalf("no-op meta update = %+v err %v, want nothing changed", kept, err)
		}
		empty := ""
		if _, err := f.Imports.SetChannelImportMeta(ctx, channel.ID, &empty, nil); !errors.Is(err, domain.ErrChannelTitleInvalid) {
			t.Fatalf("empty title err = %v, want ErrChannelTitleInvalid", err)
		}

		importBatch(t, f, imp, text(2, "a"), text(3, "b"), text(4, "c"))
		view, err := f.Channels.GetChannel(ctx, f.Reader, channel.ID)
		if err != nil {
			t.Fatalf("GetChannel: %v", err)
		}
		if view.Dialog.UnreadCount == 0 {
			t.Fatal("reader has no unread before MarkChannelImportRead; the check below would prove nothing")
		}
		if err := f.Imports.MarkChannelImportRead(ctx, channel.ID); err != nil {
			t.Fatalf("MarkChannelImportRead: %v", err)
		}
		view, err = f.Channels.GetChannel(ctx, f.Reader, channel.ID)
		if err != nil {
			t.Fatalf("GetChannel: %v", err)
		}
		if view.Dialog.UnreadCount != 0 || view.Dialog.ReadInboxMaxID != 4 {
			t.Fatalf("reader dialog = unread %d read max %d, want 0 and 4", view.Dialog.UnreadCount, view.Dialog.ReadInboxMaxID)
		}

		// 来源的创建消息已不存在：本地这条留成空号。
		if err := f.Imports.RewriteChannelCreateMessage(ctx, domain.RewriteChannelCreateRequest{ChannelID: channel.ID, Date: past, Title: "Original title", Drop: true}); err != nil {
			t.Fatalf("RewriteChannelCreateMessage drop: %v", err)
		}
		if got := loadMessages(t, f, channel, 1); len(got) != 0 && !got[1].Deleted {
			t.Fatalf("dropped create message is still visible: %+v", got[1])
		}
	})
}
