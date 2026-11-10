package channelimport

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	appchannels "telesrv/internal/app/channels"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

const (
	testOwner  = int64(1001)
	testReader = int64(1002)
	testBot    = int64(1003)
	// 来源日期在几年前。
	past = 1_500_000_000
)

type fakeUsers map[int64]domain.User

func (f fakeUsers) ByID(_ context.Context, id int64) (domain.User, bool, error) {
	u, ok := f[id]
	return u, ok, nil
}

// fakeFiles 记录创建请求并按 id 返回创建过的媒体。
type fakeFiles struct {
	nextID    int64
	photos    map[int64]domain.Photo
	documents map[int64]domain.Document
	specs     map[int64]domain.DocumentSpec
	bodies    map[int64]string
	avatars   int
}

func newFakeFiles() *fakeFiles {
	return &fakeFiles{nextID: 500, photos: map[int64]domain.Photo{}, documents: map[int64]domain.Document{}, specs: map[int64]domain.DocumentSpec{}, bodies: map[int64]string{}}
}

func (f *fakeFiles) CreatePhotoFromBytesAt(_ context.Context, data []byte, date int) (domain.Photo, error) {
	f.nextID++
	photo := domain.Photo{ID: f.nextID, AccessHash: 1, Date: date}
	f.photos[photo.ID] = photo
	f.bodies[photo.ID] = string(data)
	return photo, nil
}

func (f *fakeFiles) CreateDocumentFromReaderWithThumb(_ context.Context, r io.Reader, spec domain.DocumentSpec) (domain.Document, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return domain.Document{}, err
	}
	f.nextID++
	doc := domain.Document{ID: f.nextID, AccessHash: 1, Date: spec.Date, MimeType: spec.MimeType, Size: int64(len(data)), Attributes: spec.Attributes}
	f.documents[doc.ID] = doc
	f.specs[doc.ID] = spec
	f.bodies[doc.ID] = string(data)
	return doc, nil
}

func (f *fakeFiles) CreateAvatarFromBytes(_ context.Context, data []byte) (domain.Photo, error) {
	f.avatars++
	f.nextID++
	photo := domain.Photo{ID: f.nextID, AccessHash: 1, DCID: 2}
	f.photos[photo.ID] = photo
	return photo, nil
}

func (f *fakeFiles) GetPhoto(_ context.Context, id int64) (domain.Photo, bool, error) {
	p, ok := f.photos[id]
	return p, ok, nil
}

func (f *fakeFiles) GetDocument(_ context.Context, id int64) (domain.Document, bool, error) {
	d, ok := f.documents[id]
	return d, ok, nil
}

// fakeNotifier 记录每一次实时下发。
type fakeNotifier struct {
	created   []domain.CreateChannelResult
	events    [][]domain.ChannelUpdateEvent
	senders   []int64
	refreshed []int64
	changed   []domain.Channel
	deletions []domain.DeleteChannelMessagesResult
}

func (f *fakeNotifier) NotifyChannelImportCreated(_ context.Context, res domain.CreateChannelResult) {
	f.created = append(f.created, res)
}

func (f *fakeNotifier) NotifyChannelImportEvents(_ context.Context, _ domain.Channel, senderUserID int64, events []domain.ChannelUpdateEvent) {
	f.events = append(f.events, events)
	f.senders = append(f.senders, senderUserID)
}

// testChannels 是真实的频道服务，另外能让某个用户名被占用（内存后端没有全局用户名索引）。
type testChannels struct {
	*appchannels.Service
	takenUsername string
}

func (c *testChannels) AdminSetUsername(ctx context.Context, channelID int64, username string) (domain.Channel, error) {
	if c.takenUsername != "" && username == c.takenUsername {
		return domain.Channel{}, domain.ErrUsernameOccupied
	}
	return c.Service.AdminSetUsername(ctx, channelID, username)
}

func (f *fakeNotifier) NotifyChannelImportRefreshed(_ context.Context, channel domain.Channel) {
	f.refreshed = append(f.refreshed, channel.ID)
}

func (f *fakeNotifier) NotifyChannelChanged(_ context.Context, channel domain.Channel) error {
	f.changed = append(f.changed, channel)
	return nil
}

func (f *fakeNotifier) NotifyModerationChannelDeletion(_ context.Context, res domain.DeleteChannelMessagesResult) {
	f.deletions = append(f.deletions, res)
}

