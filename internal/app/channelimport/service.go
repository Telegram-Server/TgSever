// Package channelimport 把外部来源（官方 Telegram 的频道）克隆进本地广播频道，并在之后持续镜像。
//
// 克隆程序在仓库之外：它登录来源、拉取历史和媒体，再经 admin API 调用这里。这个包负责
// 建目标频道、把来源文件存成本地媒体、把来源消息换成领域类型交给 store.ChannelImportStore
// 写入，并在镜像阶段把新帖、编辑、删除实时下发给成员。
//
// 一个任务的三个阶段见 domain.ChannelImportState。回填阶段频道没有公开用户名，搜索不到，
// 也只有频道主自己在里面；Finalize 设置用户名之后它是一个普通的公开频道。频道主在本地发帖
// 不会和来源撞 id：来源之后的新帖拿到本地分配的 id，经映射表与来源 id 对应。
package channelimport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// MaxPhotoBytes 是一张导入照片的字节上限；照片要整个读进内存解码生成各个尺寸。
const MaxPhotoBytes = 20 << 20

var (
	// ErrOwnerInvalid 指定的频道主不能拥有频道：不存在、是机器人或系统账号、已删除。
	ErrOwnerInvalid = errors.New("channel import: owner cannot own a channel")
	// ErrMediaInvalid 上传的媒体不合法：缺来源标识、类型未知、内容为空或超过上限。
	ErrMediaInvalid = errors.New("channel import: invalid media upload")
)

// Users 按 id 读取账号。
type Users interface {
	ByID(ctx context.Context, id int64) (domain.User, bool, error)
}

// Channels 是建频道与后台路径的频道修改（生产里是 app/channels.Service）。
type Channels interface {
	CreateChannel(ctx context.Context, userID int64, req domain.CreateChannelRequest) (domain.CreateChannelResult, error)
	GetChannelByID(ctx context.Context, channelID int64) (domain.Channel, error)
	AdminSetUsername(ctx context.Context, channelID int64, username string) (domain.Channel, error)
	AdminSetPhoto(ctx context.Context, channelID int64, photo domain.Photo) (domain.Channel, error)
	DeleteMessages(ctx context.Context, userID int64, req domain.DeleteChannelMessagesRequest) (domain.DeleteChannelMessagesResult, error)
}

// Files 把来源文件存成本地媒体（生产里是 app/files.Service）。
type Files interface {
	CreatePhotoFromBytesAt(ctx context.Context, data []byte, date int) (domain.Photo, error)
	CreateDocumentFromReaderWithThumb(ctx context.Context, r io.Reader, spec domain.DocumentSpec) (domain.Document, error)
	CreateAvatarFromBytes(ctx context.Context, data []byte) (domain.Photo, error)
	GetPhoto(ctx context.Context, id int64) (domain.Photo, bool, error)
	GetDocument(ctx context.Context, id int64) (domain.Document, bool, error)
}

// Notifier 把已经落库的变化实时下发给在线成员（生产里是 rpc.Router）。全部是尽力而为：
// 持久的事件和 pts 才是离线补拉的依据。
type Notifier interface {
	// NotifyChannelImportCreated 把新建的频道告知频道主的在线会话。
	NotifyChannelImportCreated(ctx context.Context, res domain.CreateChannelResult)
	// NotifyChannelImportEvents 下发镜像阶段的新帖与编辑事件；接收者由下发方按在线成员决定。
	NotifyChannelImportEvents(ctx context.Context, channel domain.Channel, senderUserID int64, events []domain.ChannelUpdateEvent)
	// NotifyChannelImportRefreshed 让在线成员重新拉取频道（回填没有逐条事件）。
	NotifyChannelImportRefreshed(ctx context.Context, channel domain.Channel)
	NotifyChannelChanged(ctx context.Context, channel domain.Channel) error
	NotifyModerationChannelDeletion(ctx context.Context, res domain.DeleteChannelMessagesResult)
}

