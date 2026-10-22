package files

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"telesrv/internal/domain"
)

func (s *Service) BotStickerOwnedSet(ctx context.Context, botID, ownerID int64, name string) (domain.StickerSet, []domain.Document, error) {
	set, docs, found, err := s.ResolveStickerSet(ctx, domain.StickerSetRef{Kind: domain.StickerSetRefByShortName, ShortName: name})
	if err != nil {
		return set, nil, err
	}
	if !found || set.Deleted {
		return domain.StickerSet{}, nil, domain.ErrStickerSetInvalid
	}
	if botID <= 0 || set.CreatorBotID != botID || ownerID != 0 && set.CreatorUserID != ownerID {
		return domain.StickerSet{}, nil, domain.ErrStickerSetNotOwned
	}
	return set, docs, nil
}
func (s *Service) BotStickerOwnedDocument(ctx context.Context, botID, documentID int64) (domain.StickerSet, domain.Document, error) {
	docs, err := s.media.GetDocuments(ctx, []int64{documentID})
	if err != nil {
		return domain.StickerSet{}, domain.Document{}, err
	}
	if len(docs) != 1 {
		return domain.StickerSet{}, domain.Document{}, domain.ErrStickerSetFileInvalid
	}
	id, _, ok := docs[0].StickerSetRef()
	if !ok {
		return domain.StickerSet{}, domain.Document{}, domain.ErrStickerSetFileInvalid
	}
	set, found, err := s.media.GetStickerSetByID(ctx, id)
	if err != nil {
		return set, domain.Document{}, err
	}
	if !found || set.Deleted || !containsInt64(set.DocumentIDs, documentID) {
		return set, domain.Document{}, domain.ErrStickerSetInvalid
	}
	if botID <= 0 || set.CreatorBotID != botID {
		return set, domain.Document{}, domain.ErrStickerSetNotOwned
	}
	return set, docs[0], nil
}