type harness struct {
	svc      *Service
	channels *testChannels
	store    *memory.ChannelStore
	files    *fakeFiles
	notifier *fakeNotifier
	now      time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st := memory.NewChannelStore()
	channels := &testChannels{Service: appchannels.NewService(st)}
	files := newFakeFiles()
	notifier := &fakeNotifier{}
	users := fakeUsers{
		testOwner:  {ID: testOwner, FirstName: "Owner"},
		testReader: {ID: testReader, FirstName: "Reader"},
		testBot:    {ID: testBot, FirstName: "Bot", Bot: true},
	}
	h := &harness{store: st, channels: channels, files: files, notifier: notifier, now: time.Unix(1_800_000_000, 0)}
	h.svc = NewService(st, channels, files, users, notifier)
	h.svc.now = func() time.Time { return h.now }
	return h
}

func (h *harness) create(t *testing.T, req CreateRequest) CreateResult {
	t.Helper()
	if req.TargetChannelID == 0 && req.OwnerUserID == 0 {
		req.OwnerUserID = testOwner
	}
	if req.TargetChannelID == 0 && req.Title == "" {
		req.Title = "Mirror"
	}
	res, err := h.svc.Create(context.Background(), req)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return res
}

func (h *harness) importMessages(t *testing.T, importID int64, messages ...Message) BatchResult {
	t.Helper()
	res, err := h.svc.ImportMessages(context.Background(), importID, messages)
	if err != nil {
		t.Fatalf("ImportMessages: %v", err)
	}
	return res
}

func (h *harness) messages(t *testing.T, channelID int64, ids ...int) map[int]domain.ChannelMessage {
	t.Helper()
	history, err := h.store.GetChannelMessages(context.Background(), testOwner, channelID, ids)
	if err != nil {
		t.Fatalf("GetChannelMessages: %v", err)
	}
	out := make(map[int]domain.ChannelMessage, len(history.Messages))
	for _, msg := range history.Messages {
		out[msg.ID] = msg
	}
	return out
}

func text(id int, body string) Message {
	return Message{ID: id, Date: past + id, Text: body}
}

func TestCreateBuildsABroadcastChannelDatedAtTheSource(t *testing.T) {
	h := newHarness(t)
	res := h.create(t, CreateRequest{
		SourceChannelID: 777, SourceUsername: "@source",
		Title: "Current title", About: "about", CreatedDate: past, CreateTitle: "Original title",
	})
	if res.Existing || res.Import.State != domain.ChannelImportImporting || res.Import.SenderUserID != testOwner || res.Import.SourceUsername != "source" {
		t.Fatalf("import = %+v existing %v", res.Import, res.Existing)
	}
	channel, err := h.store.GetChannelByID(context.Background(), res.Channel.ID)
	if err != nil {
		t.Fatalf("GetChannelByID: %v", err)
	}
	if !channel.Broadcast || channel.Megagroup || channel.CreatorUserID != testOwner || channel.Title != "Current title" || channel.About != "about" || channel.Date != past {
		t.Fatalf("channel = %+v, want a broadcast channel owned by %d dated at the source", channel, testOwner)
	}
	created := h.messages(t, channel.ID, 1)[1]
	if created.Date != past || created.Action == nil || created.Action.Title != "Original title" {
		t.Fatalf("create message = date %d action %+v, want the source date and the title at creation", created.Date, created.Action)
	}
	if len(h.notifier.created) != 1 || h.notifier.created[0].Message.Action.Title != "Original title" {
		t.Fatalf("created notifications = %+v, want one carrying the original title", h.notifier.created)
	}

	// 同一个来源再建一次：返回已有的任务，不克隆第二个频道。
	again := h.create(t, CreateRequest{SourceChannelID: 777, Title: "Another"})
	if !again.Existing || again.Import.ID != res.Import.ID || again.Channel.ID != res.Channel.ID {
		t.Fatalf("second create = %+v, want the existing import %d", again, res.Import.ID)
	}
	if len(h.notifier.created) != 1 {
		t.Fatalf("second create announced a channel")
	}
}

func TestCreateRejectsOwnersThatCannotOwnAChannel(t *testing.T) {
	h := newHarness(t)
	for name, req := range map[string]CreateRequest{
		"missing owner": {Title: "x", OwnerUserID: 9999},
		"bot owner":     {Title: "x", OwnerUserID: testBot},
		"bot sender":    {Title: "x", OwnerUserID: testOwner, SenderUserID: testBot},
	} {
		if _, err := h.svc.Create(context.Background(), req); !errors.Is(err, ErrOwnerInvalid) {
			t.Fatalf("%s: err = %v, want ErrOwnerInvalid", name, err)
		}
	}
	if _, err := h.svc.Create(context.Background(), CreateRequest{OwnerUserID: testOwner, Title: "  "}); !errors.Is(err, domain.ErrChannelTitleInvalid) {
		t.Fatalf("blank title err = %v, want ErrChannelTitleInvalid", err)
	}
}