// Service 是频道导入的业务入口。
type Service struct {
	imports  store.ChannelImportStore
	channels Channels
	files    Files
	users    Users
	notifier Notifier
	now      func() time.Time
}

// NewService 创建频道导入服务。notifier 可以为 nil（不做实时下发）。
func NewService(imports store.ChannelImportStore, channels Channels, files Files, users Users, notifier Notifier) *Service {
	return &Service{imports: imports, channels: channels, files: files, users: users, notifier: notifier, now: time.Now}
}

// Status 是一个任务及其目标频道的当前状态。
type Status struct {
	Import  domain.ChannelImport
	Channel domain.Channel
	Stats   domain.ChannelImportStats
}

// CreateRequest 建一个导入任务。TargetChannelID 非零时导入到这个已有的空广播频道；否则以
// OwnerUserID 的身份新建一个广播频道。
type CreateRequest struct {
	SourceChannelID int64
	SourceUsername  string

	TargetChannelID int64

	OwnerUserID int64
	Title       string
	About       string

	// CreatedDate 是来源频道的创建时间；CreateTitle 是来源“频道已创建”服务消息里的标题
	// （创建时的标题，往往与当前标题不同）。CreateMessageMissing 表示来源这条服务消息已
	// 不存在，本地这条留成空号。
	CreatedDate          int
	CreateTitle          string
	CreateMessageMissing bool

	// SenderUserID 是导入帖子挂在其名下的账号；0 时取频道主。
	SenderUserID int64
}

// CreateResult 是 Create 的结果。Existing 表示来源频道已有在用的任务，返回的是它。
type CreateResult struct {
	Status
	Existing bool
}

// Create 建一个导入任务。同一个来源频道重复调用返回已有的任务，不会克隆出第二个频道。
func (s *Service) Create(ctx context.Context, req CreateRequest) (CreateResult, error) {
	if s == nil || s.imports == nil || s.channels == nil {
		return CreateResult{}, fmt.Errorf("channel import dependencies are not configured")
	}
	if req.SourceChannelID != 0 {
		existing, found, err := s.imports.FindChannelImportBySource(ctx, req.SourceChannelID)
		if err != nil {
			return CreateResult{}, err
		}
		if found {
			status, err := s.status(ctx, existing)
			return CreateResult{Status: status, Existing: true}, err
		}
	}
	req.Title = strings.TrimSpace(req.Title)
	req.CreateTitle = strings.TrimSpace(req.CreateTitle)
	if req.CreatedDate < 0 {
		return CreateResult{}, domain.ErrChannelInvalid
	}

	var channel domain.Channel
	var created *domain.CreateChannelResult
	if req.TargetChannelID != 0 {
		existing, err := s.channels.GetChannelByID(ctx, req.TargetChannelID)
		if err != nil {
			return CreateResult{}, err
		}
		channel = existing
	} else {
		if err := s.validateOwner(ctx, req.OwnerUserID); err != nil {
			return CreateResult{}, err
		}
		if !domain.ValidChannelTitle(req.Title) {
			return CreateResult{}, domain.ErrChannelTitleInvalid
		}
	}
	sender := req.SenderUserID
	if sender == 0 {
		sender = req.OwnerUserID
		if req.TargetChannelID != 0 {
			sender = channel.CreatorUserID
		}
	}
	if sender != req.OwnerUserID || req.TargetChannelID != 0 {
		if err := s.validateOwner(ctx, sender); err != nil {
			return CreateResult{}, err
		}
	}
	if req.TargetChannelID == 0 {
		date := req.CreatedDate
		if date == 0 {
			date = int(s.now().Unix())
		}
		res, err := s.channels.CreateChannel(ctx, req.OwnerUserID, domain.CreateChannelRequest{
			CreatorUserID: req.OwnerUserID,
			Title:         req.Title,
			About:         req.About,
			Broadcast:     true,
			Date:          date,
		})
		if err != nil {
			return CreateResult{}, err
		}
		channel = res.Channel
		created = &res
	}

	imp, err := s.imports.CreateChannelImport(ctx, domain.ChannelImport{
		ChannelID:       channel.ID,
		SourceChannelID: req.SourceChannelID,
		SourceUsername:  strings.TrimPrefix(strings.TrimSpace(req.SourceUsername), "@"),
		SenderUserID:    sender,
	})
	if err != nil {
		return CreateResult{}, err
	}

	// “频道已创建”这条服务消息对应来源的 id 1：新建的频道已经带着来源的创建日期，只在
	// 创建时的标题不同或来源这条已不存在时才需要改写；已有的频道要把日期也改成来源的。
	createTitle := req.CreateTitle
	if createTitle == "" {
		createTitle = channel.Title
	}
	rewrite := req.CreateMessageMissing || createTitle != channel.Title
	if req.TargetChannelID != 0 && req.CreatedDate > 0 {
		rewrite = true
	}
	if rewrite {
		date := req.CreatedDate
		if date == 0 {
			date = channel.Date
		}
		if err := s.imports.RewriteChannelCreateMessage(ctx, domain.RewriteChannelCreateRequest{
			ChannelID: channel.ID,
			Date:      date,
			Title:     createTitle,
			Drop:      req.CreateMessageMissing,
		}); err != nil {
			return CreateResult{}, err
		}
		channel.Date = date
	}
	if created != nil && s.notifier != nil {
		if created.Message.Action != nil {
			action := *created.Message.Action
			action.Title = createTitle
			created.Message.Action = &action
		}
		created.Channel = channel
		s.notifier.NotifyChannelImportCreated(ctx, *created)
	}
	return CreateResult{Status: Status{Import: imp, Channel: channel}}, nil
}

