package channelimport

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"telesrv/internal/domain"
)

func TestPingReachesTheStore(t *testing.T) {
	h := newHarness(t)
	if err := h.svc.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	var unconfigured *Service
	if err := unconfigured.Ping(context.Background()); err == nil {
		t.Fatal("Ping on an unconfigured service succeeded")
	}
}

func TestChannelReportsOwnerCountsAndImport(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	res := h.create(t, CreateRequest{SourceChannelID: 4242, Title: "Lookup", About: "about", CreatedDate: past})
	pinned := text(3, "pinned")
	pinned.Pinned = true
	h.importMessages(t, res.Import.ID, text(2, "a"), pinned, text(8, "c"))

	info, err := h.svc.Channel(ctx, res.Channel.ID)
	if err != nil {
		t.Fatalf("Channel: %v", err)
	}
	if info.Channel.Title != "Lookup" || info.Channel.About != "about" || info.Channel.Date != past || info.Channel.PinnedMessageID != 3 || info.Channel.Pts == 0 {
		t.Fatalf("channel = %+v", info.Channel)
	}
	if info.Owner == nil || info.Owner.ID != testOwner || info.Owner.FirstName != "Owner" {
		t.Fatalf("owner = %+v, want the channel creator", info.Owner)
	}
	// 4 条：频道已创建 + 3 条导入的；最后一条消息带来源日期。
	if info.MessageCount != 4 || info.LastMessageID != 8 || info.LastMessageDate != past+8 {
		t.Fatalf("counts = %d messages, last %d at %d", info.MessageCount, info.LastMessageID, info.LastMessageDate)
	}
	if info.Import == nil || info.Import.ID != res.Import.ID || info.Import.MaxSourceID != 8 || info.Importable() {
		t.Fatalf("import = %+v importable %v, want the import and not importable", info.Import, info.Importable())
	}

	// 没有任务的空广播频道可以作为导入目标；超级群不行。
	empty, err := h.store.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: testReader, Title: "Empty", Broadcast: true, Date: past})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	info, err = h.svc.Channel(ctx, empty.Channel.ID)
	if err != nil || !info.Importable() || info.Import != nil || info.MessageCount != 1 || info.Owner.ID != testReader {
		t.Fatalf("empty channel = %+v err %v, want an importable channel owned by the reader", info, err)
	}
	group, err := h.store.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: 9999, Title: "Group", Megagroup: true, Date: past})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	// 频道主的账号查不到：Owner 为 nil，不是错误。
	if info, err := h.svc.Channel(ctx, group.Channel.ID); err != nil || info.Importable() || info.Owner != nil {
		t.Fatalf("megagroup = %+v err %v, want not importable and no owner", info, err)
	}
	for _, id := range []int64{0, -5, 1 << 40} {
		if _, err := h.svc.Channel(ctx, id); !errors.Is(err, ErrChannelNotFound) {
			t.Fatalf("Channel(%d) err = %v, want ErrChannelNotFound", id, err)
		}
	}
}

