package memory

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// StarGiftOwnerStore commits gift ownership/issuance and the real Stars ledger
// and the message outbox under their store locks. No gift-coin wallet participates.
type StarGiftOwnerStore struct {
	gifts    *StarGiftStore
	stars    *StarsStore
	messages *MessageStore
	policy   domain.StarGiftLifecyclePolicy
	upgrades map[string]memoryGiftUpgrade
}

type memoryGiftUpgrade struct {
	request domain.StarGiftUpgradeRequest
	result  domain.StarGiftUpgradeResult
}

func NewStarGiftOwnerStore(gifts *StarGiftStore, stars *StarsStore, messages *MessageStore, policy domain.StarGiftLifecyclePolicy) *StarGiftOwnerStore {
	return &StarGiftOwnerStore{gifts: gifts, stars: stars, messages: messages, policy: policy, upgrades: make(map[string]memoryGiftUpgrade)}
}

func (s *StarGiftOwnerStore) configured() bool {
	return s != nil && s.gifts != nil && s.stars != nil && s.messages != nil && s.policy.Valid()
}

func (s *StarGiftOwnerStore) savedIndex(ref domain.SavedStarGiftRef) int {
	if !ref.Valid() {
		return -1
	}
	for i, saved := range s.gifts.gifts {
		if s.gifts.savedStarGiftMatchesRef(saved, ref) {
			return i
		}
	}
	return -1
}

func (s *StarGiftOwnerStore) balance(userID, delta int64, reason domain.StarsTransactionReason, peer domain.Peer, date int) (domain.StarsBalance, error) {
	state := s.stars.states[userID]
	if state == nil {
		state = &starsState{}
	}
	if delta < 0 && state.balance < -delta {
		return domain.StarsBalance{}, domain.ErrStarsInsufficient
	}
	if delta > 0 && state.balance > math.MaxInt64-delta {
		return domain.StarsBalance{}, domain.ErrStarsInvalidAmount
	}
	if delta != 0 {
		state.balance += delta
		s.stars.states[userID] = state
		s.stars.appendTxn(state, userID, delta, reason, peer, date, "Star gift", "")
	}
	return domain.StarsBalance{UserID: userID, Balance: state.balance, Granted: state.granted}, nil
}

func (s *StarGiftOwnerStore) removeCollections(owner domain.Peer, id int64) {
	for i := range s.gifts.collections[owner] {
		collection := &s.gifts.collections[owner][i]
		ids := collection.GiftIDs[:0]
		for _, existing := range collection.GiftIDs {
			if existing != id {
				ids = append(ids, existing)
			}
		}
		collection.GiftIDs = ids
		collection.Hash = domain.StarGiftCollectionHash(collection.Title, ids)
	}
	s.gifts.refreshCollectionMembershipsLocked(owner)
}

func (s *StarGiftOwnerStore) ConvertStarGift(_ context.Context, req domain.StarGiftConvertRequest) (domain.StarGiftConvertResult, error) {
	if !s.configured() || req.Date <= 0 || req.ActorUserID <= 0 || req.Ref.Owner.Type != domain.PeerTypeUser || req.Ref.Owner.ID != req.ActorUserID {
		return domain.StarGiftConvertResult{}, domain.ErrStarGiftOwnerInvalid
	}
	s.gifts.mu.Lock()
	defer s.gifts.mu.Unlock()
	s.stars.mu.Lock()
	defer s.stars.mu.Unlock()
	i := s.savedIndex(req.Ref)
	if i < 0 {
		return domain.StarGiftConvertResult{}, domain.ErrStarGiftNotFound
	}
	gift := s.gifts.gifts[i]
	if gift.Converted {
		return domain.StarGiftConvertResult{}, domain.ErrStarGiftAlreadyConverted
	}
	if gift.UniqueGiftID != 0 || !gift.LifecycleStatus.Live() {
		return domain.StarGiftConvertResult{}, domain.ErrStarGiftAlreadyUpgraded
	}
	if gift.ConvertStars <= 0 {
		return domain.StarGiftConvertResult{}, domain.ErrStarGiftNotConvertible
	}
	balance, err := s.balance(req.ActorUserID, gift.ConvertStars, domain.StarsReasonGift, domain.Peer{Type: domain.PeerTypeUser, ID: gift.FromUserID}, req.Date)
	if err != nil {
		return domain.StarGiftConvertResult{}, err
	}
	gift.Converted, gift.Unsaved, gift.LifecycleStatus = true, true, domain.StarGiftLifecycleConverted
	gift.PinnedOrder, gift.CollectionIDs = 0, nil
	s.gifts.gifts[i] = gift
	s.removeCollections(gift.Owner, gift.ID)
	return domain.StarGiftConvertResult{Saved: gift, OwnerBalance: balance.Balance}, nil
}