// validateOwner 拒绝不能拥有频道或发帖的账号：不存在、机器人、系统账号、已删除。
func (s *Service) validateOwner(ctx context.Context, userID int64) error {
	if userID <= 0 || s.users == nil {
		return ErrOwnerInvalid
	}
	user, found, err := s.users.ByID(ctx, userID)
	if err != nil {
		return err
	}
	if !found || user.Bot || user.Deleted || domain.IsSystemUserID(user.ID) {
		return ErrOwnerInvalid
	}
	return nil
}

// Get 返回任务的当前状态。
func (s *Service) Get(ctx context.Context, importID int64) (Status, error) {
	imp, err := s.imports.GetChannelImport(ctx, importID)
	if err != nil {
		return Status{}, err
	}
	return s.status(ctx, imp)
}

func (s *Service) status(ctx context.Context, imp domain.ChannelImport) (Status, error) {
	channel, err := s.channels.GetChannelByID(ctx, imp.ChannelID)
	if err != nil {
		return Status{}, err
	}
	stats, err := s.imports.ChannelImportStats(ctx, imp.ID)
	if err != nil {
		return Status{}, err
	}
	return Status{Import: imp, Channel: channel, Stats: stats}, nil
}

// writable 读出任务并拒绝已经 detached 的。
func (s *Service) writable(ctx context.Context, importID int64) (domain.ChannelImport, error) {
	imp, err := s.imports.GetChannelImport(ctx, importID)
	if err != nil {
		return domain.ChannelImport{}, err
	}
	if imp.State == domain.ChannelImportDetached {
		return domain.ChannelImport{}, domain.ErrChannelImportState
	}
	return imp, nil
}

// MediaUpload 是一个来源文件。SourceKey 是它在来源的稳定标识（例如 file_unique_id），
// 消息用它引用媒体；同一个 SourceKey 第二次上传直接返回第一次的结果，不读 Body。
type MediaUpload struct {
	SourceKey string
	Kind      domain.ChannelImportMediaKind
	// Date 是来源文件的日期；0 取当前时间。
	Date       int
	MimeType   string
	Attributes []domain.DocumentAttribute
	Body       io.Reader
}

// MediaResult 是 PutMedia 的结果。
type MediaResult struct {
	SourceKey string
	Media     domain.ChannelImportMedia
	// Existing 表示这个来源文件之前已经上传过。
	Existing bool
}