func TestSearchChannelsFindsEveryNamesake(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	first := h.create(t, CreateRequest{Title: "Daily News", CreatedDate: past})
	second := h.create(t, CreateRequest{Title: "daily news", OwnerUserID: testReader, CreatedDate: past + 100})
	other := h.create(t, CreateRequest{Title: "Weekly digest", CreatedDate: past + 200})
	h.importMessages(t, first.Import.ID, Message{ID: 2, Date: past + 5000, Text: "latest"})
	if _, err := h.svc.Finalize(ctx, other.Import.ID, FinalizeRequest{Username: "daily"}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	found, err := h.svc.SearchChannels(ctx, ChannelSearch{Query: " DAILY NEWS "})
	if err != nil {
		t.Fatalf("SearchChannels: %v", err)
	}
	// 同名的两个都在，最近有消息的在前；各自带着自己的频道主和创建时间。
	if len(found) != 2 || found[0].Channel.ID != first.Channel.ID || found[1].Channel.ID != second.Channel.ID {
		t.Fatalf("search = %+v, want both namesakes with the most recently active first", found)
	}
	if found[0].Owner.ID != testOwner || found[1].Owner.ID != testReader || found[1].Channel.Date != past+100 || found[0].LastMessageDate != past+5000 {
		t.Fatalf("namesakes = owner %d/%d created %d last %d", found[0].Owner.ID, found[1].Owner.ID, found[1].Channel.Date, found[0].LastMessageDate)
	}
	if found[0].MessageCount != -1 || found[0].Import == nil {
		t.Fatalf("list item = count %d import %+v, want no count and the import", found[0].MessageCount, found[0].Import)
	}

	// 单个词：标题包含匹配，同时接受与它完全相同的用户名。
	found, err = h.svc.SearchChannels(ctx, ChannelSearch{Query: "daily"})
	if err != nil || len(found) != 3 {
		t.Fatalf("search daily = %d results err %v, want the two titles and the username", len(found), err)
	}
	// @ 开头：只按用户名精确匹配。
	found, err = h.svc.SearchChannels(ctx, ChannelSearch{Query: "@Daily"})
	if err != nil || len(found) != 1 || found[0].Channel.ID != other.Channel.ID {
		t.Fatalf("search @Daily = %+v err %v, want only the channel holding the username", found, err)
	}
	if found, err := h.svc.SearchChannels(ctx, ChannelSearch{Query: "@dail"}); err != nil || len(found) != 0 {
		t.Fatalf("search @dail = %d results err %v, want none", len(found), err)
	}
	paged, err := h.svc.SearchChannels(ctx, ChannelSearch{Query: "daily news", Limit: 1, Offset: 1})
	if err != nil || len(paged) != 1 || paged[0].Channel.ID != second.Channel.ID {
		t.Fatalf("paged search = %+v err %v", paged, err)
	}
	for _, query := range []string{"", "   ", "@"} {
		if _, err := h.svc.SearchChannels(ctx, ChannelSearch{Query: query}); !errors.Is(err, domain.ErrChannelInvalid) {
			t.Fatalf("search %q err = %v, want ErrChannelInvalid", query, err)
		}
	}
}

func TestListFindsImportsBySourceAndState(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	first := h.create(t, CreateRequest{SourceChannelID: 111, Title: "First"})
	second := h.create(t, CreateRequest{SourceChannelID: 222, Title: "Second"})
	if _, err := h.svc.Finalize(ctx, second.Import.ID, FinalizeRequest{}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	all, err := h.svc.List(ctx, domain.ChannelImportFilter{})
	if err != nil || len(all) != 2 || all[0].Import.ID != second.Import.ID || all[0].Channel.Title != "Second" || all[1].Channel.Title != "First" {
		t.Fatalf("list = %+v err %v, want both imports, newest first, with their channels", all, err)
	}
	bySource, err := h.svc.List(ctx, domain.ChannelImportFilter{SourceChannelID: 111})
	if err != nil || len(bySource) != 1 || bySource[0].Import.ID != first.Import.ID {
		t.Fatalf("list by source = %+v err %v", bySource, err)
	}
	mirroring, err := h.svc.List(ctx, domain.ChannelImportFilter{State: domain.ChannelImportMirroring})
	if err != nil || len(mirroring) != 1 || mirroring[0].Import.ID != second.Import.ID {
		t.Fatalf("list mirroring = %+v err %v", mirroring, err)
	}
	if _, err := h.svc.List(ctx, domain.ChannelImportFilter{State: "paused"}); !errors.Is(err, domain.ErrChannelInvalid) {
		t.Fatalf("unknown state err = %v, want ErrChannelInvalid", err)
	}
}

func TestChannelMessagesAreViewedInTheWriteFormat(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	origin := h.create(t, CreateRequest{SourceChannelID: 111, Title: "Origin"})
	h.importMessages(t, origin.Import.ID, text(5, "origin post"))
	res := h.create(t, CreateRequest{Title: "Mirror"})
	imp, channelID := res.Import, res.Channel.ID
	if _, err := h.svc.PutMedia(ctx, imp.ID, MediaUpload{
		SourceKey: "video-1", Kind: domain.ChannelImportMediaDocument, MimeType: "video/mp4",
		Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrVideo, W: 640, H: 360, Duration: 12.5}, {Kind: domain.DocAttrFilename, FileName: "clip.mp4"}},
		Body:       strings.NewReader("mp4-bytes"),
	}); err != nil {
		t.Fatalf("PutMedia: %v", err)
	}
	rich := Message{
		ID: 2, Date: past + 2, EditDate: past + 50, Text: "bold link",
		Entities:   []Entity{{Type: "bold", Offset: 0, Length: 4}, {Type: "text_url", Offset: 5, Length: 4, URL: "https://example.com"}},
		Media:      &Media{SourceKey: "video-1", Spoiler: true},
		GroupedID:  13_000_000_000_000_001,
		Forward:    &Forward{FromSourceChannelID: 111, ChannelPost: 5, Date: past},
		PostAuthor: "Editor", Views: 900, Forwards: 7, Silent: true, Pinned: true,
		Buttons: [][]Button{{{Text: "Open", URL: "https://example.com"}}},
	}
	reply := Message{ID: 4, Date: past + 4, Text: "reply", ReplyToMsgID: 2}
	renamed := Message{ID: 6, Date: past + 6, Action: &Action{Type: "edit_title", Title: "Old title"}}
	h.importMessages(t, imp.ID, rich, reply, renamed)
	if _, err := h.store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: testOwner, ChannelID: channelID, RandomID: 6, Message: "local", Date: int(h.now.Unix())}); err != nil {
		t.Fatalf("SendChannelMessage: %v", err)
	}
	if _, err := h.svc.DeleteMessages(ctx, imp.ID, []int{4}, "cloner"); err != nil {
		t.Fatalf("DeleteMessages: %v", err)
	}

	page, err := h.svc.ChannelMessages(ctx, channelID, 0, 3)
	if err != nil {
		t.Fatalf("ChannelMessages: %v", err)
	}
	// 从新到旧：本地帖子 7、服务消息 6、来源 2（4 已删除）。
	if len(page) != 3 || page[0].ID != 7 || page[1].ID != 6 || page[2].ID != 2 {
		t.Fatalf("page ids = %+v, want 7, 6, 2", page)
	}
	if local := page[0]; local.SourceID != nil || local.Text != "local" || local.Date != int(h.now.Unix()) {
		t.Fatalf("local post = %+v, want no source id", local)
	}
	if service := page[1]; service.SourceID == nil || *service.SourceID != 6 || service.Action == nil || service.Action.Type != "edit_title" || service.Action.Title != "Old title" {
		t.Fatalf("service message = %+v action %+v", service, service.Action)
	}
	got := page[2]
	if got.SourceID == nil || *got.SourceID != 2 || got.Date != past+2 || got.EditDate != past+50 || got.Text != "bold link" ||
		got.PostAuthor != "Editor" || got.Views != 900 || got.Forwards != 7 || !got.Silent || !got.Pinned || got.GroupedID != 13_000_000_000_000_001 {
		t.Fatalf("mirrored message = %+v", got)
	}
	if len(got.Entities) != 2 || got.Entities[0].Type != "bold" || got.Entities[1].URL != "https://example.com" {
		t.Fatalf("entities = %+v", got.Entities)
	}
	if m := got.Media; m == nil || m.Kind != "document" || m.MimeType != "video/mp4" || m.FileName != "clip.mp4" || m.Width != 640 || m.Height != 360 ||
		m.Duration != 12.5 || m.Size != int64(len("mp4-bytes")) || !m.Video || !m.Spoiler || m.MediaID == 0 {
		t.Fatalf("media = %+v", m)
	}
	if f := got.Forward; f == nil || f.FromChannelID != origin.Channel.ID || f.ChannelPost != 5 || f.Date != past {
		t.Fatalf("forward = %+v, want the local origin channel", f)
	}
	if len(got.Buttons) != 1 || got.Buttons[0][0] != (Button{Text: "Open", URL: "https://example.com"}) {
		t.Fatalf("buttons = %+v", got.Buttons)
	}
	// 线上格式：没有来源的消息 source_id 为 null，相册 id 是字符串。
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if text := string(raw); !strings.Contains(text, `"source_id":null`) || !strings.Contains(text, `"grouped_id":"13000000000000001"`) {
		t.Fatalf("wire format = %s", text)
	}

	// 翻页：before_id 之前只剩“频道已创建”。
	rest, err := h.svc.ChannelMessages(ctx, channelID, 2, 50)
	if err != nil || len(rest) != 1 || rest[0].ID != 1 || rest[0].Action == nil || rest[0].Action.Type != "channel_create" {
		t.Fatalf("rest = %+v err %v, want only the create message under its domain action name", rest, err)
	}
	if _, err := h.svc.ChannelMessages(ctx, 1<<40, 0, 10); !errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("unknown channel err = %v, want ErrChannelNotFound", err)
	}
}

func TestResolveMessageIDs(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	res := h.create(t, CreateRequest{})
	h.importMessages(t, res.Import.ID, text(2, "a"))
	if _, err := h.store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: testOwner, ChannelID: res.Channel.ID, RandomID: 8, Message: "local"}); err != nil {
		t.Fatalf("SendChannelMessage: %v", err)
	}
	h.importMessages(t, res.Import.ID, text(3, "b"))

	mapped, err := h.svc.ResolveMessageIDs(ctx, res.Import.ID, []int{2, 3, 99})
	if err != nil || len(mapped) != 2 || mapped[2] != 2 || mapped[3] != 4 {
		t.Fatalf("resolved = %v err %v, want 2->2 and 3->4", mapped, err)
	}
	if _, err := h.svc.ResolveMessageIDs(ctx, res.Import.ID, nil); !errors.Is(err, domain.ErrChannelInvalid) {
		t.Fatalf("empty resolve err = %v, want ErrChannelInvalid", err)
	}
	if _, err := h.svc.ResolveMessageIDs(ctx, res.Import.ID+50, []int{2}); !errors.Is(err, domain.ErrChannelImportNotFound) {
		t.Fatalf("unknown import err = %v, want ErrChannelImportNotFound", err)
	}
}
