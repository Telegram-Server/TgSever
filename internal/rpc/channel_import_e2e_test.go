package rpc

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/app/channelimport"
	appchannels "telesrv/internal/app/channels"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// importTestFiles 是频道导入要用到的最小媒体后端：只存照片。
type importTestFiles struct {
	nextID int64
	photos map[int64]domain.Photo
}

func (f *importTestFiles) CreatePhotoFromBytesAt(_ context.Context, _ []byte, date int) (domain.Photo, error) {
	f.nextID++
	photo := domain.Photo{ID: f.nextID, AccessHash: 77, Date: date, DCID: 2, Sizes: []domain.PhotoSize{{Type: "x", W: 640, H: 360, Size: 1000}}}
	f.photos[photo.ID] = photo
	return photo, nil
}

func (f *importTestFiles) CreateDocumentFromReaderWithThumb(context.Context, io.Reader, domain.DocumentSpec) (domain.Document, error) {
	return domain.Document{}, errors.New("documents are not part of this test")
}

func (f *importTestFiles) CreateAvatarFromBytes(context.Context, []byte) (domain.Photo, error) {
	return domain.Photo{}, errors.New("avatars are not part of this test")
}

func (f *importTestFiles) GetPhoto(_ context.Context, id int64) (domain.Photo, bool, error) {
	photo, ok := f.photos[id]
	return photo, ok, nil
}

func (f *importTestFiles) GetDocument(context.Context, int64) (domain.Document, bool, error) {
	return domain.Document{}, false, nil
}

func dispatchImportRPC(t *testing.T, r *Router, ctx context.Context, req bin.Encoder) bin.Encoder {
	t.Helper()
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode %T: %v", req, err)
	}
	out, err := r.Dispatch(ctx, [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch %T: %v", req, err)
	}
	return out
}

