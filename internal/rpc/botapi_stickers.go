package rpc

import (
	"context"
	"errors"
	"strings"

	"telesrv/internal/domain"
)

type botStickerFiles interface {
	BotStickerOwnedSet(context.Context, int64, int64, string) (domain.StickerSet, []domain.Document, error)
	BotStickerOwnedDocument(context.Context, int64, int64) (domain.StickerSet, domain.Document, error)
	BotStickerCreate(context.Context, int64, string, domain.BotAPICreateStickerSet) (domain.StickerSet, error)
	BotStickerFile(context.Context, domain.BotAPIFileInput, string, domain.StickerSetKind, bool) (domain.Document, error)
	BotStickerAdd(context.Context, int64, int64, string, domain.BotAPIInputSticker) (domain.StickerSet, error)
	BotStickerReplace(context.Context, int64, int64, string, int64, domain.BotAPIInputSticker) (domain.StickerSet, error)
	BotStickerSetEmoji(context.Context, int64, int64, []string) (domain.StickerSet, error)
	BotStickerSetKeywords(context.Context, int64, int64, []string) (domain.StickerSet, error)
	BotStickerSetMask(context.Context, int64, int64, *domain.StickerMaskPosition) (domain.StickerSet, error)
	BotStickerThumbnail(context.Context, int64, int64, string, string, domain.BotAPIFileInput, int64, bool) (domain.StickerSet, error)
	BotStickerMove(context.Context, int64, int64, int) (domain.StickerSet, error)
	BotStickerRemove(context.Context, int64, int64) (domain.StickerSet, error)
	BotStickerTitle(context.Context, int64, string, string) (domain.StickerSet, error)
	BotStickerDelete(context.Context, int64, string) (domain.StickerSet, error)
}