func TestCreateIntoAnExistingEmptyChannel(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	made, err := h.store.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: testOwner, Title: "Made in the client", Broadcast: true, Date: int(h.now.Unix())})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	res := h.create(t, CreateRequest{TargetChannelID: made.Channel.ID, CreatedDate: past})
	if res.Import.SenderUserID != testOwner || res.Channel.ID != made.Channel.ID {
		t.Fatalf("import = %+v, want the existing channel with its creator as sender", res.Import)
	}
	created := h.messages(t, made.Channel.ID, 1)[1]
	if created.Date != past || created.Action.Title != "Made in the client" {
		t.Fatalf("create message = date %d title %q, want the source date and the unchanged title", created.Date, created.Action.Title)
	}
	if len(h.notifier.created) != 0 {
		t.Fatal("importing into an existing channel announced a new channel")
	}

	// 已经发过帖的频道不能作为目标。
	used, err := h.store.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: testOwner, Title: "Used", Broadcast: true, Date: int(h.now.Unix())})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if _, err := h.store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: testOwner, ChannelID: used.Channel.ID, RandomID: 1, Message: "post"}); err != nil {
		t.Fatalf("SendChannelMessage: %v", err)
	}
	if _, err := h.svc.Create(ctx, CreateRequest{TargetChannelID: used.Channel.ID}); !errors.Is(err, domain.ErrChannelImportTarget) {
		t.Fatalf("non-empty target err = %v, want ErrChannelImportTarget", err)
	}
}