func drawMemoryGiftAttribute(attributes []domain.StarGiftCollectibleAttribute) (domain.StarGiftCollectibleAttribute, error) {
	var weight int64
	for _, a := range attributes {
		if !a.Crafted && (a.RarityKind == "" || a.RarityKind == domain.StarGiftRarityPermille) && a.RarityPermille > 0 {
			weight += int64(a.RarityPermille)
		}
	}
	if weight == 0 {
		return domain.StarGiftCollectibleAttribute{}, domain.ErrStarGiftCollectibleUnavailable
	}
	random, err := rand.Int(rand.Reader, big.NewInt(weight))
	if err != nil {
		return domain.StarGiftCollectibleAttribute{}, err
	}
	choice := random.Int64()
	for _, a := range attributes {
		if a.Crafted || (a.RarityKind != "" && a.RarityKind != domain.StarGiftRarityPermille) || a.RarityPermille <= 0 {
			continue
		}
		choice -= int64(a.RarityPermille)
		if choice < 0 {
			return cloneCollectibleAttribute(a), nil
		}
	}
	return domain.StarGiftCollectibleAttribute{}, domain.ErrStarGiftCollectibleUnavailable
}

func (s *StarGiftOwnerStore) UpgradeStarGift(_ context.Context, req domain.StarGiftUpgradeRequest) (domain.StarGiftUpgradeResult, error) {
	if !s.configured() || req.UserID <= 0 || req.Ref.Owner != (domain.Peer{Type: domain.PeerTypeUser, ID: req.UserID}) || req.Date <= 0 ||
		strings.TrimSpace(req.CommandKey) == "" || len(req.CommandKey) > 256 || req.ChargeStars < 0 ||
		(req.RequirePrepaid && (req.ChargeStars != 0 || req.FormID != 0)) || (!req.RequirePrepaid && (req.ChargeStars <= 0 || req.FormID == 0)) ||
		req.ModelAttributeID != 0 || req.PatternAttributeID != 0 || req.BackdropAttributeID != 0 {
		return domain.StarGiftUpgradeResult{}, domain.ErrStarGiftCollectibleInvalid
	}
	s.gifts.mu.Lock()
	defer s.gifts.mu.Unlock()
	s.stars.mu.Lock()
	defer s.stars.mu.Unlock()
	key := fmt.Sprintf("%d:%s", req.UserID, req.CommandKey)
	if replay, found := s.upgrades[key]; found {
		old := replay.request
		if old.Ref != req.Ref || old.ChargeStars != req.ChargeStars || old.FormID != req.FormID || old.RequirePrepaid != req.RequirePrepaid || old.KeepOriginalDetails != req.KeepOriginalDetails {
			return domain.StarGiftUpgradeResult{}, domain.ErrStarGiftIdempotencyConflict
		}
		result := replay.result
		result.Duplicate = true
		return result, nil
	}
	i := s.savedIndex(req.Ref)
	if i < 0 {
		return domain.StarGiftUpgradeResult{}, domain.ErrStarGiftNotFound
	}
	saved := s.gifts.gifts[i]
	if saved.Converted || !saved.LifecycleStatus.Live() {
		return domain.StarGiftUpgradeResult{}, domain.ErrStarGiftAlreadyConverted
	}
	if saved.UniqueGiftID != 0 {
		return domain.StarGiftUpgradeResult{}, domain.ErrStarGiftAlreadyUpgraded
	}
	revision, found := s.gifts.collectibles[saved.GiftID]
	if !found || !revision.Published {
		return domain.StarGiftUpgradeResult{}, domain.ErrStarGiftCollectibleUnavailable
	}
	if revision.Issued >= revision.SupplyTotal {
		return domain.StarGiftUpgradeResult{}, domain.ErrStarGiftCollectibleSoldOut
	}
	if (req.RequirePrepaid && saved.PrepaidUpgradeStars <= 0) || (!req.RequirePrepaid && req.ChargeStars != revision.UpgradeStars) {
		return domain.StarGiftUpgradeResult{}, domain.ErrStarGiftCollectibleUnavailable
	}
	model, err := drawMemoryGiftAttribute(revision.Models)
	if err != nil {
		return domain.StarGiftUpgradeResult{}, err
	}
	pattern, err := drawMemoryGiftAttribute(revision.Patterns)
	if err != nil {
		return domain.StarGiftUpgradeResult{}, err
	}
	backdrop, err := drawMemoryGiftAttribute(revision.Backdrops)
	if err != nil {
		return domain.StarGiftUpgradeResult{}, err
	}
	num := saved.GiftNum
	if num == 0 {
		num = 1
	}
	for {
		used := false
		for _, unique := range s.gifts.uniqueByID {
			if unique.GiftID == saved.GiftID && unique.Num == num {
				used = true
				break
			}
		}
		if !used {
			break
		}
		if saved.GiftNum != 0 {
			return domain.StarGiftUpgradeResult{}, domain.ErrStarGiftCollectibleUnavailable
		}
		num++
	}
	if num > revision.SupplyTotal {
		return domain.StarGiftUpgradeResult{}, domain.ErrStarGiftCollectibleSoldOut
	}
	id := int64(1)
	for s.gifts.uniqueByID[id].ID != 0 {
		id++
	}
	catalog := s.gifts.revisions[saved.RevisionID]
	unique := domain.UniqueStarGift{ID: id, GiftID: saved.GiftID, SourceSavedGiftID: saved.ID, CollectibleRevisionID: revision.ID,
		Title: catalog.Title, Slug: fmt.Sprintf("%s-%d", revision.SlugPrefix, num), Num: num, Owner: saved.Owner,
		Model: model, Pattern: pattern, Backdrop: backdrop, AvailabilityIssued: revision.Issued + 1, AvailabilityTotal: revision.SupplyTotal,
		RequirePremium: catalog.RequirePremium, ReleasedBy: catalog.ReleasedBy, ThemeAvailable: catalog.PeerColorAvailable,
		KeepOriginalDetails: req.KeepOriginalDetails, CreatedAt: time.Unix(int64(req.Date), 0)}
	if req.KeepOriginalDetails {
		unique.OriginalFromUserID, unique.OriginalOwner, unique.OriginalDate = saved.FromUserID, saved.Owner, saved.Date
		unique.OriginalMessage, unique.OriginalMessageEntities, unique.OriginalNameHidden = saved.Message, append([]domain.MessageEntity(nil), saved.MessageEntities...), saved.NameHidden
	}
	current, _ := s.balance(req.UserID, 0, domain.StarsReasonGiftUpgrade, saved.Owner, req.Date)
	if current.Balance < req.ChargeStars {
		return domain.StarGiftUpgradeResult{}, domain.ErrStarsInsufficient
	}
	s.messages.mu.Lock()
	defer s.messages.mu.Unlock()
	saved.UpgradeMsgID = s.messages.nextBox[req.UserID] + 1
	saved.UniqueGiftID, saved.ConvertStars, saved.PrepaidUpgradeStars, saved.PrepaidUpgradeHash = id, 0, 0, ""
	saved.TransferStars = s.policy.TransferStars
	saved.CanTransferAt, saved.CanExportAt, saved.CanResellAt = memoryGiftReadyAt(req.Date, s.policy.TransferDelaySeconds), memoryGiftReadyAt(req.Date, s.policy.ExportDelaySeconds), memoryGiftReadyAt(req.Date, s.policy.ResellDelaySeconds)
	saved.DropOriginalDetailsStars, saved.Unique = s.policy.DropOriginalDetailsStars, nil
	result := domain.StarGiftUpgradeResult{Saved: saved, Unique: unique}
	sent, err := s.sendUniqueLocked(saved.FromUserID, req.UserID, req.Date, req.CommandKey, saved, unique, true, func() {
		result.Balance, _ = s.balance(req.UserID, -req.ChargeStars, domain.StarsReasonGiftUpgrade, saved.Owner, req.Date)
		revision.Issued++
		s.gifts.collectibles[saved.GiftID] = revision
		live := s.gifts.catalog[saved.GiftID]
		live.UpgradeIssued = revision.Issued
		s.gifts.catalog[saved.GiftID] = live
		s.gifts.uniqueByID[id], s.gifts.uniqueBySlug[unique.Slug] = unique, id
		s.gifts.gifts[i] = saved
	})
	if err != nil {
		return domain.StarGiftUpgradeResult{}, err
	}
	result.Send = sent
	s.upgrades[key] = memoryGiftUpgrade{request: req, result: result}
	return result, nil
}