// ponytail: Bot API mutations serialize on the monolith's files.Service to avoid
// lost read-modify-write updates. Use per-set locks if creator traffic warrants it.
func (s *Service) BotStickerCreate(ctx context.Context, botID int64, username string, req domain.BotAPICreateStickerSet) (domain.StickerSet, error) {
	s.botStickerMu.Lock()
	defer s.botStickerMu.Unlock()
	name := normalizeStickerSetShortName(req.Name)
	if botID <= 0 || req.UserID <= 0 {
		return domain.StickerSet{}, domain.ErrStickerSetCreatorInvalid
	}
	if username == "" || !strings.HasSuffix(name, "_by_"+strings.ToLower(username)) {
		return domain.StickerSet{}, domain.ErrStickerSetShortNameInvalid
	}
	if err := validateCreatedStickerSetName(name, botID); err != nil {
		return domain.StickerSet{}, err
	}
	if req.Kind != domain.StickerSetKindStickers && req.Kind != domain.StickerSetKindEmoji && req.Kind != domain.StickerSetKindMasks || req.NeedsRepainting && req.Kind != domain.StickerSetKindEmoji {
		return domain.StickerSet{}, domain.ErrStickerSetTypeInvalid
	}
	if len(req.Stickers) < 1 || len(req.Stickers) > 50 {
		return domain.StickerSet{}, domain.ErrStickerSetTooMuch
	}
	if err := validateStickerSetTitle(req.Title); err != nil {
		return domain.StickerSet{}, err
	}
	available, err := s.media.StickerSetShortNameAvailable(ctx, name)
	if err != nil {
		return domain.StickerSet{}, err
	}
	if !available {
		return domain.StickerSet{}, domain.ErrStickerSetShortNameOccupied
	}
	for _, input := range req.Stickers {
		if err := validateBotStickerMetadata(input, req.Kind); err != nil {
			return domain.StickerSet{}, err
		}
	}
	items := make([]domain.StickerSetItemInput, 0, len(req.Stickers))
	for _, input := range req.Stickers {
		item, err := s.botStickerItem(ctx, input, req.Kind)
		if err != nil {
			return domain.StickerSet{}, err
		}
		items = append(items, item)
	}
	set, _, err := s.CreateStickerSet(ctx, domain.CreateStickerSetRequest{CreatorUserID: req.UserID, CreatorBotID: botID, ShortName: name, Title: req.Title, Kind: req.Kind, TextColor: req.NeedsRepainting, Items: items})
	return set, err
}
func (s *Service) botStickerItem(ctx context.Context, input domain.BotAPIInputSticker, kind domain.StickerSetKind) (domain.StickerSetItemInput, error) {
	if err := validateBotStickerMetadata(input, kind); err != nil {
		return domain.StickerSetItemInput{}, err
	}
	doc, err := s.BotStickerFile(ctx, input.File, input.Format, kind, false)
	if err != nil {
		return domain.StickerSetItemInput{}, err
	}
	return domain.StickerSetItemInput{DocumentID: doc.ID, DocumentAccessHash: doc.AccessHash, Emoji: input.EmojiList[0], EmojiList: input.EmojiList, KeywordsList: append([]string{}, input.Keywords...), MaskPosition: input.MaskPosition}, nil
}
func (s *Service) BotStickerAdd(ctx context.Context, botID, ownerID int64, name string, input domain.BotAPIInputSticker) (domain.StickerSet, error) {
	s.botStickerMu.Lock()
	defer s.botStickerMu.Unlock()
	set, _, err := s.BotStickerOwnedSet(ctx, botID, ownerID, name)
	if err != nil {
		return set, err
	}
	item, err := s.botStickerItem(ctx, input, set.Kind)
	if err != nil {
		return set, err
	}
	duplicate, err := s.botStickerDuplicate(ctx, set, item.DocumentID)
	if err != nil {
		return set, err
	}
	if duplicate {
		return set, nil
	}
	set, _, err = s.AddStickerToSet(ctx, set.CreatorUserID, domain.StickerSetRef{Kind: domain.StickerSetRefByShortName, ShortName: name}, item)
	return set, err
}
func (s *Service) BotStickerReplace(ctx context.Context, botID, ownerID int64, name string, oldID int64, input domain.BotAPIInputSticker) (domain.StickerSet, error) {
	s.botStickerMu.Lock()
	defer s.botStickerMu.Unlock()
	set, docs, err := s.BotStickerOwnedSet(ctx, botID, ownerID, name)
	if err != nil {
		return set, err
	}
	index := indexInt64(set.DocumentIDs, oldID)
	if index < 0 {
		return set, domain.ErrStickerSetFileInvalid
	}
	item, err := s.botStickerItem(ctx, input, set.Kind)
	if err != nil {
		return set, err
	}
	duplicate, err := s.botStickerDuplicate(ctx, set, item.DocumentID)
	if err != nil {
		return set, err
	}
	if duplicate {
		return set, nil
	}
	doc, err := s.loadStickerMaterialDocument(ctx, item.DocumentID, item.DocumentAccessHash)
	if err != nil {
		return set, err
	}
	doc, err = s.materialDocumentForStickerSet(ctx, doc, set.ID)
	if err != nil {
		return set, err
	}
	doc, err = s.prepareStickerSetDocument(ctx, doc, set, item.Emoji)
	if err != nil {
		return set, err
	}
	doc = applyStickerInputMetadata(doc, item)
	var old domain.Document
	for _, d := range docs {
		if d.ID == oldID {
			old = detachStickerSetFromDocument(d)
		}
	}
	set.DocumentIDs = append([]int64(nil), set.DocumentIDs...)
	set.DocumentIDs[index] = doc.ID
	set.Packs = removeDocumentFromStickerPacks(set.Packs, oldID)
	set.Keywords = removeStickerKeywords(set.Keywords, oldID)
	set.Packs, set.Keywords = stickerInputIndexes(set.Packs, set.Keywords, doc.ID, item)
	docs = append(removeDocumentByID(docs, oldID), doc)
	if set.ThumbDocumentID == oldID {
		setStickerSetThumbFromDocument(&set, doc)
	}
	set.Hash = stickerSetHash(set)
	set, _, err = s.persistStickerSetMutation(ctx, set, docs, []domain.Document{old, doc})
	return set, err
}
func (s *Service) BotStickerSetEmoji(ctx context.Context, botID, id int64, emojis []string) (domain.StickerSet, error) {
	s.botStickerMu.Lock()
	defer s.botStickerMu.Unlock()
	set, doc, err := s.BotStickerOwnedDocument(ctx, botID, id)
	if err != nil {
		return set, err
	}
	if set.Kind == domain.StickerSetKindMasks {
		return set, domain.ErrStickerSetTypeInvalid
	}
	if err := validateBotStickerMetadata(domain.BotAPIInputSticker{EmojiList: emojis}, set.Kind); err != nil {
		return set, err
	}
	_, docs, _, err := s.ResolveStickerSet(ctx, domain.StickerSetRef{Kind: domain.StickerSetRefByID, ID: set.ID})
	if err != nil {
		return set, err
	}
	set.Packs = removeDocumentFromStickerPacks(set.Packs, id)
	for _, e := range emojis {
		set.Packs = addDocumentToStickerPacks(set.Packs, e, id)
	}
	doc = attachStickerSetToDocument(doc, set, emojis[0])
	docs = append(removeDocumentByID(docs, id), doc)
	set.Hash = stickerSetHash(set)
	set, _, err = s.persistStickerSetMutation(ctx, set, docs, []domain.Document{doc})
	return set, err
}
func (s *Service) BotStickerSetKeywords(ctx context.Context, botID, id int64, keywords []string) (domain.StickerSet, error) {
	s.botStickerMu.Lock()
	defer s.botStickerMu.Unlock()
	set, _, err := s.BotStickerOwnedDocument(ctx, botID, id)
	if err != nil {
		return set, err
	}
	if set.Kind == domain.StickerSetKindMasks {
		return set, domain.ErrStickerSetTypeInvalid
	}
	if err := validateBotStickerKeywords(keywords, set.Kind); err != nil {
		return set, err
	}
	_, docs, _, err := s.ResolveStickerSet(ctx, domain.StickerSetRef{Kind: domain.StickerSetRefByID, ID: set.ID})
	if err != nil {
		return set, err
	}
	set.Keywords = upsertStickerKeywords(set.Keywords, domain.StickerKeyword{DocumentID: id, Keywords: keywords})
	set.Hash = stickerSetHash(set)
	set, _, err = s.persistStickerSetMutation(ctx, set, docs, nil)
	return set, err
}
func (s *Service) BotStickerSetMask(ctx context.Context, botID, id int64, p *domain.StickerMaskPosition) (domain.StickerSet, error) {
	s.botStickerMu.Lock()
	defer s.botStickerMu.Unlock()
	set, doc, err := s.BotStickerOwnedDocument(ctx, botID, id)
	if err != nil {
		return set, err
	}
	if set.Kind != domain.StickerSetKindMasks {
		return set, domain.ErrStickerSetTypeInvalid
	}
	if err := validateBotMaskPosition(p); err != nil {
		return set, err
	}
	_, docs, _, err := s.ResolveStickerSet(ctx, domain.StickerSetRef{Kind: domain.StickerSetRefByID, ID: set.ID})
	if err != nil {
		return set, err
	}
	doc = applyStickerInputMetadata(doc, domain.StickerSetItemInput{MaskPosition: p})
	docs = append(removeDocumentByID(docs, id), doc)
	set.Hash = (set.Hash + 1) & 0x7fffffff
	if set.Hash == 0 {
		set.Hash = 1
	}
	set, _, err = s.persistStickerSetMutation(ctx, set, docs, []domain.Document{doc})
	return set, err
}
func (s *Service) BotStickerThumbnail(ctx context.Context, botID, ownerID int64, name, format string, file domain.BotAPIFileInput, emojiID int64, custom bool) (domain.StickerSet, error) {
	s.botStickerMu.Lock()
	defer s.botStickerMu.Unlock()
	set, docs, err := s.BotStickerOwnedSet(ctx, botID, ownerID, name)
	if err != nil {
		return set, err
	}
	if custom != (set.Kind == domain.StickerSetKindEmoji) {
		return set, domain.ErrStickerSetTypeInvalid
	}
	clearStickerSetThumb(&set)
	if custom {
		if emojiID != 0 {
			if !containsInt64(set.DocumentIDs, emojiID) {
				return set, domain.ErrStickerSetFileInvalid
			}
			for _, doc := range docs {
				if doc.ID == emojiID {
					if err = s.botStickerThumbFromDocument(ctx, &set, doc); err != nil {
						return set, err
					}
				}
			}
		}
	} else {
		if format != "static" && format != "animated" && format != "video" {
			return set, domain.ErrStickerSetFileInvalid
		}
		if len(file.Bytes) > 0 || file.LocationKey != "" || file.RemoteURL != "" {
			if len(docs) == 0 || stickerDocumentFormat(docs[0]) != format {
				return set, domain.ErrStickerSetFileInvalid
			}
			doc, e := s.BotStickerFile(ctx, file, format, set.Kind, true)
			if e != nil {
				return set, e
			}
			if err = s.botStickerThumbFromDocument(ctx, &set, doc); err != nil {
				return set, err
			}
		}
	}
	set.Hash = stickerSetHash(set)
	set, _, err = s.persistStickerSetMutation(ctx, set, docs, nil)
	return set, err
}