// PutMedia 把一个来源文件存成本地照片或文档，并记下 SourceKey 到它的映射。
func (s *Service) PutMedia(ctx context.Context, importID int64, upload MediaUpload) (MediaResult, error) {
	if s == nil || s.files == nil {
		return MediaResult{}, fmt.Errorf("channel import file dependency is not configured")
	}
	upload.SourceKey = strings.TrimSpace(upload.SourceKey)
	if upload.SourceKey == "" || len(upload.SourceKey) > 256 {
		return MediaResult{}, ErrMediaInvalid
	}
	if upload.Kind != domain.ChannelImportMediaPhoto && upload.Kind != domain.ChannelImportMediaDocument {
		return MediaResult{}, ErrMediaInvalid
	}
	imp, err := s.writable(ctx, importID)
	if err != nil {
		return MediaResult{}, err
	}
	if existing, found, err := s.imports.GetChannelImportMedia(ctx, imp.ID, upload.SourceKey); err != nil {
		return MediaResult{}, err
	} else if found {
		return MediaResult{SourceKey: upload.SourceKey, Media: existing, Existing: true}, nil
	}
	if upload.Body == nil {
		return MediaResult{}, ErrMediaInvalid
	}
	var media domain.ChannelImportMedia
	switch upload.Kind {
	case domain.ChannelImportMediaPhoto:
		data, err := io.ReadAll(io.LimitReader(upload.Body, MaxPhotoBytes+1))
		if err != nil {
			return MediaResult{}, fmt.Errorf("read imported photo: %w", err)
		}
		if len(data) == 0 || len(data) > MaxPhotoBytes {
			return MediaResult{}, ErrMediaInvalid
		}
		photo, err := s.files.CreatePhotoFromBytesAt(ctx, data, upload.Date)
		if err != nil {
			return MediaResult{}, err
		}
		media = domain.ChannelImportMedia{Kind: upload.Kind, MediaID: photo.ID}
	case domain.ChannelImportMediaDocument:
		doc, err := s.files.CreateDocumentFromReaderWithThumb(ctx, upload.Body, domain.DocumentSpec{
			MimeType:   upload.MimeType,
			Attributes: importedDocumentAttributes(upload.Attributes),
			Date:       upload.Date,
		})
		if err != nil {
			return MediaResult{}, err
		}
		media = domain.ChannelImportMedia{Kind: upload.Kind, MediaID: doc.ID}
	}
	stored, err := s.imports.PutChannelImportMedia(ctx, imp.ID, upload.SourceKey, media)
	if err != nil {
		return MediaResult{}, err
	}
	// 两个并发上传同一个文件时，后到的一方拿回先写入的映射。
	return MediaResult{SourceKey: upload.SourceKey, Media: stored, Existing: stored.MediaID != media.MediaID}, nil
}

// importedDocumentAttributes 去掉指向来源服务器对象的属性：贴纸集和自定义表情在本地不存在。
func importedDocumentAttributes(attrs []domain.DocumentAttribute) []domain.DocumentAttribute {
	out := make([]domain.DocumentAttribute, 0, len(attrs))
	for _, attr := range attrs {
		switch attr.Kind {
		case domain.DocAttrImageSize, domain.DocAttrAnimated, domain.DocAttrVideo, domain.DocAttrAudio, domain.DocAttrFilename:
			out = append(out, attr)
		case domain.DocAttrSticker:
			attr.StickerSetID, attr.StickerSetAccessHash = 0, 0
			out = append(out, attr)
		}
	}
	return out
}

// BatchResult 是一批消息的写入结果。
type BatchResult struct {
	Import    domain.ChannelImport
	Written   int
	Edited    int
	Unchanged int
	// LocalIDs 是来源消息 id → 本地消息 id。
	LocalIDs map[int]int
	// Skipped 是没有写入的消息及原因（不支持的服务消息类型、没有可导入的内容、引用了
	// 没上传过的媒体）；它们在本地留成空号。
	Skipped []Skipped
}

// Skipped 是一条被跳过的来源消息。
type Skipped struct {
	ID     int    `json:"id"`
	Reason string `json:"reason"`
}