// TestChannelImportEndToEnd 走一遍频道克隆在客户端眼里的样子：导入服务经 router 的下发钩子
// 接到真实的 RPC 层上，回填一个带跳号、回复、照片、置顶和服务消息的频道，finalize 之后由
// 一个新用户按用户名找到并加入，读历史、搜索、看未读；随后频道主照常发帖，来源的新帖经
// 镜像实时到达成员。业务规则的矩阵在 app/channelimport 的测试里，这里只验证接线。
func TestChannelImportEndToEnd(t *testing.T) {
	ctx := context.Background()
	const past = 1_500_000_000

	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550007001", FirstName: "Owner"})
	reader, _ := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550007002", FirstName: "Reader"})
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	sessions := &captureScopedSessions{captureSessions: &captureSessions{}}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	files := &importTestFiles{nextID: 9000, photos: map[int64]domain.Photo{}}
	imports := channelimport.NewService(channelStore, channelService, files, userStore, r)

	// 建任务：频道主的在线会话收到新频道。
	created, err := imports.Create(ctx, channelimport.CreateRequest{
		SourceChannelID: 1234567890, SourceUsername: "source",
		OwnerUserID: owner.ID, Title: "Mirror", About: "cloned",
		CreatedDate: past, CreateTitle: "First title",
	})
	if err != nil {
		t.Fatalf("create import: %v", err)
	}
	importID, channelID := created.Import.ID, created.Channel.ID
	if !slices.Contains(sessions.pushedUserIDs(), owner.ID) {
		t.Fatalf("pushed users = %v, want the owner to be told about the new channel", sessions.pushedUserIDs())
	}
	sessions.clearMessages()

	if _, err := imports.PutMedia(ctx, importID, channelimport.MediaUpload{
		SourceKey: "photo-1", Kind: domain.ChannelImportMediaPhoto, Date: past, Body: strings.NewReader("jpeg"),
	}); err != nil {
		t.Fatalf("put media: %v", err)
	}
	photoPost := channelimport.Message{ID: 3, Date: past + 30, Text: "photo caption", Media: &channelimport.Media{SourceKey: "photo-1"}, Views: 4321, Pinned: true}
	reply := channelimport.Message{ID: 7, Date: past + 70, Text: "needle in the haystack", ReplyToMsgID: 3, PostAuthor: "Editor"}
	batch, err := imports.ImportMessages(ctx, importID, []channelimport.Message{
		{ID: 2, Date: past + 20, Text: "first post", Entities: []channelimport.Entity{{Type: "bold", Offset: 0, Length: 5}}},
		photoPost,
		{ID: 5, Date: past + 50, Action: &channelimport.Action{Type: "edit_title", Title: "Second title"}},
		reply,
	})
	if err != nil {
		t.Fatalf("import backfill: %v", err)
	}
	if batch.Written != 4 || len(batch.Skipped) != 0 {
		t.Fatalf("backfill = %+v, want 4 written", batch)
	}
	// 回填不下发任何更新。
	if pushed := sessions.pushedUserIDs(); len(pushed) != 0 {
		t.Fatalf("backfill pushed updates to %v", pushed)
	}

	if _, err := imports.Finalize(ctx, importID, channelimport.FinalizeRequest{Username: "mirror_channel"}); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	sessions.clearMessages()

	// 新用户按用户名找到频道并加入。
	readerCtx := WithUserID(ctx, reader.ID)
	resolved, err := r.onContactsResolveUsername(readerCtx, &tg.ContactsResolveUsernameRequest{Username: "mirror_channel"})
	if err != nil {
		t.Fatalf("resolve username: %v", err)
	}
	if len(resolved.Chats) != 1 {
		t.Fatalf("resolved chats = %d, want the mirrored channel", len(resolved.Chats))
	}
	channel := resolved.Chats[0].(*tg.Channel)
	if channel.ID != channelID || !channel.Broadcast || channel.Title != "Mirror" || channel.Date != past {
		t.Fatalf("resolved channel = id %d broadcast %v title %q date %d", channel.ID, channel.Broadcast, channel.Title, channel.Date)
	}
	input := &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	if _, err := r.onChannelsJoinChannel(readerCtx, input); err != nil {
		t.Fatalf("join channel: %v", err)
	}

	// 历史：来源的 id（含跳号）、日期、回复、置顶、浏览数、服务消息都在。
	peer := &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	history, ok := dispatchImportRPC(t, r, readerCtx, &tg.MessagesGetHistoryRequest{Peer: peer, Limit: 50}).(*tg.MessagesChannelMessages)
	if !ok {
		t.Fatal("history is not messages.channelMessages")
	}
	byID := map[int]tg.MessageClass{}
	var ids []int
	for _, m := range history.Messages {
		byID[m.GetID()] = m
		ids = append(ids, m.GetID())
	}
	if !slices.Equal(ids, []int{7, 5, 3, 2, 1}) {
		t.Fatalf("history ids = %v, want [7 5 3 2 1] with the source gaps", ids)
	}
	first := byID[2].(*tg.Message)
	if first.Date != past+20 || first.Message != "first post" || !first.Post || len(first.Entities) != 1 {
		t.Fatalf("message 2 = date %d text %q post %v entities %d", first.Date, first.Message, first.Post, len(first.Entities))
	}
	withPhoto := byID[3].(*tg.Message)
	if _, isPhoto := withPhoto.Media.(*tg.MessageMediaPhoto); !isPhoto || !withPhoto.Pinned || withPhoto.Views != 4321 {
		t.Fatalf("message 3 = media %T pinned %v views %d", withPhoto.Media, withPhoto.Pinned, withPhoto.Views)
	}
	replied := byID[7].(*tg.Message)
	header, ok := replied.ReplyTo.(*tg.MessageReplyHeader)
	if !ok || header.ReplyToMsgID != 3 || replied.Date != past+70 {
		t.Fatalf("message 7 = reply %#v date %d", replied.ReplyTo, replied.Date)
	}
	renamed := byID[5].(*tg.MessageService)
	if action, ok := renamed.Action.(*tg.MessageActionChatEditTitle); !ok || action.Title != "Second title" || renamed.Date != past+50 {
		t.Fatalf("message 5 = action %#v date %d", renamed.Action, renamed.Date)
	}
	createdMsg := byID[1].(*tg.MessageService)
	if action, ok := createdMsg.Action.(*tg.MessageActionChannelCreate); !ok || action.Title != "First title" || createdMsg.Date != past {
		t.Fatalf("message 1 = action %#v date %d", createdMsg.Action, createdMsg.Date)
	}

	// 频道内搜索能找到导入的正文。
	found, ok := dispatchImportRPC(t, r, readerCtx, &tg.MessagesSearchRequest{
		Peer: peer, Q: "needle", Filter: &tg.InputMessagesFilterEmpty{}, Limit: 20,
	}).(*tg.MessagesChannelMessages)
	if !ok || len(found.Messages) != 1 || found.Messages[0].GetID() != 7 {
		t.Fatalf("search result = %#v, want message 7", found)
	}

	// 新加入的成员没有一屏未读。
	full, err := r.onChannelsGetFullChannel(readerCtx, input)
	if err != nil {
		t.Fatalf("get full channel: %v", err)
	}
	if info := full.FullChat.(*tg.ChannelFull); info.UnreadCount != 0 || info.PinnedMsgID != 3 || info.About != "cloned" {
		t.Fatalf("full channel = unread %d pinned %d about %q", info.UnreadCount, info.PinnedMsgID, info.About)
	}

	// 频道主照常发帖：紧接在导入的最大 id 之后。
	sent, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{Peer: peer, Message: "posted locally", RandomID: 501})
	if err != nil {
		t.Fatalf("owner send: %v", err)
	}
	if id, ok := sent.(*tg.Updates).Updates[0].(*tg.UpdateMessageID); !ok || id.ID != 8 {
		t.Fatalf("local post = %#v, want id 8", sent.(*tg.Updates).Updates[0])
	}
	sessions.clearMessages()

	// 镜像：来源的新帖实时到达成员，带着来源日期；它的号已被本地帖子占用，落在 9 上。
	mirrored, err := imports.ImportMessages(ctx, importID, []channelimport.Message{{ID: 8, Date: past + 80, Text: "new at source"}})
	if err != nil {
		t.Fatalf("import mirrored post: %v", err)
	}
	if mirrored.LocalIDs[8] != 9 {
		t.Fatalf("mirrored local id = %d, want 9", mirrored.LocalIDs[8])
	}
	if !slices.Contains(sessions.pushedUserIDs(), reader.ID) {
		t.Fatalf("pushed users = %v, want the reader to receive the mirrored post", sessions.pushedUserIDs())
	}
	pushed, ok := sessions.lastUserPush().(*tg.Updates)
	if !ok || len(pushed.Updates) != 1 {
		t.Fatalf("pushed update = %#v, want one update", sessions.lastUserPush())
	}
	update, ok := pushed.Updates[0].(*tg.UpdateNewChannelMessage)
	if !ok {
		t.Fatalf("pushed update = %T, want updateNewChannelMessage", pushed.Updates[0])
	}
	if msg := update.Message.(*tg.Message); msg.ID != 9 || msg.Date != past+80 || msg.Message != "new at source" {
		t.Fatalf("pushed message = id %d date %d text %q", msg.ID, msg.Date, msg.Message)
	}

	// 来源删除一条帖子：本地对应的消息从历史里消失。
	if _, err := imports.DeleteMessages(ctx, importID, []int{2}, "cloner"); err != nil {
		t.Fatalf("delete at source: %v", err)
	}
	after := dispatchImportRPC(t, r, readerCtx, &tg.MessagesGetHistoryRequest{Peer: peer, Limit: 50}).(*tg.MessagesChannelMessages)
	ids = ids[:0]
	for _, m := range after.Messages {
		ids = append(ids, m.GetID())
	}
	if !slices.Equal(ids, []int{9, 8, 7, 5, 3, 1}) {
		t.Fatalf("history ids after mirroring = %v, want [9 8 7 5 3 1]", ids)
	}
}