func TestPutMediaStoresOnceAndKeepsTheSourceDate(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	imp := h.create(t, CreateRequest{}).Import

	photo, err := h.svc.PutMedia(ctx, imp.ID, MediaUpload{SourceKey: "photo-1", Kind: domain.ChannelImportMediaPhoto, Date: past, Body: strings.NewReader("jpeg")})
	if err != nil || photo.Existing || photo.Media.Kind != domain.ChannelImportMediaPhoto {
		t.Fatalf("photo upload = %+v err %v", photo, err)
	}
	if got := h.files.photos[photo.Media.MediaID].Date; got != past {
		t.Fatalf("photo date = %d, want the source date", got)
	}

	doc, err := h.svc.PutMedia(ctx, imp.ID, MediaUpload{
		SourceKey: "video-1", Kind: domain.ChannelImportMediaDocument, Date: past + 1, MimeType: "video/mp4",
		Attributes: []domain.DocumentAttribute{
			{Kind: domain.DocAttrVideo, W: 640, H: 360, Duration: 12},
			{Kind: domain.DocAttrFilename, FileName: "clip.mp4"},
			{Kind: domain.DocAttrSticker, Alt: "x", StickerSetID: 42, StickerSetAccessHash: 43},
			{Kind: domain.DocAttrCustomEmoji, Alt: "y"},
		},
		Body: strings.NewReader("mp4-bytes"),
	})
	if err != nil {
		t.Fatalf("document upload: %v", err)
	}
	spec := h.files.specs[doc.Media.MediaID]
	if spec.Date != past+1 || spec.MimeType != "video/mp4" || h.files.bodies[doc.Media.MediaID] != "mp4-bytes" {
		t.Fatalf("document spec = %+v body %q", spec, h.files.bodies[doc.Media.MediaID])
	}
	// 自定义表情属性丢弃，贴纸属性去掉指向来源贴纸集的 id。
	if len(spec.Attributes) != 3 || spec.Attributes[2].Kind != domain.DocAttrSticker || spec.Attributes[2].StickerSetID != 0 || spec.Attributes[2].StickerSetAccessHash != 0 {
		t.Fatalf("document attributes = %+v, want video, filename and a sticker attribute without a set", spec.Attributes)
	}

	// 同一个来源文件第二次上传：返回第一次的媒体，不读 Body。
	created := len(h.files.photos)
	again, err := h.svc.PutMedia(ctx, imp.ID, MediaUpload{SourceKey: "photo-1", Kind: domain.ChannelImportMediaPhoto, Body: failingReader{}})
	if err != nil || !again.Existing || again.Media.MediaID != photo.Media.MediaID || len(h.files.photos) != created {
		t.Fatalf("repeated upload = %+v err %v, want the first media without storing anything", again, err)
	}

	for name, upload := range map[string]MediaUpload{
		"no source key": {Kind: domain.ChannelImportMediaPhoto, Body: strings.NewReader("x")},
		"unknown kind":  {SourceKey: "k", Kind: "sticker", Body: strings.NewReader("x")},
		"empty photo":   {SourceKey: "k", Kind: domain.ChannelImportMediaPhoto, Body: strings.NewReader("")},
		"no body":       {SourceKey: "k", Kind: domain.ChannelImportMediaDocument},
	} {
		if _, err := h.svc.PutMedia(ctx, imp.ID, upload); !errors.Is(err, ErrMediaInvalid) {
			t.Fatalf("%s: err = %v, want ErrMediaInvalid", name, err)
		}
	}
	if _, err := h.svc.PutMedia(ctx, imp.ID+50, MediaUpload{SourceKey: "k", Kind: domain.ChannelImportMediaPhoto, Body: strings.NewReader("x")}); !errors.Is(err, domain.ErrChannelImportNotFound) {
		t.Fatalf("unknown import err = %v, want ErrChannelImportNotFound", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("body must not be read") }

func TestImportMessagesConvertsContent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	res := h.create(t, CreateRequest{Title: "Mirror"})
	imp, channelID := res.Import, res.Channel.ID
	for key, kind := range map[string]domain.ChannelImportMediaKind{"photo-1": domain.ChannelImportMediaPhoto, "avatar-1": domain.ChannelImportMediaPhoto} {
		if _, err := h.svc.PutMedia(ctx, imp.ID, MediaUpload{SourceKey: key, Kind: kind, Body: strings.NewReader("img")}); err != nil {
			t.Fatalf("PutMedia %s: %v", key, err)
		}
	}
	if _, err := h.svc.PutMedia(ctx, imp.ID, MediaUpload{
		SourceKey: "voice-1", Kind: domain.ChannelImportMediaDocument, MimeType: "audio/ogg",
		Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrAudio, Voice: true, AudioDuration: 3}},
		Body:       strings.NewReader("ogg"),
	}); err != nil {
		t.Fatalf("PutMedia voice: %v", err)
	}

	rich := text(2, "bold link emoji")
	rich.Entities = []Entity{
		{Type: "bold", Offset: 0, Length: 4},
		{Type: "text_url", Offset: 5, Length: 4, URL: "https://example.com"},
		{Type: "custom_emoji", Offset: 10, Length: 5},
		{Type: "text_url", Offset: 5, Length: 4},
	}
	rich.Buttons = [][]Button{{{Text: "Open", URL: "https://example.com"}, {Text: "Callback"}}, {{Text: "Only callback"}}}
	rich.PostAuthor = "Editor"
	rich.Views, rich.Forwards = 900, 7
	rich.Forward = &Forward{FromName: "Some Channel", Date: past - 100}

	album1 := Message{ID: 3, Date: past + 3, Media: &Media{SourceKey: "photo-1", Spoiler: true}, GroupedID: 13_000_000_000_000_001}
	voice := Message{ID: 4, Date: past + 4, Media: &Media{SourceKey: "voice-1"}}
	missing := Message{ID: 5, Date: past + 5, Text: "caption", Media: &Media{SourceKey: "never-uploaded"}}
	empty := Message{ID: 6, Date: past + 6}
	renamed := Message{ID: 7, Date: past + 7, Action: &Action{Type: "edit_title", Title: "Old title"}}
	newPhoto := Message{ID: 8, Date: past + 8, Action: &Action{Type: "edit_photo", PhotoSourceKey: "avatar-1"}}
	pin := Message{ID: 9, Date: past + 9, ReplyToMsgID: 2, Action: &Action{Type: "pin_message"}}
	giveaway := Message{ID: 10, Date: past + 10, Action: &Action{Type: "giveaway_launch"}}

	out := h.importMessages(t, imp.ID, rich, album1, voice, missing, empty, renamed, newPhoto, pin, giveaway)
	if out.Written != 6 || len(out.Skipped) != 3 {
		t.Fatalf("written %d skipped %+v, want 6 written and 3 skipped", out.Written, out.Skipped)
	}
	skipped := map[int]string{}
	for _, s := range out.Skipped {
		skipped[s.ID] = s.Reason
	}
	if !strings.Contains(skipped[5], "never-uploaded") || skipped[6] == "" || !strings.Contains(skipped[10], "giveaway_launch") {
		t.Fatalf("skipped = %v, want reasons for 5, 6 and 10", skipped)
	}
	if out.Import.MaxSourceID != 9 {
		t.Fatalf("max source id = %d, want 9 (the last written message)", out.Import.MaxSourceID)
	}

	got := h.messages(t, channelID, 2, 3, 4, 5, 6, 7, 8, 9, 10)
	if len(got) != 6 {
		t.Fatalf("stored %d messages, want 6 with gaps at 5, 6 and 10", len(got))
	}
	m := got[2]
	if len(m.Entities) != 2 || m.Entities[0].Type != domain.MessageEntityBold || m.Entities[1].URL != "https://example.com" {
		t.Fatalf("entities = %+v, want bold and one text_url", m.Entities)
	}
	if m.ReplyMarkup == nil || len(m.ReplyMarkup.Inline) != 1 || len(m.ReplyMarkup.Inline[0]) != 1 || m.ReplyMarkup.Inline[0][0].URL != "https://example.com" {
		t.Fatalf("reply markup = %+v, want a single URL button", m.ReplyMarkup)
	}
	if m.PostAuthor != "Editor" || m.ViewsCount != 900 || m.ForwardsCount != 7 || !m.Post || m.Date != past+2 {
		t.Fatalf("message 2 = %+v", m)
	}
	if m.Forward == nil || m.Forward.FromName != "Some Channel" || m.Forward.Date != past-100 || m.Forward.From.ID != 0 {
		t.Fatalf("forward = %+v, want a name-only header", m.Forward)
	}
	if p := got[3]; p.Media == nil || p.Media.Kind != domain.MessageMediaKindPhoto || !p.Media.Spoiler || p.GroupedID != 13_000_000_000_000_001 || p.Body != "" {
		t.Fatalf("message 3 = %+v media %+v", p, p.Media)
	}
	if v := got[4]; v.Media == nil || v.Media.Kind != domain.MessageMediaKindDocument || !v.Media.Voice {
		t.Fatalf("message 4 media = %+v, want a voice document", v.Media)
	}
	if a := got[7].Action; a == nil || a.Type != domain.ChannelActionEditTitle || a.Title != "Old title" {
		t.Fatalf("message 7 action = %+v", a)
	}
	if a := got[8].Action; a == nil || a.Type != domain.ChannelActionChatEditPhoto || a.Photo == nil {
		t.Fatalf("message 8 action = %+v", a)
	}
	if p := got[9]; p.Action == nil || p.Action.Type != domain.ChannelActionPinMessage || p.ReplyTo == nil || p.ReplyTo.MessageID != 2 {
		t.Fatalf("message 9 = action %+v reply %+v", p.Action, p.ReplyTo)
	}

	// 导入的服务消息只是历史：频道的标题、头像、置顶都没有变。
	channel, err := h.store.GetChannelByID(ctx, channelID)
	if err != nil {
		t.Fatalf("GetChannelByID: %v", err)
	}
	if channel.Title != "Mirror" || channel.PhotoID != 0 || channel.PinnedMessageID != 0 {
		t.Fatalf("channel = title %q photo %d pinned %d, want history actions to have no effect", channel.Title, channel.PhotoID, channel.PinnedMessageID)
	}
	// 回填阶段不下发。
	if len(h.notifier.events) != 0 {
		t.Fatalf("backfill notified %d times", len(h.notifier.events))
	}
}

