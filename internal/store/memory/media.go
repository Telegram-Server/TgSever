package memory

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

type mediaPartKey struct {
	owner, file int64
	part        int
}
type mediaUploadKey struct{ owner, file int64 }
type mediaPart struct {
	value   domain.UploadPart
	updated time.Time
}
type mediaWebPage struct {
	value     domain.MessageWebPage
	refreshed int
}
type mediaProfileKey struct {
	ownerType domain.PeerType
	owner     int64
	kind      domain.ProfilePhotoKind
}
type mediaProfilePhoto struct {
	id     int64
	order  int64
	active bool
	date   int
}

// MediaStore keeps the same metadata boundary as Postgres: bytes stay in BlobBackend.
// Every read returns an isolated snapshot and set/document writes commit under one lock.
type MediaStore struct {
	mu           sync.RWMutex
	parts        map[mediaPartKey]mediaPart
	receipts     map[mediaUploadKey]domain.UploadedMediaReceipt
	blobs        map[string]domain.FileBlob
	seeds        map[string]string
	documents    map[int64]domain.Document
	photos       map[int64]domain.Photo
	webpages     map[int64]mediaWebPage
	sets         map[int64]domain.StickerSet
	reactions    map[string]domain.AvailableReaction
	profiles     map[mediaProfileKey]map[int64]mediaProfilePhoto
	profileOrder int64
}

var _ store.MediaStore = (*MediaStore)(nil)

func NewMediaStore() *MediaStore {
	return &MediaStore{
		parts: make(map[mediaPartKey]mediaPart), receipts: make(map[mediaUploadKey]domain.UploadedMediaReceipt),
		blobs: make(map[string]domain.FileBlob), seeds: make(map[string]string), documents: make(map[int64]domain.Document), photos: make(map[int64]domain.Photo),
		webpages: make(map[int64]mediaWebPage), sets: make(map[int64]domain.StickerSet), reactions: make(map[string]domain.AvailableReaction), profiles: make(map[mediaProfileKey]map[int64]mediaProfilePhoto),
	}
}

func cloneMediaBlob(b domain.FileBlob) domain.FileBlob {
	b.SHA256 = append([]byte(nil), b.SHA256...)
	return b
}
func cloneMediaPart(p domain.UploadPart) domain.UploadPart {
	p.SHA256 = append([]byte(nil), p.SHA256...)
	return p
}
func cloneMediaReceipt(r domain.UploadedMediaReceipt) domain.UploadedMediaReceipt {
	r.IntentHash = append([]byte(nil), r.IntentHash...)
	return r
}
func cloneMediaSet(set domain.StickerSet) domain.StickerSet {
	set.DocumentIDs = append([]int64(nil), set.DocumentIDs...)
	set.Thumbs = domain.ClonePhotoPtr(&domain.Photo{Sizes: set.Thumbs}).Sizes
	packs := make([]domain.StickerPack, len(set.Packs))
	for i, p := range set.Packs {
		packs[i] = domain.StickerPack{Emoticon: p.Emoticon, DocumentIDs: append([]int64(nil), p.DocumentIDs...)}
	}
	set.Packs = packs
	keywords := make([]domain.StickerKeyword, len(set.Keywords))
	for i, k := range set.Keywords {
		keywords[i] = domain.StickerKeyword{DocumentID: k.DocumentID, Keywords: append([]string(nil), k.Keywords...)}
	}
	set.Keywords = keywords
	return set
}
func cloneMediaWebPage(p domain.MessageWebPage) domain.MessageWebPage {
	p.Photo = domain.ClonePhotoPtr(p.Photo)
	return p
}