func (s *StarGiftOwnerStore) StarGiftUpgradeReceipt(_ context.Context, userID int64, commandKey string) (domain.StarGiftUpgradeReceipt, bool, error) {
	if !s.configured() {
		return domain.StarGiftUpgradeReceipt{}, false, domain.ErrStarGiftUnavailable
	}
	s.gifts.mu.Lock()
	defer s.gifts.mu.Unlock()
	replay, found := s.upgrades[fmt.Sprintf("%d:%s", userID, commandKey)]
	if !found {
		return domain.StarGiftUpgradeReceipt{}, false, nil
	}
	r, result := replay.request, replay.result
	return domain.StarGiftUpgradeReceipt{UserID: userID, SourceSavedGiftID: result.Saved.ID, UniqueGiftID: result.Unique.ID,
		FormID: r.FormID, ChargeStars: r.ChargeStars, BalanceAfter: result.Balance.Balance, RequirePrepaid: r.RequirePrepaid, KeepOriginalDetails: r.KeepOriginalDetails}, true, nil
}

func (s *StarGiftOwnerStore) TransferStarGift(_ context.Context, req domain.StarGiftTransferRequest) (domain.StarGiftTransferResult, error) {
	if !s.configured() || req.ActorUserID <= 0 || req.Ref.Owner != (domain.Peer{Type: domain.PeerTypeUser, ID: req.ActorUserID}) || req.To.Type != domain.PeerTypeUser || req.To.ID <= 0 || req.To == req.Ref.Owner || req.ChargeStars < 0 || req.Date <= 0 || strings.TrimSpace(req.CommandKey) == "" {
		return domain.StarGiftTransferResult{}, domain.ErrStarGiftTransferUnavailable
	}
	s.gifts.mu.Lock()
	defer s.gifts.mu.Unlock()
	s.stars.mu.Lock()
	defer s.stars.mu.Unlock()
	i := s.savedIndex(req.Ref)
	if i < 0 {
		return domain.StarGiftTransferResult{}, domain.ErrStarGiftNotFound
	}
	saved := s.gifts.gifts[i]
	unique, found := s.gifts.uniqueByID[saved.UniqueGiftID]
	if !found || unique.Owner != saved.Owner || !saved.LifecycleStatus.Live() || saved.Converted || unique.Burned || unique.OwnerAddress != "" ||
		saved.CanTransferAt > req.Date || saved.TransferStars != req.ChargeStars || unique.ResellAmount != nil || len(s.gifts.listings[unique.ID]) > 0 {
		return domain.StarGiftTransferResult{}, domain.ErrStarGiftTransferUnavailable
	}
	current, _ := s.balance(req.ActorUserID, 0, domain.StarsReasonGiftTransfer, req.To, req.Date)
	if current.Balance < req.ChargeStars {
		return domain.StarGiftTransferResult{}, domain.ErrStarsInsufficient
	}
	s.messages.mu.Lock()
	defer s.messages.mu.Unlock()
	msgID := s.messages.nextBox[req.To.ID] + 1
	oldOwner := saved.Owner
	unique.Owner = req.To
	saved.Owner, saved.FromUserID, saved.MsgID, saved.UpgradeMsgID = req.To, req.ActorUserID, msgID, msgID
	saved.Date, saved.SavedID, saved.CanTransferAt, saved.PinnedOrder = req.Date, 0, 0, 0
	saved.Unsaved, saved.NameHidden, saved.CollectionIDs, saved.Unique = req.RecipientUnsaved, false, nil, nil
	result := domain.StarGiftTransferResult{Saved: saved, Unique: unique}
	key := fmt.Sprintf("%s:ref:%d:%d", req.CommandKey, req.Ref.Owner.ID, req.Ref.MsgID)
	sent, err := s.sendUniqueLocked(req.ActorUserID, req.To.ID, req.Date, key, saved, unique, false, func() {
		result.Balance, _ = s.balance(req.ActorUserID, -req.ChargeStars, domain.StarsReasonGiftTransfer, req.To, req.Date)
		s.removeCollections(oldOwner, saved.ID)
		s.gifts.uniqueByID[unique.ID], s.gifts.gifts[i] = unique, saved
	})
	if err != nil {
		return domain.StarGiftTransferResult{}, err
	}
	result.Send = sent
	return result, nil
}