// ImportMessages 写入一批来源消息。来源 id 必须严格递增；回填阶段不下发，镜像阶段实时下发。
func (s *Service) ImportMessages(ctx context.Context, importID int64, messages []Message) (BatchResult, error) {
	if len(messages) == 0 || len(messages) > domain.MaxChannelImportBatch {
		return BatchResult{}, domain.ErrChannelInvalid
	}
	imp, err := s.writable(ctx, importID)
	if err != nil {
		return BatchResult{}, err
	}
	out := BatchResult{Import: imp, LocalIDs: map[int]int{}}
	converter := newMessageConverter(s, imp)
	batch := make([]domain.ImportedChannelMessage, 0, len(messages))
	previous := 0
	for _, msg := range messages {
		// 顺序在跳过之前检查：被跳过的消息同样占着它的位置。
		if msg.ID <= previous {
			return BatchResult{}, domain.ErrChannelImportOrder
		}
		previous = msg.ID
		converted, reason, err := converter.convert(ctx, msg)
		if err != nil {
			return BatchResult{}, err
		}
		if reason != "" {
			out.Skipped = append(out.Skipped, Skipped{ID: msg.ID, Reason: reason})
			continue
		}
		batch = append(batch, converted)
	}
	if len(batch) == 0 {
		return out, nil
	}
	res, err := s.imports.ImportChannelMessages(ctx, domain.ImportChannelMessagesRequest{
		ImportID: imp.ID,
		Messages: batch,
		Now:      int(s.now().Unix()),
	})
	if err != nil {
		return BatchResult{}, err
	}
	out.Import = res.Import
	out.Written, out.Edited, out.Unchanged = res.Written, res.Edited, res.Unchanged
	out.LocalIDs = res.LocalIDs
	if len(res.Events) > 0 && s.notifier != nil {
		s.notifier.NotifyChannelImportEvents(ctx, res.Channel, res.Import.SenderUserID, res.Events)
	}
	return out, nil
}

// FinalizeRequest 结束回填。Username 非空时设为频道的公开用户名；ReserveThrough 非零时
// 之后本地分配的消息 id 都大于它（来源末尾被删掉的号段不被本地帖子占用）。
type FinalizeRequest struct {
	Username       string
	ReserveThrough int
}

// Finalize 结束回填，任务进入镜像阶段：现有成员的已读位置推到最新一条（回填的历史不算
// 未读），设置公开用户名，并让在线成员重新拉取频道。对已经在镜像的任务重复调用是空操作。
func (s *Service) Finalize(ctx context.Context, importID int64, req FinalizeRequest) (Status, error) {
	imp, err := s.writable(ctx, importID)
	if err != nil {
		return Status{}, err
	}
	if imp.State == domain.ChannelImportMirroring {
		return s.status(ctx, imp)
	}
	if err := s.imports.MarkChannelImportRead(ctx, imp.ChannelID); err != nil {
		return Status{}, err
	}
	if req.ReserveThrough > 0 {
		if err := s.imports.ReserveChannelMessageIDs(ctx, imp.ChannelID, req.ReserveThrough); err != nil {
			return Status{}, err
		}
	}
	username := strings.TrimPrefix(strings.TrimSpace(req.Username), "@")
	if username != "" {
		// 先设用户名再改状态：用户名被占用时任务停在 importing，换一个名字重试即可。
		if _, err := s.channels.AdminSetUsername(ctx, imp.ChannelID, username); err != nil {
			return Status{}, err
		}
	}
	imp, err = s.imports.SetChannelImportState(ctx, imp.ID, domain.ChannelImportMirroring)
	if err != nil {
		return Status{}, err
	}
	status, err := s.status(ctx, imp)
	if err != nil {
		return Status{}, err
	}
	if s.notifier != nil {
		if username != "" {
			_ = s.notifier.NotifyChannelChanged(ctx, status.Channel)
		}
		s.notifier.NotifyChannelImportRefreshed(ctx, status.Channel)
	}
	return status, nil
}