func (s *MediaStore) SaveFilePart(_ context.Context, p domain.UploadPart) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.parts[mediaPartKey{p.OwnerUserID, p.FileID, p.Part}] = mediaPart{cloneMediaPart(p), time.Now()}
	return nil
}
func (s *MediaStore) UploadPartUsage(_ context.Context, owner int64) (domain.UploadPartUsage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var usage domain.UploadPartUsage
	files := make(map[int64]bool)
	for k, p := range s.parts {
		if k.owner == owner {
			usage.Bytes += p.value.Size
			usage.Parts++
			files[k.file] = true
		}
	}
	usage.Files = len(files)
	return usage, nil
}
func (s *MediaStore) UploadPartSlot(_ context.Context, owner, file int64, part int) (domain.UploadPartSlot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var slot domain.UploadPartSlot
	for k, p := range s.parts {
		if k.owner == owner && k.file == file {
			slot.FileParts++
			if k.part == part {
				slot.Found = true
				slot.ExistingBytes = p.value.Size
				slot.ObjectKey = p.value.ObjectKey
			}
		}
	}
	return slot, nil
}
func (s *MediaStore) LoadFileParts(_ context.Context, owner, file int64) ([]domain.UploadPart, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.UploadPart, 0)
	for k, p := range s.parts {
		if k.owner == owner && k.file == file {
			out = append(out, cloneMediaPart(p.value))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Part < out[j].Part })
	return out, nil
}
func (s *MediaStore) DeleteFileParts(_ context.Context, owner, file int64) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0)
	for k, p := range s.parts {
		if k.owner == owner && k.file == file {
			out = append(out, p.value.ObjectKey)
			delete(s.parts, k)
		}
	}
	return out, nil
}
func (s *MediaStore) DeleteExpiredUploadParts(_ context.Context, before time.Time, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	type expired struct {
		k mediaPartKey
		p mediaPart
	}
	candidates := make([]expired, 0)
	for k, p := range s.parts {
		if p.updated.Before(before) {
			candidates = append(candidates, expired{k, p})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].p.updated.Before(candidates[j].p.updated) })
	if limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}
	out := make([]string, 0, len(candidates))
	for _, p := range candidates {
		out = append(out, p.p.value.ObjectKey)
		delete(s.parts, p.k)
	}
	return out, nil
}
func (s *MediaStore) GetUploadedMediaReceipt(_ context.Context, owner, file int64) (domain.UploadedMediaReceipt, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.receipts[mediaUploadKey{owner, file}]
	return cloneMediaReceipt(r), ok, nil
}
func (s *MediaStore) PutUploadedMediaReceipt(_ context.Context, r domain.UploadedMediaReceipt) (domain.UploadedMediaReceipt, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := mediaUploadKey{r.OwnerUserID, r.FileID}
	if old, ok := s.receipts[key]; ok {
		return cloneMediaReceipt(old), false, nil
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	s.receipts[key] = cloneMediaReceipt(r)
	return cloneMediaReceipt(r), true, nil
}
func (s *MediaStore) PutFileBlob(_ context.Context, b domain.FileBlob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blobs[b.LocationKey] = cloneMediaBlob(b)
	return nil
}
func (s *MediaStore) GetFileBlob(_ context.Context, key string) (domain.FileBlob, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.blobs[key]
	return cloneMediaBlob(b), ok, nil
}
func (s *MediaStore) GetFileBlobs(_ context.Context, keys []string) (map[string]domain.FileBlob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]domain.FileBlob, len(keys))
	for _, key := range keys {
		if b, ok := s.blobs[key]; ok {
			out[key] = cloneMediaBlob(b)
		}
	}
	return out, nil
}
func (s *MediaStore) GetSeedState(_ context.Context, key string) (string, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.seeds[key]
	return v, ok, nil
}
func (s *MediaStore) PutSeedState(_ context.Context, key, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seeds[key] = hash
	return nil
}
func (s *MediaStore) PutDocument(_ context.Context, d domain.Document) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.documents[d.ID] = *domain.CloneDocumentPtr(&d)
	return nil
}
func (s *MediaStore) GetDocument(_ context.Context, id int64) (domain.Document, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.documents[id]
	return *domain.CloneDocumentPtr(&d), ok, nil
}
func (s *MediaStore) GetDocuments(_ context.Context, ids []int64) ([]domain.Document, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Document, 0, len(ids))
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if d, ok := s.documents[id]; ok && !seen[id] {
			out = append(out, *domain.CloneDocumentPtr(&d))
			seen[id] = true
		}
	}
	return out, nil
}
func (s *MediaStore) PutPhoto(_ context.Context, p domain.Photo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.photos[p.ID] = *domain.ClonePhotoPtr(&p)
	return nil
}
func (s *MediaStore) GetPhoto(_ context.Context, id int64) (domain.Photo, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.photos[id]
	return *domain.ClonePhotoPtr(&p), ok, nil
}
func (s *MediaStore) PutWebPage(_ context.Context, hash int64, p domain.MessageWebPage, now int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.webpages[hash] = mediaWebPage{cloneMediaWebPage(p), now}
	return nil
}
func (s *MediaStore) GetWebPageByURLHash(_ context.Context, hash int64) (domain.MessageWebPage, int, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.webpages[hash]
	return cloneMediaWebPage(p.value), p.refreshed, ok, nil
}