func (r *Router) botStickerService(ctx context.Context, botID int64) (botStickerFiles, error) {
	if _, err := r.BotAPISelf(ctx, botID); err != nil {
		return nil, err
	}
	svc, ok := r.deps.Files.(botStickerFiles)
	if !ok {
		return nil, errors.New("INTERNAL_SERVER_ERROR")
	}
	return svc, nil
}
func (r *Router) botStickerProxy(ctx context.Context, botID, userID int64) error {
	if userID <= 0 || r.deps.Users == nil {
		return errors.New("USER_ID_INVALID")
	}
	user, found, err := r.deps.Users.ByID(ctx, botID, userID)
	if err != nil {
		return err
	}
	if !found || user.Bot || user.Deleted {
		return errors.New("USER_ID_INVALID")
	}
	// Like normal bot sends, delegation requires an established private dialog or explicit permission.
	if r.deps.Dialogs != nil {
		list, e := r.deps.Dialogs.GetPeerDialogs(ctx, botID, []domain.Peer{{Type: domain.PeerTypeUser, ID: userID}})
		if e != nil {
			return e
		}
		for _, dialog := range list.Dialogs {
			if dialog.TopMessage > 0 {
				return nil
			}
		}
	}
	if r.deps.Bots != nil {
		allowed, e := r.deps.Bots.CanSendMessage(ctx, userID, botID)
		if e != nil {
			return e
		}
		if allowed {
			return nil
		}
	}
	return errors.New("USER_ID_INVALID")
}
func botStickerError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, domain.ErrStickerSetNotOwned) {
		return errors.New("STICKERSET_INVALID")
	}
	if errors.Is(err, domain.ErrStickerSetTypeInvalid) {
		return packTypeInvalidErr()
	}
	return stickerSetManagementErr(err)
}
func (r *Router) BotAPIGetStickerSet(ctx context.Context, botID int64, name string) (domain.StickerSet, []domain.Document, error) {
	if _, err := r.botStickerService(ctx, botID); err != nil {
		return domain.StickerSet{}, nil, err
	}
	set, docs, found, err := r.deps.Files.ResolveStickerSet(ctx, domain.StickerSetRef{Kind: domain.StickerSetRefByShortName, ShortName: name})
	if err != nil {
		return set, nil, err
	}
	if !found || set.Deleted {
		return set, nil, errors.New("STICKERSET_INVALID")
	}
	return set, docs, nil
}
func (r *Router) BotAPIGetCustomEmojiStickers(ctx context.Context, botID int64, ids []int64) ([]domain.Document, error) {
	if _, err := r.botStickerService(ctx, botID); err != nil {
		return nil, err
	}
	if len(ids) > 200 {
		return nil, errors.New("STICKER_FILE_INVALID")
	}
	docs, err := r.deps.Files.GetDocuments(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]domain.Document, len(docs))
	for _, doc := range docs {
		if doc.IsCustomEmoji() {
			byID[doc.ID] = doc
		}
	}
	out := make([]domain.Document, 0, len(ids))
	for _, id := range ids {
		if doc, ok := byID[id]; ok {
			out = append(out, doc)
		}
	}
	return out, nil
}
func (r *Router) BotAPIUploadStickerFile(ctx context.Context, botID, userID int64, file domain.BotAPIFileInput, format string) (domain.Document, error) {
	svc, err := r.botStickerService(ctx, botID)
	if err != nil {
		return domain.Document{}, err
	}
	if err = r.botStickerProxy(ctx, botID, userID); err != nil {
		return domain.Document{}, err
	}
	if len(file.Bytes) == 0 {
		return domain.Document{}, errors.New("STICKER_FILE_INVALID")
	}
	doc, err := svc.BotStickerFile(ctx, file, format, "", false)
	return doc, botStickerError(err)
}
func (r *Router) BotAPICreateNewStickerSet(ctx context.Context, botID int64, req domain.BotAPICreateStickerSet) error {
	svc, err := r.botStickerService(ctx, botID)
	if err != nil {
		return err
	}
	if err = r.botStickerProxy(ctx, botID, req.UserID); err != nil {
		return err
	}
	bot, err := r.BotAPISelf(ctx, botID)
	if err != nil {
		return err
	}
	set, err := svc.BotStickerCreate(ctx, botID, bot.Username, req)
	if err != nil {
		return stickerSetCreateErr(err)
	}
	r.notifyStickerSetMutated(ctx, req.UserID, set)
	return nil
}
func (r *Router) BotAPIAddStickerToSet(ctx context.Context, botID, userID int64, name string, input domain.BotAPIInputSticker) error {
	svc, err := r.botStickerService(ctx, botID)
	if err != nil {
		return err
	}
	if err = r.botStickerProxy(ctx, botID, userID); err != nil {
		return err
	}
	set, err := svc.BotStickerAdd(ctx, botID, userID, name, input)
	if err != nil {
		return botStickerError(err)
	}
	r.notifyStickerSetMutated(ctx, userID, set)
	return nil
}
func (r *Router) BotAPIReplaceStickerInSet(ctx context.Context, botID, userID int64, name string, oldID int64, input domain.BotAPIInputSticker) error {
	svc, err := r.botStickerService(ctx, botID)
	if err != nil {
		return err
	}
	if err = r.botStickerProxy(ctx, botID, userID); err != nil {
		return err
	}
	set, err := svc.BotStickerReplace(ctx, botID, userID, name, oldID, input)
	if err != nil {
		return botStickerError(err)
	}
	r.notifyStickerSetMutated(ctx, userID, set)
	return nil
}
func (r *Router) BotAPISetStickerPositionInSet(ctx context.Context, botID, id int64, position int) error {
	svc, err := r.botStickerService(ctx, botID)
	if err != nil {
		return err
	}
	set, err := svc.BotStickerMove(ctx, botID, id, position)
	if err != nil {
		return botStickerError(err)
	}
	r.notifyStickerSetMutated(ctx, set.CreatorUserID, set)
	return nil
}
func (r *Router) BotAPIDeleteStickerFromSet(ctx context.Context, botID, id int64) error {
	svc, err := r.botStickerService(ctx, botID)
	if err != nil {
		return err
	}
	set, err := svc.BotStickerRemove(ctx, botID, id)
	if err != nil {
		return botStickerError(err)
	}
	r.notifyStickerSetMutated(ctx, set.CreatorUserID, set)
	return nil
}
func (r *Router) BotAPISetStickerEmojiList(ctx context.Context, botID, id int64, emojis []string) error {
	svc, err := r.botStickerService(ctx, botID)
	if err != nil {
		return err
	}
	set, err := svc.BotStickerSetEmoji(ctx, botID, id, emojis)
	if err != nil {
		return botStickerError(err)
	}
	r.notifyStickerSetMutated(ctx, set.CreatorUserID, set)
	return nil
}
func (r *Router) BotAPISetStickerKeywords(ctx context.Context, botID, id int64, keywords []string) error {
	svc, err := r.botStickerService(ctx, botID)
	if err != nil {
		return err
	}
	set, err := svc.BotStickerSetKeywords(ctx, botID, id, keywords)
	if err != nil {
		return botStickerError(err)
	}
	r.notifyStickerSetMutated(ctx, set.CreatorUserID, set)
	return nil
}
func (r *Router) BotAPISetStickerMaskPosition(ctx context.Context, botID, id int64, p *domain.StickerMaskPosition) error {
	svc, err := r.botStickerService(ctx, botID)
	if err != nil {
		return err
	}
	set, err := svc.BotStickerSetMask(ctx, botID, id, p)
	if err != nil {
		return botStickerError(err)
	}
	r.notifyStickerSetMutated(ctx, set.CreatorUserID, set)
	return nil
}
func (r *Router) BotAPISetStickerSetTitle(ctx context.Context, botID int64, name, title string) error {
	svc, err := r.botStickerService(ctx, botID)
	if err != nil {
		return err
	}
	set, err := svc.BotStickerTitle(ctx, botID, name, title)
	if err != nil {
		return botStickerError(err)
	}
	r.notifyStickerSetMutated(ctx, set.CreatorUserID, set)
	return nil
}
func (r *Router) BotAPISetStickerSetThumbnail(ctx context.Context, botID, userID int64, name, format string, file domain.BotAPIFileInput) error {
	svc, err := r.botStickerService(ctx, botID)
	if err != nil {
		return err
	}
	if err = r.botStickerProxy(ctx, botID, userID); err != nil {
		return err
	}
	set, err := svc.BotStickerThumbnail(ctx, botID, userID, name, format, file, 0, false)
	if err != nil {
		return botStickerError(err)
	}
	r.notifyStickerSetMutated(ctx, userID, set)
	return nil
}
func (r *Router) BotAPISetCustomEmojiStickerSetThumbnail(ctx context.Context, botID int64, name string, emojiID int64) error {
	svc, err := r.botStickerService(ctx, botID)
	if err != nil {
		return err
	}
	set, err := svc.BotStickerThumbnail(ctx, botID, 0, name, "", domain.BotAPIFileInput{}, emojiID, true)
	if err != nil {
		return botStickerError(err)
	}
	r.notifyStickerSetMutated(ctx, set.CreatorUserID, set)
	return nil
}
func (r *Router) BotAPIDeleteStickerSet(ctx context.Context, botID int64, name string) error {
	svc, err := r.botStickerService(ctx, botID)
	if err != nil {
		return err
	}
	set, err := svc.BotStickerDelete(ctx, botID, strings.TrimSpace(name))
	if err != nil {
		return botStickerError(err)
	}
	r.invalidateStickerCatalog(set.Kind)
	r.pushStickerSetsUpdate(ctx, set.CreatorUserID, set.Kind)
	return nil
}
