package rpc

import (
	"context"
	"errors"
	"strings"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// accountRingtonesService 是已保存通知铃声的可选扩展，由 *app/account.Service 实现。
// 未接通时 getSavedRingtones 返回空列表，上传/保存返回 RINGTONE_INVALID。
type accountRingtonesService interface {
	SaveRingtone(ctx context.Context, userID int64, doc domain.Document, unsave bool) (bool, error)
	ListSavedRingtoneIDs(ctx context.Context, userID int64) ([]int64, error)
}

func (r *Router) accountRingtonesSvc() (accountRingtonesService, bool) {
	svc, ok := r.deps.Account.(accountRingtonesService)
	return svc, ok
}

// onAccountUploadRingtone 把上传的音频落成 Document 并直接加入已保存铃声：TDesktop
// 上传成功后自己把文档插到列表最前，不会再调 saveRingtone。时长在客户端预检，
// 这里只有文件名属性，服务端不探测时长。
func (r *Router) onAccountUploadRingtone(ctx context.Context, req *tg.AccountUploadRingtoneRequest) (tg.DocumentClass, error) {
	if req == nil || req.File == nil {
		return nil, ringtoneInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 {
		return nil, authKeyUnregisteredErr()
	}
	if !strings.HasPrefix(strings.ToLower(req.MimeType), "audio/") {
		return nil, tgerr400("RINGTONE_MIME_INVALID")
	}
	svc, ok := r.accountRingtonesSvc()
	if !ok || r.deps.Files == nil {
		return nil, ringtoneInvalidErr()
	}
	ref, ok := uploadedFileRef(userID, req.File)
	if !ok {
		return nil, ringtoneInvalidErr()
	}
	doc, err := r.deps.Files.CreateDocumentFromUpload(ctx, ref, domain.DocumentSpec{
		MimeType:   req.MimeType,
		Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrFilename, FileName: req.FileName}},
	})
	if err != nil {
		return nil, mediaUploadErr(err)
	}
	changed, err := svc.SaveRingtone(ctx, userID, doc, false)
	if err != nil {
		return nil, ringtoneErr(err)
	}
	if changed {
		r.pushSavedRingtonesChanged(ctx, userID)
	}
	return tgDocument(doc), nil
}

// onAccountSaveRingtone 把已有文档（如聊天里的音频/语音）加入或移出已保存铃声。
// 不做格式转换，所以总是返回 account.savedRingtone，不返回 savedRingtoneConverted。
func (r *Router) onAccountSaveRingtone(ctx context.Context, req *tg.AccountSaveRingtoneRequest) (tg.AccountSavedRingtoneClass, error) {
	if req == nil {
		return nil, ringtoneInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 {
		return nil, authKeyUnregisteredErr()
	}
	svc, ok := r.accountRingtonesSvc()
	if !ok || r.deps.Files == nil {
		return nil, ringtoneInvalidErr()
	}
	in, ok := req.ID.(*tg.InputDocument)
	if !ok || in.ID == 0 {
		return nil, ringtoneInvalidErr()
	}
	doc, found, err := r.deps.Files.GetDocument(ctx, in.ID)
	if err != nil {
		return nil, internalErr()
	}
	if !found || doc.AccessHash != in.AccessHash {
		return nil, ringtoneInvalidErr()
	}
	changed, err := svc.SaveRingtone(ctx, userID, doc, req.Unsave)
	if err != nil {
		return nil, ringtoneErr(err)
	}
	if changed {
		r.pushSavedRingtonesChanged(ctx, userID)
	}
	return &tg.AccountSavedRingtone{}, nil
}

// onAccountGetSavedRingtones 返回已保存铃声（最新在前）。已被删除的文档跳过；hash
// 按实际返回的文档 id 计算。
func (r *Router) onAccountGetSavedRingtones(ctx context.Context, hash int64) (tg.AccountSavedRingtonesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	empty := &tg.AccountSavedRingtones{Ringtones: []tg.DocumentClass{}}
	svc, ok := r.accountRingtonesSvc()
	if !ok || r.deps.Files == nil || userID == 0 {
		return empty, nil
	}
	ids, err := svc.ListSavedRingtoneIDs(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	if len(ids) == 0 {
		return empty, nil
	}
	docs, err := r.deps.Files.GetDocuments(ctx, ids)
	if err != nil {
		return nil, internalErr()
	}
	byID := make(map[int64]domain.Document, len(docs))
	for _, d := range docs {
		byID[d.ID] = d
	}
	present := make([]int64, 0, len(ids))
	out := make([]tg.DocumentClass, 0, len(ids))
	for _, id := range ids {
		d, ok := byID[id]
		if !ok {
			continue
		}
		present = append(present, id)
		out = append(out, tgDocument(d))
	}
	listHash := int64(tdesktopCountHash(present))
	if hash != 0 && hash == listHash {
		return &tg.AccountSavedRingtonesNotModified{}, nil
	}
	return &tg.AccountSavedRingtones{Hash: listHash, Ringtones: out}, nil
}

// pushSavedRingtonesChanged 通知本人其它设备重新拉取铃声列表。
func (r *Router) pushSavedRingtonesChanged(ctx context.Context, userID int64) {
	r.pushUserUpdates(ctx, userID, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateSavedRingtones{}},
		Users:   []tg.UserClass{},
		Chats:   []tg.ChatClass{},
		Date:    int(r.clock.Now().Unix()),
	})
}

func ringtoneInvalidErr() error { return tgerr400("RINGTONE_INVALID") }

// ringtoneErr 映射为 TDesktop ringtones_box 认得的错误码。
func ringtoneErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrRingtoneMimeInvalid):
		return tgerr400("RINGTONE_MIME_INVALID")
	case errors.Is(err, domain.ErrRingtoneSizeTooBig):
		return tgerr400("RINGTONE_SIZE_TOO_BIG")
	case errors.Is(err, domain.ErrRingtoneDurationTooLong):
		return tgerr400("RINGTONE_DURATION_TOO_LONG")
	case errors.Is(err, domain.ErrRingtoneInvalid):
		return ringtoneInvalidErr()
	default:
		return internalErr()
	}
}