func (s *Service) botStickerThumbFromDocument(ctx context.Context, set *domain.StickerSet, doc domain.Document) error {
	setStickerSetThumbFromDocument(set, doc)
	if len(set.Thumbs) > 0 {
		return nil
	}
	width, height := 0, 0
	for _, attr := range doc.Attributes {
		if attr.W > 0 && attr.H > 0 {
			width, height = attr.W, attr.H
			break
		}
	}
	if width == 0 || height == 0 {
		return domain.ErrStickerSetFileInvalid
	}
	blob, found, err := s.media.GetFileBlob(ctx, fmt.Sprintf("doc:%d", doc.ID))
	if err != nil {
		return err
	}
	if !found {
		return domain.ErrStickerSetFileInvalid
	}
	blob.LocationKey = fmt.Sprintf("doc:%d:botapi_set", doc.ID)
	if err = s.media.PutFileBlob(ctx, blob); err != nil {
		return err
	}
	set.Thumbs = []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "botapi_set", W: width, H: height, Size: int(doc.Size)}}
	set.ThumbVersion = 1
	return nil
}
func stickerDocumentFormat(doc domain.Document) string {
	if doc.MimeType == stickerMaterialMimeTGS {
		return "animated"
	}
	if doc.MimeType == stickerMaterialMimeWebM {
		return "video"
	}
	return "static"
}