// sendUniqueLocked stages the state commit in MessageStore's infallible prefix:
// encoding/delivery failures leave issuance, ownership and Stars unchanged.
func (s *StarGiftOwnerStore) sendUniqueLocked(sender, recipient int64, date int, key string, saved domain.SavedStarGift, unique domain.UniqueStarGift, upgrade bool, apply func()) (domain.SendPrivateTextResult, error) {
	hash := sha256.Sum256([]byte(fmt.Sprintf("gift-owner:%d:%d:%s", sender, recipient, key)))
	randomID := int64(binary.LittleEndian.Uint64(hash[:8]))
	if randomID == 0 {
		randomID = 1
	}
	req := domain.SendPrivateTextRequest{SenderUserID: sender, RecipientUserID: recipient, Date: date, RandomID: randomID,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionStarGiftUnique,
			StarGiftUnique: &domain.MessageStarGiftUniqueAction{Gift: unique, FromUserID: sender, Upgrade: upgrade, Transferred: !upgrade,
				Saved: !saved.Unsaved, TransferStars: saved.TransferStars, CanTransferAt: saved.CanTransferAt, CanExportAt: saved.CanExportAt,
				CanResellAt: saved.CanResellAt, DropOriginalDetailsStars: saved.DropOriginalDetailsStars},
		}}}
	fingerprint, err := store.PrivateSendFingerprint(req)
	if err != nil {
		return domain.SendPrivateTextResult{}, err
	}
	return s.messages.sendPrivateTextLocked(req, fingerprint, &memoryPrivateSendPrefix{apply: func([]domain.UpdateEvent) { apply() }})
}

func memoryGiftReadyAt(date, delay int) int {
	if date >= math.MaxInt32 || delay > math.MaxInt32-date {
		return math.MaxInt32
	}
	return date + delay
}