func TestImportMessagesOrderAndLimits(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	imp := h.create(t, CreateRequest{}).Import
	if _, err := h.svc.ImportMessages(ctx, imp.ID, nil); !errors.Is(err, domain.ErrChannelInvalid) {
		t.Fatalf("empty batch err = %v", err)
	}
	if _, err := h.svc.ImportMessages(ctx, imp.ID, make([]Message, domain.MaxChannelImportBatch+1)); !errors.Is(err, domain.ErrChannelInvalid) {
		t.Fatalf("oversized batch err = %v", err)
	}
	// 被跳过的消息同样占着它的位置：排在它后面的 id 更小的消息仍然是乱序。
	if _, err := h.svc.ImportMessages(ctx, imp.ID, []Message{{ID: 5, Date: past}, text(4, "x")}); !errors.Is(err, domain.ErrChannelImportOrder) {
		t.Fatalf("out of order err = %v, want ErrChannelImportOrder", err)
	}
	if _, err := h.svc.ImportMessages(ctx, imp.ID, []Message{{ID: 5, Text: "no date"}}); !errors.Is(err, domain.ErrChannelInvalid) {
		t.Fatalf("missing date err = %v, want ErrChannelInvalid", err)
	}
	// 整批都被跳过：没有写入，也不是错误。
	out := h.importMessages(t, imp.ID, Message{ID: 5, Date: past})
	if out.Written != 0 || len(out.Skipped) != 1 {
		t.Fatalf("all-skipped batch = %+v", out)
	}
}