func (s *MediaStore) stickerNameOccupied(name string, except int64) bool {
	if name == "" {
		return false
	}
	for id, set := range s.sets {
		if id != except && !set.Deleted && strings.EqualFold(set.ShortName, name) {
			return true
		}
	}
	return false
}
func (s *MediaStore) PutStickerSet(_ context.Context, set domain.StickerSet) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stickerNameOccupied(set.ShortName, set.ID) {
		return domain.ErrStickerSetShortNameOccupied
	}
	if set.Kind == "" {
		set.Kind = domain.StickerSetKindStickers
	}
	s.sets[set.ID] = cloneMediaSet(set)
	return nil
}
func (s *MediaStore) CreateStickerSet(_ context.Context, set domain.StickerSet, docs []domain.Document) error {
	if set.Kind == "" {
		set.Kind = domain.StickerSetKindStickers
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stickerNameOccupied(set.ShortName, set.ID) {
		return domain.ErrStickerSetShortNameOccupied
	}
	if _, exists := s.sets[set.ID]; exists {
		return domain.ErrStickerSetInvalid
	}
	s.sets[set.ID] = cloneMediaSet(set)
	for _, d := range docs {
		s.documents[d.ID] = *domain.CloneDocumentPtr(&d)
	}
	return nil
}
func (s *MediaStore) UpdateStickerSet(_ context.Context, set domain.StickerSet, docs []domain.Document) error {
	if set.Kind == "" {
		set.Kind = domain.StickerSetKindStickers
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.sets[set.ID]
	if !ok || old.Deleted || old.AccessHash != set.AccessHash {
		return domain.ErrStickerSetInvalid
	}
	set.CreatorUserID = old.CreatorUserID
	set.CreatorBotID = old.CreatorBotID
	set.ShortName = old.ShortName
	set.Deleted = old.Deleted
	s.sets[set.ID] = cloneMediaSet(set)
	for _, d := range docs {
		s.documents[d.ID] = *domain.CloneDocumentPtr(&d)
	}
	return nil
}
func (s *MediaStore) DeleteStickerSet(_ context.Context, id, creator int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	set, ok := s.sets[id]
	if !ok || set.Deleted || set.CreatorUserID != creator {
		return domain.ErrStickerSetInvalid
	}
	set.Deleted = true
	s.sets[id] = set
	return nil
}
func (s *MediaStore) AdminDeleteStickerSet(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	set, ok := s.sets[id]
	if !ok || set.Deleted {
		return domain.ErrStickerSetInvalid
	}
	set.Deleted = true
	s.sets[id] = set
	return nil
}
func (s *MediaStore) GetStickerSetByID(_ context.Context, id int64) (domain.StickerSet, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	set, ok := s.sets[id]
	if set.Deleted {
		ok = false
	}
	return cloneMediaSet(set), ok, nil
}
func (s *MediaStore) GetStickerSetByShortName(_ context.Context, name string) (domain.StickerSet, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, set := range s.sets {
		if !set.Deleted && strings.EqualFold(set.ShortName, name) {
			return cloneMediaSet(set), true, nil
		}
	}
	return domain.StickerSet{}, false, nil
}
func (s *MediaStore) GetStickerSetBySystemKey(_ context.Context, key string) (domain.StickerSet, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, set := range s.sets {
		if !set.Deleted && set.SystemKey == key {
			return cloneMediaSet(set), true, nil
		}
	}
	return domain.StickerSet{}, false, nil
}
func (s *MediaStore) ListStickerSets(_ context.Context, kind domain.StickerSetKind) ([]domain.StickerSet, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.StickerSet, 0)
	for _, set := range s.sets {
		if !set.Deleted && set.Kind == kind {
			out = append(out, cloneMediaSet(set))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SortOrder == out[j].SortOrder {
			return out[i].ID < out[j].ID
		}
		return out[i].SortOrder < out[j].SortOrder
	})
	return out, nil
}
func (s *MediaStore) ListStickerSetsByCreator(_ context.Context, creator, offset int64, limit int) ([]domain.StickerSet, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > domain.MaxCreatedStickerSets {
		limit = domain.MaxCreatedStickerSets
	}
	out := make([]domain.StickerSet, 0)
	total := 0
	for _, set := range s.sets {
		if !set.Deleted && set.CreatorUserID == creator {
			total++
			if offset == 0 || set.ID < offset {
				set.Creator = true
				out = append(out, cloneMediaSet(set))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, total, nil
}
func (s *MediaStore) StickerSetShortNameAvailable(_ context.Context, name string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.stickerNameOccupied(name, 0), nil
}
func (s *MediaStore) CountStickerSets(_ context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sets), nil
}
func (s *MediaStore) PutAvailableReaction(_ context.Context, r domain.AvailableReaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reactions[r.Reaction] = r
	return nil
}
func (s *MediaStore) ListAvailableReactions(_ context.Context) ([]domain.AvailableReaction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.AvailableReaction, 0, len(s.reactions))
	for _, r := range s.reactions {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Order == out[j].Order {
			return out[i].Reaction < out[j].Reaction
		}
		return out[i].Order < out[j].Order
	})
	return out, nil
}
func (s *MediaStore) CountAvailableReactions(_ context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.reactions), nil
}