// DeleteResult 是 DeleteMessages 的结果。
type DeleteResult struct {
	// Deleted 是实际删除的本地消息 id。
	Deleted []int
	// Unknown 是没有导入过的来源消息 id。
	Unknown []int
}

// DeleteMessages 删除来源已经删除的消息。actor 记入删除记录。
func (s *Service) DeleteMessages(ctx context.Context, importID int64, sourceIDs []int, actor string) (DeleteResult, error) {
	if len(sourceIDs) == 0 || len(sourceIDs) > domain.MaxDeleteMessageIDs {
		return DeleteResult{}, domain.ErrChannelInvalid
	}
	imp, err := s.writable(ctx, importID)
	if err != nil {
		return DeleteResult{}, err
	}
	mapped, err := s.imports.ResolveChannelImportMessageIDs(ctx, imp.ID, sourceIDs)
	if err != nil {
		return DeleteResult{}, err
	}
	var out DeleteResult
	local := make([]int, 0, len(mapped))
	for _, id := range sourceIDs {
		localID, ok := mapped[id]
		if !ok {
			out.Unknown = append(out.Unknown, id)
			continue
		}
		local = append(local, localID)
	}
	if len(local) == 0 {
		return out, nil
	}
	channel, err := s.channels.GetChannelByID(ctx, imp.ChannelID)
	if err != nil {
		return DeleteResult{}, err
	}
	// 以频道主的身份删除（频道主对帖子总有删除权），删除记录归属到调用方。
	res, err := s.channels.DeleteMessages(ctx, channel.CreatorUserID, domain.DeleteChannelMessagesRequest{
		UserID:    channel.CreatorUserID,
		ChannelID: imp.ChannelID,
		IDs:       local,
		Date:      int(s.now().Unix()),
		Audit:     domain.OperatorDeletionAudit(actor, "", "channel import: deleted at source"),
	})
	if err != nil {
		return DeleteResult{}, err
	}
	out.Deleted = res.DeletedIDs
	if s.notifier != nil {
		s.notifier.NotifyModerationChannelDeletion(ctx, res)
	}
	return out, nil
}

// Meta 是来源频道资料的变化；nil 或空表示不改。
type Meta struct {
	Title *string
	About *string
	// Photo 是新头像的图片字节。
	Photo []byte
}

// SetMeta 镜像来源的标题、简介和头像：不写服务消息（来源自己的服务消息会作为历史导入）。
func (s *Service) SetMeta(ctx context.Context, importID int64, meta Meta) (Status, error) {
	imp, err := s.writable(ctx, importID)
	if err != nil {
		return Status{}, err
	}
	if meta.Title != nil {
		title := strings.TrimSpace(*meta.Title)
		meta.Title = &title
	}
	if meta.Title != nil || meta.About != nil {
		if _, err := s.imports.SetChannelImportMeta(ctx, imp.ChannelID, meta.Title, meta.About); err != nil {
			return Status{}, err
		}
	}
	if len(meta.Photo) > 0 {
		if len(meta.Photo) > MaxPhotoBytes || s.files == nil {
			return Status{}, ErrMediaInvalid
		}
		photo, err := s.files.CreateAvatarFromBytes(ctx, meta.Photo)
		if err != nil {
			return Status{}, err
		}
		if _, err := s.channels.AdminSetPhoto(ctx, imp.ChannelID, photo); err != nil {
			return Status{}, err
		}
	}
	status, err := s.status(ctx, imp)
	if err != nil {
		return Status{}, err
	}
	if s.notifier != nil {
		_ = s.notifier.NotifyChannelChanged(ctx, status.Channel)
	}
	return status, nil
}

// Detach 结束镜像：频道成为一个普通频道，任务不再接受写入。映射表保留。
func (s *Service) Detach(ctx context.Context, importID int64) (Status, error) {
	imp, err := s.imports.SetChannelImportState(ctx, importID, domain.ChannelImportDetached)
	if err != nil {
		return Status{}, err
	}
	return s.status(ctx, imp)
}