func TestForwardFromAnotherClonedChannelPointsAtTheLocalPost(t *testing.T) {
	h := newHarness(t)
	origin := h.create(t, CreateRequest{SourceChannelID: 111, Title: "Origin"})
	h.importMessages(t, origin.Import.ID, text(2, "a"), text(30, "the forwarded post"))
	// 在来源频道本地发一条帖子，之后来源的 31 会落到本地 32 上。
	if _, err := h.store.SendChannelMessage(context.Background(), domain.SendChannelMessageRequest{UserID: testOwner, ChannelID: origin.Channel.ID, RandomID: 5, Message: "local"}); err != nil {
		t.Fatalf("SendChannelMessage: %v", err)
	}
	h.importMessages(t, origin.Import.ID, text(31, "remapped"))

	mirror := h.create(t, CreateRequest{SourceChannelID: 222, Title: "Mirror"})
	same := text(2, "forward of 30")
	same.Forward = &Forward{FromName: "Origin", FromSourceChannelID: 111, ChannelPost: 30, Date: past}
	remapped := text(3, "forward of 31")
	remapped.Forward = &Forward{FromSourceChannelID: 111, ChannelPost: 31}
	unknown := text(4, "forward from a channel that was never cloned")
	unknown.Forward = &Forward{FromName: "Elsewhere", FromSourceChannelID: 999, ChannelPost: 8}
	h.importMessages(t, mirror.Import.ID, same, remapped, unknown)

	got := h.messages(t, mirror.Channel.ID, 2, 3, 4)
	want := domain.Peer{Type: domain.PeerTypeChannel, ID: origin.Channel.ID}
	if f := got[2].Forward; f == nil || f.From != want || f.ChannelPost != 30 || f.FromName != "" {
		t.Fatalf("forward 2 = %+v, want the local origin channel post 30", f)
	}
	if f := got[3].Forward; f == nil || f.From != want || f.ChannelPost != 32 {
		t.Fatalf("forward 3 = %+v, want the remapped local post 32", f)
	}
	if f := got[4].Forward; f == nil || f.From.ID != 0 || f.FromName != "Elsewhere" || f.ChannelPost != 0 {
		t.Fatalf("forward 4 = %+v, want a name-only header", f)
	}
}

func TestFinalizeOpensTheChannelAndMirroringNotifies(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	res := h.create(t, CreateRequest{})
	imp, channelID := res.Import, res.Channel.ID
	if _, err := h.store.InviteToChannel(ctx, channelID, testOwner, []int64{testReader}, int(h.now.Unix())); err != nil {
		t.Fatalf("InviteToChannel: %v", err)
	}
	h.importMessages(t, imp.ID, text(2, "a"), text(3, "b"))

	status, err := h.svc.Finalize(ctx, imp.ID, FinalizeRequest{Username: "@mirror_channel", ReserveThrough: 20})
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if status.Import.State != domain.ChannelImportMirroring || status.Import.FinalizedAt == 0 || status.Channel.Username != "mirror_channel" || status.Stats.Messages != 2 {
		t.Fatalf("finalized status = %+v", status)
	}
	if len(h.notifier.refreshed) != 1 || h.notifier.refreshed[0] != channelID || len(h.notifier.changed) != 1 {
		t.Fatalf("finalize notifications = refreshed %v changed %d, want one of each", h.notifier.refreshed, len(h.notifier.changed))
	}
	view, err := h.store.GetChannel(ctx, testReader, channelID)
	if err != nil {
		t.Fatalf("GetChannel: %v", err)
	}
	if view.Dialog.UnreadCount != 0 {
		t.Fatalf("reader unread after finalize = %d, want 0", view.Dialog.UnreadCount)
	}
	// 重复调用是空操作。
	if _, err := h.svc.Finalize(ctx, imp.ID, FinalizeRequest{Username: "other_name"}); err != nil {
		t.Fatalf("second Finalize: %v", err)
	}
	if len(h.notifier.refreshed) != 1 {
		t.Fatal("second Finalize refreshed members again")
	}

	// 预留的号段不被本地帖子占用。
	sent, err := h.store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: testOwner, ChannelID: channelID, RandomID: 9, Message: "local"})
	if err != nil {
		t.Fatalf("SendChannelMessage: %v", err)
	}
	if sent.Message.ID != 21 {
		t.Fatalf("local post id = %d, want 21 (after the reserved range)", sent.Message.ID)
	}

	// 镜像阶段的新帖和编辑实时下发给成员，事件时间是服务器时间。
	out := h.importMessages(t, imp.ID, text(25, "new at source"))
	if out.LocalIDs[25] != 25 {
		t.Fatalf("local id = %d, want the source id 25", out.LocalIDs[25])
	}
	edited := text(25, "new at source, edited")
	edited.EditDate = past + 99
	h.importMessages(t, imp.ID, edited)
	if len(h.notifier.events) != 2 {
		t.Fatalf("mirroring notified %d times, want 2", len(h.notifier.events))
	}
	first := h.notifier.events[0][0]
	if first.Type != domain.ChannelUpdateNewMessage || first.Date != int(h.now.Unix()) || first.Message.Date != past+25 {
		t.Fatalf("new message event = type %s date %d message date %d", first.Type, first.Date, first.Message.Date)
	}
	if second := h.notifier.events[1][0]; second.Type != domain.ChannelUpdateEditMessage || second.Message.Body != "new at source, edited" {
		t.Fatalf("edit event = %+v", second)
	}
	if h.notifier.senders[0] != testOwner {
		t.Fatalf("event sender = %d, want the import sender %d", h.notifier.senders[0], testOwner)
	}
}

