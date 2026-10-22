package botapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"telesrv/internal/domain"
)

type StickerGateway interface {
	BotAPIGetStickerSet(context.Context, int64, string) (domain.StickerSet, []domain.Document, error)
	BotAPIGetCustomEmojiStickers(context.Context, int64, []int64) ([]domain.Document, error)
	BotAPIUploadStickerFile(context.Context, int64, int64, domain.BotAPIFileInput, string) (domain.Document, error)
	BotAPICreateNewStickerSet(context.Context, int64, domain.BotAPICreateStickerSet) error
	BotAPIAddStickerToSet(context.Context, int64, int64, string, domain.BotAPIInputSticker) error
	BotAPIReplaceStickerInSet(context.Context, int64, int64, string, int64, domain.BotAPIInputSticker) error
	BotAPISetStickerPositionInSet(context.Context, int64, int64, int) error
	BotAPIDeleteStickerFromSet(context.Context, int64, int64) error
	BotAPISetStickerEmojiList(context.Context, int64, int64, []string) error
	BotAPISetStickerKeywords(context.Context, int64, int64, []string) error
	BotAPISetStickerMaskPosition(context.Context, int64, int64, *domain.StickerMaskPosition) error
	BotAPISetStickerSetTitle(context.Context, int64, string, string) error
	BotAPISetStickerSetThumbnail(context.Context, int64, int64, string, string, domain.BotAPIFileInput) error
	BotAPISetCustomEmojiStickerSetThumbnail(context.Context, int64, string, int64) error
	BotAPIDeleteStickerSet(context.Context, int64, string) error
}