// ponytail: the memory backend serializes transactions and snapshots metadata;
// a write journal can replace the snapshot if large in-memory catalogs need it.
func (s *MediaStore) WithTx(ctx context.Context, fn func(context.Context, store.MediaStore) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx := NewMediaStore()
	tx.profileOrder = s.profileOrder
	for k, v := range s.parts {
		tx.parts[k] = mediaPart{cloneMediaPart(v.value), v.updated}
	}
	for k, v := range s.receipts {
		tx.receipts[k] = cloneMediaReceipt(v)
	}
	for k, v := range s.blobs {
		tx.blobs[k] = cloneMediaBlob(v)
	}
	for k, v := range s.seeds {
		tx.seeds[k] = v
	}
	for k, v := range s.documents {
		tx.documents[k] = *domain.CloneDocumentPtr(&v)
	}
	for k, v := range s.photos {
		tx.photos[k] = *domain.ClonePhotoPtr(&v)
	}
	for k, v := range s.webpages {
		tx.webpages[k] = mediaWebPage{cloneMediaWebPage(v.value), v.refreshed}
	}
	for k, v := range s.sets {
		tx.sets[k] = cloneMediaSet(v)
	}
	for k, v := range s.reactions {
		tx.reactions[k] = v
	}
	for k, rows := range s.profiles {
		tx.profiles[k] = make(map[int64]mediaProfilePhoto, len(rows))
		for id, row := range rows {
			tx.profiles[k][id] = row
		}
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	s.parts = tx.parts
	s.receipts = tx.receipts
	s.blobs = tx.blobs
	s.seeds = tx.seeds
	s.documents = tx.documents
	s.photos = tx.photos
	s.webpages = tx.webpages
	s.sets = tx.sets
	s.reactions = tx.reactions
	s.profiles = tx.profiles
	s.profileOrder = tx.profileOrder
	return nil
}

func (s *MediaStore) AddProfilePhotoKind(_ context.Context, ownerType domain.PeerType, owner int64, kind domain.ProfilePhotoKind, id int64, date int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := mediaProfileKey{ownerType, owner, memoryProfileKind(kind)}
	if s.profiles[key] == nil {
		s.profiles[key] = make(map[int64]mediaProfilePhoto)
	}
	s.profileOrder++
	s.profiles[key][id] = mediaProfilePhoto{id, s.profileOrder, true, date}
	return nil
}
func (s *MediaStore) currentProfile(key mediaProfileKey) (mediaProfilePhoto, bool) {
	key.kind = memoryProfileKind(key.kind)
	var current mediaProfilePhoto
	found := false
	for _, row := range s.profiles[key] {
		if row.active && (!found || row.order > current.order) {
			current = row
			found = true
		}
	}
	return current, found
}
func (s *MediaStore) CurrentProfilePhotoKind(_ context.Context, ownerType domain.PeerType, owner int64, kind domain.ProfilePhotoKind) (int64, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	row, ok := s.currentProfile(mediaProfileKey{ownerType, owner, kind})
	return row.id, ok, nil
}
func (s *MediaStore) CurrentProfilePhotos(ctx context.Context, ownerType domain.PeerType, owners []int64) (map[int64]domain.ProfilePhotoRef, error) {
	return s.CurrentProfilePhotosKind(ctx, ownerType, owners, domain.ProfilePhotoKindProfile)
}
func (s *MediaStore) CurrentProfilePhotosKind(_ context.Context, ownerType domain.PeerType, owners []int64, kind domain.ProfilePhotoKind) (map[int64]domain.ProfilePhotoRef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[int64]domain.ProfilePhotoRef, len(owners))
	for _, owner := range owners {
		row, ok := s.currentProfile(mediaProfileKey{ownerType, owner, kind})
		if !ok {
			continue
		}
		p, found := s.photos[row.id]
		if !found {
			continue
		}
		out[owner] = domain.ProfilePhotoRef{PhotoID: p.ID, DCID: p.DCID, Stripped: append([]byte(nil), domain.StrippedFromSizes(p.Sizes)...), HasVideo: domain.PhotoHasVideo(p.Sizes)}
	}
	return out, nil
}
func memoryProfileKind(kind domain.ProfilePhotoKind) domain.ProfilePhotoKind {
	if kind == domain.ProfilePhotoKindFallback {
		return kind
	}
	return domain.ProfilePhotoKindProfile
}
func (s *MediaStore) profileRows(key mediaProfileKey, maxID int64) []mediaProfilePhoto {
	key.kind = memoryProfileKind(key.kind)
	out := make([]mediaProfilePhoto, 0)
	for _, row := range s.profiles[key] {
		if row.active && (maxID <= 0 || row.id < maxID) {
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].order > out[j].order })
	return out
}
func (s *MediaStore) ListProfilePhotosKind(_ context.Context, ownerType domain.PeerType, owner int64, kind domain.ProfilePhotoKind, offset, limit int, maxID int64) ([]int64, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key := mediaProfileKey{ownerType, owner, memoryProfileKind(kind)}
	total := len(s.profileRows(key, 0))
	rows := s.profileRows(key, maxID)
	if offset < 0 {
		offset = 0
	}
	if offset > len(rows) {
		offset = len(rows)
	}
	rows = rows[offset:]
	if limit < 0 {
		limit = len(rows)
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.id)
	}
	return ids, total, nil
}
func (s *MediaStore) ListProfilePhotoDetailsKind(_ context.Context, ownerType domain.PeerType, owner int64, kind domain.ProfilePhotoKind, offset, limit int, maxID int64) ([]domain.Photo, int, error) {
	if limit <= 0 {
		return nil, 0, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	key := mediaProfileKey{ownerType, owner, memoryProfileKind(kind)}
	total := len(s.profileRows(key, 0))
	rows := s.profileRows(key, maxID)
	if offset < 0 && maxID > 0 {
		rows = nil
		if row, ok := s.profiles[key][maxID]; ok && row.active {
			rows = []mediaProfilePhoto{row}
		}
	} else {
		if offset < 0 {
			offset = 0
		}
		if offset > len(rows) {
			offset = len(rows)
		}
		rows = rows[offset:]
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	photos := make([]domain.Photo, 0, len(rows))
	for _, row := range rows {
		if photo, ok := s.photos[row.id]; ok {
			photos = append(photos, *domain.ClonePhotoPtr(&photo))
		}
	}
	return photos, total, nil
}
func (s *MediaStore) DeleteProfilePhotos(ctx context.Context, ownerType domain.PeerType, owner int64, ids []int64) ([]int64, error) {
	return s.DeleteProfilePhotosKind(ctx, ownerType, owner, domain.ProfilePhotoKindProfile, ids)
}
func (s *MediaStore) DeleteProfilePhotosKind(_ context.Context, ownerType domain.PeerType, owner int64, kind domain.ProfilePhotoKind, ids []int64) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := s.profiles[mediaProfileKey{ownerType, owner, memoryProfileKind(kind)}]
	out := make([]int64, 0)
	for _, id := range ids {
		if row, ok := rows[id]; ok && row.active {
			row.active = false
			rows[id] = row
			out = append(out, id)
		}
	}
	return out, nil
}
func (s *MediaStore) ProfilePhotoInHistory(_ context.Context, ownerType domain.PeerType, owner, id int64) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for key, rows := range s.profiles {
		if key.ownerType == ownerType && key.owner == owner {
			if _, ok := rows[id]; ok {
				return true, nil
			}
		}
	}
	return false, nil
}
func (s *MediaStore) OfferProfilePhoto(_ context.Context, ownerType domain.PeerType, owner, id int64, date int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := mediaProfileKey{ownerType, owner, domain.ProfilePhotoKindProfile}
	if s.profiles[key] == nil {
		s.profiles[key] = make(map[int64]mediaProfilePhoto)
	}
	if _, exists := s.profiles[key][id]; !exists {
		s.profileOrder++
		s.profiles[key][id] = mediaProfilePhoto{id, s.profileOrder, false, date}
	}
	return nil
}