func TestFinalizeKeepsImportingWhenTheUsernameIsTaken(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.channels.takenUsername = "taken_name"
	res := h.create(t, CreateRequest{})
	if _, err := h.svc.Finalize(ctx, res.Import.ID, FinalizeRequest{Username: "taken_name"}); !errors.Is(err, domain.ErrUsernameOccupied) {
		t.Fatalf("Finalize with a taken username err = %v, want ErrUsernameOccupied", err)
	}
	status, err := h.svc.Get(ctx, res.Import.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if status.Import.State != domain.ChannelImportImporting {
		t.Fatalf("state = %s, want importing so the caller can retry with another name", status.Import.State)
	}
	if len(h.notifier.refreshed) != 0 {
		t.Fatal("failed Finalize refreshed members")
	}
	status, err = h.svc.Finalize(ctx, res.Import.ID, FinalizeRequest{Username: "free_name"})
	if err != nil || status.Import.State != domain.ChannelImportMirroring || status.Channel.Username != "free_name" {
		t.Fatalf("retry Finalize = %+v err %v", status, err)
	}
}

func TestDeleteMessagesMapsSourceIDs(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	res := h.create(t, CreateRequest{})
	imp, channelID := res.Import, res.Channel.ID
	h.importMessages(t, imp.ID, text(2, "a"), text(3, "b"))
	if _, err := h.store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: testOwner, ChannelID: channelID, RandomID: 3, Message: "local"}); err != nil {
		t.Fatalf("SendChannelMessage: %v", err)
	}
	// 来源的 4 落在本地 5 上。
	h.importMessages(t, imp.ID, text(4, "c"))

	out, err := h.svc.DeleteMessages(ctx, imp.ID, []int{2, 4, 99}, "cloner")
	if err != nil {
		t.Fatalf("DeleteMessages: %v", err)
	}
	if len(out.Deleted) != 2 || len(out.Unknown) != 1 || out.Unknown[0] != 99 {
		t.Fatalf("delete result = %+v, want two deleted and 99 unknown", out)
	}
	left := h.messages(t, channelID, 2, 3, 4, 5)
	for id, msg := range left {
		if msg.Deleted {
			delete(left, id)
		}
	}
	if len(left) != 2 || left[3].Body != "b" || left[4].Body != "local" {
		t.Fatalf("remaining messages = %+v, want source 3 and the local post", left)
	}
	if len(h.notifier.deletions) != 1 {
		t.Fatalf("deletion notifications = %d, want 1", len(h.notifier.deletions))
	}
	// 来源重发一条本地已删除的消息：不会复活。
	revived := text(2, "a, edited after deletion")
	if out := h.importMessages(t, imp.ID, revived); out.Written != 0 {
		t.Fatalf("resending a deleted message wrote %d messages", out.Written)
	}
	if msg, ok := h.messages(t, channelID, 2)[2]; ok && !msg.Deleted {
		t.Fatalf("deleted message came back: %+v", msg)
	}

	if _, err := h.svc.DeleteMessages(ctx, imp.ID, nil, "cloner"); !errors.Is(err, domain.ErrChannelInvalid) {
		t.Fatalf("empty delete err = %v", err)
	}
	if out, err := h.svc.DeleteMessages(ctx, imp.ID, []int{500}, "cloner"); err != nil || len(out.Unknown) != 1 || len(out.Deleted) != 0 {
		t.Fatalf("unknown-only delete = %+v err %v", out, err)
	}
}