func (s *Service) BotStickerMove(ctx context.Context, botID, id int64, position int) (domain.StickerSet, error) {
	s.botStickerMu.Lock()
	defer s.botStickerMu.Unlock()
	set, doc, err := s.BotStickerOwnedDocument(ctx, botID, id)
	if err != nil {
		return set, err
	}
	set, _, err = s.ChangeStickerPosition(ctx, set.CreatorUserID, id, doc.AccessHash, position)
	return set, err
}
func (s *Service) BotStickerRemove(ctx context.Context, botID, id int64) (domain.StickerSet, error) {
	s.botStickerMu.Lock()
	defer s.botStickerMu.Unlock()
	set, doc, err := s.BotStickerOwnedDocument(ctx, botID, id)
	if err != nil {
		return set, err
	}
	if len(set.DocumentIDs) > 1 {
		set, _, err = s.RemoveStickerFromSet(ctx, set.CreatorUserID, id, doc.AccessHash)
		return set, err
	}
	set.DocumentIDs = nil
	set.Count = 0
	set.Packs = nil
	set.Keywords = nil
	clearStickerSetThumb(&set)
	set.Hash = stickerSetHash(set)
	doc = detachStickerSetFromDocument(doc)
	set, _, err = s.persistStickerSetMutation(ctx, set, nil, []domain.Document{doc})
	return set, err
}
func (s *Service) BotStickerTitle(ctx context.Context, botID int64, name, title string) (domain.StickerSet, error) {
	s.botStickerMu.Lock()
	defer s.botStickerMu.Unlock()
	set, _, err := s.BotStickerOwnedSet(ctx, botID, 0, name)
	if err != nil {
		return set, err
	}
	set, _, err = s.RenameStickerSet(ctx, set.CreatorUserID, domain.StickerSetRef{Kind: domain.StickerSetRefByShortName, ShortName: name}, title)
	return set, err
}
func (s *Service) BotStickerDelete(ctx context.Context, botID int64, name string) (domain.StickerSet, error) {
	s.botStickerMu.Lock()
	defer s.botStickerMu.Unlock()
	set, _, err := s.BotStickerOwnedSet(ctx, botID, 0, name)
	if err != nil {
		return set, err
	}
	_, err = s.DeleteStickerSet(ctx, set.CreatorUserID, domain.StickerSetRef{Kind: domain.StickerSetRefByShortName, ShortName: name})
	return set, err
}

func (s *Service) botStickerDuplicate(ctx context.Context, set domain.StickerSet, id int64) (bool, error) {
	if containsInt64(set.DocumentIDs, id) {
		return true, nil
	}
	keys := make([]string, 0, len(set.DocumentIDs)+1)
	sourceKey := fmt.Sprintf("doc:%d", id)
	keys = append(keys, sourceKey)
	for _, documentID := range set.DocumentIDs {
		keys = append(keys, fmt.Sprintf("doc:%d", documentID))
	}
	blobs, err := s.media.GetFileBlobs(ctx, keys)
	if err != nil {
		return false, err
	}
	source, found := blobs[sourceKey]
	if !found || len(source.SHA256) == 0 {
		return false, domain.ErrStickerSetFileInvalid
	}
	for _, key := range keys[1:] {
		if blob, ok := blobs[key]; ok && bytes.Equal(blob.SHA256, source.SHA256) {
			return true, nil
		}
	}
	return false, nil
}