func init() {
	registerMethods(map[string]methodFunc{
		"getStickerSet": (*handler).getStickerSet, "getCustomEmojiStickers": (*handler).getCustomEmojiStickers, "uploadStickerFile": (*handler).uploadStickerFile,
		"createNewStickerSet": (*handler).createNewStickerSet, "addStickerToSet": (*handler).addStickerToSet, "replaceStickerInSet": (*handler).replaceStickerInSet,
		"setStickerPositionInSet": (*handler).setStickerPositionInSet, "deleteStickerFromSet": (*handler).deleteStickerFromSet,
		"setStickerEmojiList": (*handler).setStickerEmojiList, "setStickerKeywords": (*handler).setStickerKeywords, "setStickerMaskPosition": (*handler).setStickerMaskPosition,
		"setStickerSetTitle": (*handler).setStickerSetTitle, "setStickerSetThumbnail": (*handler).setStickerSetThumbnail, "setCustomEmojiStickerSetThumbnail": (*handler).setCustomEmojiStickerSetThumbnail, "deleteStickerSet": (*handler).deleteStickerSet,
	})
	registerErrorMarkers("STICKERSET_INVALID", "STICKERSET_NOT_MODIFIED", "STICKER_FILE_INVALID", "STICKER_EMOJI_INVALID", "STICKERS_TOO_MUCH", "STICKERS_EMPTY", "PACK_SHORT_NAME_INVALID", "PACK_SHORT_NAME_OCCUPIED", "PACK_TITLE_INVALID", "PACK_TYPE_INVALID", "POSITION_INVALID", "USER_ID_INVALID")
}
func (h *handler) stickerRequest(w http.ResponseWriter, r *http.Request) (StickerGateway, map[string]string, map[string]uploadedFile, bool) {
	g, ok := h.gateway.(StickerGateway)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return nil, nil, nil, false
	}
	v, f, err := requestValuesWithFiles(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return nil, nil, nil, false
	}
	return g, v, f, true
}
func stickerResponse(w http.ResponseWriter, err error) {
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, true)
}
func stickerNumber(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("STICKER_FILE_INVALID")
	}
	return id, nil
}
func stickerFileNumber(raw string) (int64, error) {
	key, ok := decodeBotAPIFileID(raw)
	if !ok || !strings.HasPrefix(key, "doc:") {
		return 0, errors.New("STICKER_FILE_INVALID")
	}
	return stickerNumber(strings.TrimPrefix(key, "doc:"))
}
func stickerOwner(v map[string]string) (int64, error) {
	id, err := strconv.ParseInt(v["user_id"], 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("USER_ID_INVALID")
	}
	return id, nil
}
func stickerStrings(raw string, optional bool) ([]string, error) {
	if raw == "" && optional {
		return []string{}, nil
	}
	var list []string
	if json.Unmarshal([]byte(raw), &list) != nil || list == nil {
		return nil, errors.New("STICKER_FILE_INVALID")
	}
	return list, nil
}
func parseInputSticker(raw string, files map[string]uploadedFile) (domain.BotAPIInputSticker, error) {
	var input struct {
		Sticker      string                      `json:"sticker"`
		Format       string                      `json:"format"`
		EmojiList    []string                    `json:"emoji_list"`
		Keywords     []string                    `json:"keywords"`
		MaskPosition *domain.StickerMaskPosition `json:"mask_position"`
	}
	if json.Unmarshal([]byte(raw), &input) != nil {
		return domain.BotAPIInputSticker{}, errors.New("STICKER_FILE_INVALID")
	}
	file, ok := botAPIFileInput(input.Sticker, files, "", nil)
	if !ok {
		return domain.BotAPIInputSticker{}, errors.New("STICKER_FILE_INVALID")
	}
	return domain.BotAPIInputSticker{File: file, Format: input.Format, EmojiList: input.EmojiList, Keywords: input.Keywords, MaskPosition: input.MaskPosition}, nil
}
func (h *handler) getStickerSet(w http.ResponseWriter, r *http.Request, botID int64) {
	g, values, _, ok := h.stickerRequest(w, r)
	if !ok {
		return
	}
	set, docs, err := g.BotAPIGetStickerSet(r.Context(), botID, values["name"])
	if err != nil {
		stickerResponse(w, err)
		return
	}
	items := make([]map[string]any, 0, len(docs))
	for _, doc := range docs {
		items = append(items, apiManagedSticker(doc, set.ShortName))
	}
	out := map[string]any{"name": set.ShortName, "title": set.Title, "sticker_type": stickerAPIKind(set.Kind), "stickers": items}
	if set.ThumbDocumentID != 0 {
		prefix := "doc:" + strconv.FormatInt(set.ThumbDocumentID, 10) + ":"
		if thumbs := apiPhotoSizesWithPrefix(set.Thumbs, prefix); len(thumbs) > 0 {
			out["thumbnail"] = thumbs[len(thumbs)-1]
		}
	}
	writeAPIOK(w, out)
}
func (h *handler) getCustomEmojiStickers(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, _, ok := h.stickerRequest(w, r)
	if !ok {
		return
	}
	raw, err := stickerStrings(v["custom_emoji_ids"], false)
	if err != nil || len(raw) > 200 {
		stickerResponse(w, errors.New("STICKER_FILE_INVALID"))
		return
	}
	ids := make([]int64, 0, len(raw))
	for _, s := range raw {
		id, e := stickerNumber(s)
		if e != nil {
			stickerResponse(w, e)
			return
		}
		ids = append(ids, id)
	}
	docs, err := g.BotAPIGetCustomEmojiStickers(r.Context(), botID, ids)
	if err != nil {
		stickerResponse(w, err)
		return
	}
	out := make([]map[string]any, 0, len(docs))
	for _, doc := range docs {
		out = append(out, apiManagedSticker(doc, ""))
	}
	writeAPIOK(w, out)
}
func (h *handler) uploadStickerFile(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, f, ok := h.stickerRequest(w, r)
	if !ok {
		return
	}
	id, err := stickerOwner(v)
	if err != nil {
		stickerResponse(w, err)
		return
	}
	file, ok := botAPIFileInput(v["sticker"], f, "sticker", nil)
	if !ok || len(file.Bytes) == 0 {
		stickerResponse(w, errors.New("STICKER_FILE_INVALID"))
		return
	}
	doc, err := g.BotAPIUploadStickerFile(r.Context(), botID, id, file, v["sticker_format"])
	if err != nil {
		stickerResponse(w, err)
		return
	}
	out := apiDocument(doc)
	delete(out, "file_name")
	delete(out, "mime_type")
	delete(out, "thumbnail")
	writeAPIOK(w, out)
}
func (h *handler) createNewStickerSet(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, f, ok := h.stickerRequest(w, r)
	if !ok {
		return
	}
	id, err := stickerOwner(v)
	if err != nil {
		stickerResponse(w, err)
		return
	}
	var raw []json.RawMessage
	if json.Unmarshal([]byte(v["stickers"]), &raw) != nil || len(raw) < 1 || len(raw) > 50 {
		stickerResponse(w, errors.New("STICKERS_TOO_MUCH"))
		return
	}
	items := make([]domain.BotAPIInputSticker, 0, len(raw))
	for _, s := range raw {
		item, e := parseInputSticker(string(s), f)
		if e != nil {
			stickerResponse(w, e)
			return
		}
		items = append(items, item)
	}
	kind := domain.StickerSetKindStickers
	switch v["sticker_type"] {
	case "", "regular":
	case "mask":
		kind = domain.StickerSetKindMasks
	case "custom_emoji":
		kind = domain.StickerSetKindEmoji
	default:
		stickerResponse(w, errors.New("PACK_TYPE_INVALID"))
		return
	}
	stickerResponse(w, g.BotAPICreateNewStickerSet(r.Context(), botID, domain.BotAPICreateStickerSet{UserID: id, Name: v["name"], Title: v["title"], Kind: kind, NeedsRepainting: apiBool(v["needs_repainting"]), Stickers: items}))
}
func (h *handler) addStickerToSet(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, f, ok := h.stickerRequest(w, r)
	if !ok {
		return
	}
	id, err := stickerOwner(v)
	if err != nil {
		stickerResponse(w, err)
		return
	}
	item, err := parseInputSticker(v["sticker"], f)
	if err != nil {
		stickerResponse(w, err)
		return
	}
	stickerResponse(w, g.BotAPIAddStickerToSet(r.Context(), botID, id, v["name"], item))
}
func (h *handler) replaceStickerInSet(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, f, ok := h.stickerRequest(w, r)
	if !ok {
		return
	}
	id, err := stickerOwner(v)
	if err != nil {
		stickerResponse(w, err)
		return
	}
	old, err := stickerFileNumber(v["old_sticker"])
	if err != nil {
		stickerResponse(w, err)
		return
	}
	item, err := parseInputSticker(v["sticker"], f)
	if err != nil {
		stickerResponse(w, err)
		return
	}
	stickerResponse(w, g.BotAPIReplaceStickerInSet(r.Context(), botID, id, v["name"], old, item))
}
func (h *handler) setStickerPositionInSet(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, _, ok := h.stickerRequest(w, r)
	if !ok {
		return
	}
	id, err := stickerFileNumber(v["sticker"])
	if err != nil {
		stickerResponse(w, err)
		return
	}
	position, err := strconv.Atoi(v["position"])
	if err != nil || position < 0 {
		stickerResponse(w, errors.New("POSITION_INVALID"))
		return
	}
	stickerResponse(w, g.BotAPISetStickerPositionInSet(r.Context(), botID, id, position))
}
func (h *handler) deleteStickerFromSet(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, _, ok := h.stickerRequest(w, r)
	if !ok {
		return
	}
	id, err := stickerFileNumber(v["sticker"])
	if err != nil {
		stickerResponse(w, err)
		return
	}
	stickerResponse(w, g.BotAPIDeleteStickerFromSet(r.Context(), botID, id))
}
func (h *handler) setStickerEmojiList(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, _, ok := h.stickerRequest(w, r)
	if !ok {
		return
	}
	id, err := stickerFileNumber(v["sticker"])
	if err != nil {
		stickerResponse(w, err)
		return
	}
	list, err := stickerStrings(v["emoji_list"], false)
	if err != nil {
		stickerResponse(w, err)
		return
	}
	stickerResponse(w, g.BotAPISetStickerEmojiList(r.Context(), botID, id, list))
}
func (h *handler) setStickerKeywords(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, _, ok := h.stickerRequest(w, r)
	if !ok {
		return
	}
	id, err := stickerFileNumber(v["sticker"])
	if err != nil {
		stickerResponse(w, err)
		return
	}
	list, err := stickerStrings(v["keywords"], true)
	if err != nil {
		stickerResponse(w, err)
		return
	}
	stickerResponse(w, g.BotAPISetStickerKeywords(r.Context(), botID, id, list))
}
func (h *handler) setStickerMaskPosition(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, _, ok := h.stickerRequest(w, r)
	if !ok {
		return
	}
	id, err := stickerFileNumber(v["sticker"])
	if err != nil {
		stickerResponse(w, err)
		return
	}
	var p *domain.StickerMaskPosition
	if raw := v["mask_position"]; raw != "" {
		if json.Unmarshal([]byte(raw), &p) != nil {
			stickerResponse(w, errors.New("STICKER_FILE_INVALID"))
			return
		}
	}
	stickerResponse(w, g.BotAPISetStickerMaskPosition(r.Context(), botID, id, p))
}
func (h *handler) setStickerSetTitle(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, _, ok := h.stickerRequest(w, r)
	if !ok {
		return
	}
	stickerResponse(w, g.BotAPISetStickerSetTitle(r.Context(), botID, v["name"], v["title"]))
}
func (h *handler) setStickerSetThumbnail(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, f, ok := h.stickerRequest(w, r)
	if !ok {
		return
	}
	id, err := stickerOwner(v)
	if err != nil {
		stickerResponse(w, err)
		return
	}
	var file domain.BotAPIFileInput
	if v["thumbnail"] != "" || len(f["thumbnail"].Bytes) > 0 {
		var valid bool
		file, valid = botAPIFileInput(v["thumbnail"], f, "thumbnail", nil)
		if !valid {
			stickerResponse(w, errors.New("STICKER_FILE_INVALID"))
			return
		}
	}
	stickerResponse(w, g.BotAPISetStickerSetThumbnail(r.Context(), botID, id, v["name"], v["format"], file))
}
func (h *handler) setCustomEmojiStickerSetThumbnail(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, _, ok := h.stickerRequest(w, r)
	if !ok {
		return
	}
	var id int64
	var err error
	if v["custom_emoji_id"] != "" {
		id, err = stickerNumber(v["custom_emoji_id"])
		if err != nil {
			stickerResponse(w, err)
			return
		}
	}
	stickerResponse(w, g.BotAPISetCustomEmojiStickerSetThumbnail(r.Context(), botID, v["name"], id))
}
func (h *handler) deleteStickerSet(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, _, ok := h.stickerRequest(w, r)
	if !ok {
		return
	}
	stickerResponse(w, g.BotAPIDeleteStickerSet(r.Context(), botID, v["name"]))
}
func stickerAPIKind(kind domain.StickerSetKind) string {
	if kind == domain.StickerSetKindMasks {
		return "mask"
	}
	if kind == domain.StickerSetKindEmoji {
		return "custom_emoji"
	}
	return "regular"
}
func apiManagedSticker(doc domain.Document, name string) map[string]any {
	out := apiDocument(doc)
	delete(out, "mime_type")
	delete(out, "file_name")
	out["type"] = "regular"
	out["width"] = 0
	out["height"] = 0
	out["is_animated"] = doc.MimeType == "application/x-tgsticker"
	out["is_video"] = doc.MimeType == "video/webm"
	for _, a := range doc.Attributes {
		if a.W > 0 && a.H > 0 {
			out["width"] = a.W
			out["height"] = a.H
		}
		if a.Kind == domain.DocAttrSticker || a.Kind == domain.DocAttrCustomEmoji {
			if a.Alt != "" {
				out["emoji"] = a.Alt
			}
			if a.Mask {
				out["type"] = "mask"
			}
			if a.MaskPosition != nil {
				out["mask_position"] = a.MaskPosition
			}
			if a.Kind == domain.DocAttrCustomEmoji {
				out["type"] = "custom_emoji"
				out["custom_emoji_id"] = strconv.FormatInt(doc.ID, 10)
			}
			if a.TextColor {
				out["needs_repainting"] = true
			}
		}
	}
	if name != "" {
		out["set_name"] = name
	}
	return out
}