func TestSetMetaMirrorsTitleAboutAndPhotoWithoutServiceMessages(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	res := h.create(t, CreateRequest{Title: "Before"})
	before, err := h.store.GetChannelByID(ctx, res.Channel.ID)
	if err != nil {
		t.Fatalf("GetChannelByID: %v", err)
	}
	title, about := "  After  ", "new about"
	status, err := h.svc.SetMeta(ctx, res.Import.ID, Meta{Title: &title, About: &about, Photo: []byte("img")})
	if err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	if status.Channel.Title != "After" || status.Channel.About != "new about" || status.Channel.PhotoID == 0 {
		t.Fatalf("channel = %+v", status.Channel)
	}
	if status.Channel.TopMessageID != before.TopMessageID || status.Channel.Pts != before.Pts {
		t.Fatalf("meta change moved top %d->%d or pts %d->%d", before.TopMessageID, status.Channel.TopMessageID, before.Pts, status.Channel.Pts)
	}
	if h.files.avatars != 1 || len(h.notifier.changed) != 1 {
		t.Fatalf("avatars %d notifications %d, want 1 and 1", h.files.avatars, len(h.notifier.changed))
	}
	blank := " "
	if _, err := h.svc.SetMeta(ctx, res.Import.ID, Meta{Title: &blank}); !errors.Is(err, domain.ErrChannelTitleInvalid) {
		t.Fatalf("blank title err = %v, want ErrChannelTitleInvalid", err)
	}
}

func TestDetachedImportRejectsWrites(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	res := h.create(t, CreateRequest{SourceChannelID: 333})
	h.importMessages(t, res.Import.ID, text(2, "a"))
	status, err := h.svc.Detach(ctx, res.Import.ID)
	if err != nil || status.Import.State != domain.ChannelImportDetached {
		t.Fatalf("Detach = %+v err %v", status, err)
	}
	if _, err := h.svc.ImportMessages(ctx, res.Import.ID, []Message{text(3, "b")}); !errors.Is(err, domain.ErrChannelImportState) {
		t.Fatalf("import after detach err = %v", err)
	}
	if _, err := h.svc.PutMedia(ctx, res.Import.ID, MediaUpload{SourceKey: "k", Kind: domain.ChannelImportMediaPhoto, Body: strings.NewReader("x")}); !errors.Is(err, domain.ErrChannelImportState) {
		t.Fatalf("media after detach err = %v", err)
	}
	if _, err := h.svc.Finalize(ctx, res.Import.ID, FinalizeRequest{}); !errors.Is(err, domain.ErrChannelImportState) {
		t.Fatalf("finalize after detach err = %v", err)
	}
	if _, err := h.svc.DeleteMessages(ctx, res.Import.ID, []int{2}, "cloner"); !errors.Is(err, domain.ErrChannelImportState) {
		t.Fatalf("delete after detach err = %v", err)
	}
	title := "x"
	if _, err := h.svc.SetMeta(ctx, res.Import.ID, Meta{Title: &title}); !errors.Is(err, domain.ErrChannelImportState) {
		t.Fatalf("meta after detach err = %v", err)
	}
	// 频道本身是一个普通频道：频道主照常发帖。
	if _, err := h.store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: testOwner, ChannelID: res.Channel.ID, RandomID: 4, Message: "after detach"}); err != nil {
		t.Fatalf("SendChannelMessage after detach: %v", err)
	}
	// 同一个来源可以重新克隆。
	again := h.create(t, CreateRequest{SourceChannelID: 333, Title: "Again"})
	if again.Existing || again.Channel.ID == res.Channel.ID {
		t.Fatalf("re-clone after detach = %+v, want a new import", again)
	}
}

func TestInt64AcceptsNumbersAndStrings(t *testing.T) {
	for in, want := range map[string]int64{`13000000000000001`: 13000000000000001, `"13000000000000001"`: 13000000000000001, `""`: 0, `null`: 0, `0`: 0} {
		var v Int64
		if err := v.UnmarshalJSON([]byte(in)); err != nil || int64(v) != want {
			t.Fatalf("UnmarshalJSON(%s) = %d, %v; want %d", in, v, err, want)
		}
	}
	var v Int64
	if err := v.UnmarshalJSON([]byte(`"abc"`)); err == nil {
		t.Fatal("UnmarshalJSON accepted a non-numeric string")
	}
}
