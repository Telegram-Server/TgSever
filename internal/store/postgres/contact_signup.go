package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

var _ store.ContactSignUpNoticeStore = (*MessageStore)(nil)

// DeliverContactSignUpNotice 写一条只属于 Owner 一侧的 contactSignUp 服务消息（新账号默认入驻）。
// 结构与 DeliverBroadcastRecipient 相同：private_messages 只有收方一侧（sender_box_id = 0），
// Owner 的 box、会话、pts 事件、投递任务与 contact_sign_up_notices 回执在同一个事务里。
func (s *MessageStore) DeliverContactSignUpNotice(ctx context.Context, n domain.ContactSignUpNotice) (domain.Message, bool, error) {
	if n.OwnerUserID <= 0 || n.FromUserID <= 0 || n.OwnerUserID == n.FromUserID {
		return domain.Message{}, false, domain.ErrUserNotFound
	}
	if n.Date == 0 {
		n.Date = int(time.Now().Unix())
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.Message{}, false, fmt.Errorf("deliver contact sign up notice: database does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.Message{}, false, fmt.Errorf("begin contact sign up notice: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	// 锁住 Owner 的账号行：与注销互斥，同一 Owner 的两次写入在这里排队后由回执去重。
	if err := lockUsersForUpdate(ctx, tx, n.OwnerUserID); err != nil {
		return domain.Message{}, false, fmt.Errorf("lock contact sign up owner: %w", err)
	}
	qtx := sqlcgen.New(tx)
	if boxID, err := qtx.GetContactSignUpNoticeBoxID(ctx, sqlcgen.GetContactSignUpNoticeBoxIDParams{OwnerUserID: n.OwnerUserID, FromUserID: n.FromUserID}); err == nil {
		list, err := s.GetByIDs(ctx, n.OwnerUserID, []int{int(boxID)})
		if err != nil {
			return domain.Message{}, false, fmt.Errorf("load replayed contact sign up notice: %w", err)
		}
		if len(list.Messages) == 0 {
			// Owner 已经删掉了这条消息：回执仍在，不再重写。
			return domain.Message{ID: int(boxID), OwnerUserID: n.OwnerUserID}, false, nil
		}
		return list.Messages[0], false, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Message{}, false, fmt.Errorf("read contact sign up notice receipt: %w", err)
	}
	eligible, err := qtx.ContactSignUpNoticeEligible(ctx, sqlcgen.ContactSignUpNoticeEligibleParams{OwnerUserID: n.OwnerUserID, FromUserID: n.FromUserID})
	if err != nil {
		return domain.Message{}, false, fmt.Errorf("check contact sign up notice accounts: %w", err)
	}
	if !eligible {
		return domain.Message{}, false, domain.ErrUserNotFound
	}

	media := &domain.MessageMedia{
		Kind:          domain.MessageMediaKindService,
		ServiceAction: &domain.MessageServiceAction{Kind: domain.MessageServiceActionContactSignUp},
	}
	mediaJSON, err := encodeMessageMedia(media)
	if err != nil {
		return domain.Message{}, false, fmt.Errorf("encode contact sign up action: %w", err)
	}
	pm, err := qtx.CreatePrivateMessage(ctx, sqlcgen.CreatePrivateMessageParams{
		SenderUserID:       n.FromUserID,
		RecipientUserID:    n.OwnerUserID,
		RandomID:           0,
		RequestFingerprint: []byte{},
		RecipientDelivered: true,
		MessageDate:        int32(n.Date),
		EntitiesJson:       []byte("[]"),
		Silent:             n.Silent,
		QuoteEntitiesJson:  []byte("[]"),
		MediaJson:          mediaJSON,
		ReplyMarkupJson:    []byte("{}"),
		RichMessageJson:    []byte("{}"),
	})
	if err != nil {
		return domain.Message{}, false, fmt.Errorf("create contact sign up private message: %w", err)
	}
	boxID, err := s.nextBoxID(ctx, tx, n.OwnerUserID)
	if err != nil {
		return domain.Message{}, false, fmt.Errorf("allocate contact sign up box id: %w", err)
	}
	if boxID <= 0 || boxID > domain.MaxMessageBoxID {
		return domain.Message{}, false, fmt.Errorf("allocate contact sign up box id: %d", boxID)
	}
	pts, err := s.reservePts(ctx, tx, n.OwnerUserID)
	if err != nil {
		return domain.Message{}, false, fmt.Errorf("allocate contact sign up pts: %w", err)
	}
	boxRow, err := qtx.CreateMessageBox(ctx, sqlcgen.CreateMessageBoxParams{
		OwnerUserID:       n.OwnerUserID,
		BoxID:             int32(boxID),
		PrivateMessageID:  pm.ID,
		MessageSenderID:   n.FromUserID,
		PeerType:          string(domain.PeerTypeUser),
		PeerID:            n.FromUserID,
		FromUserID:        n.FromUserID,
		MessageDate:       int32(n.Date),
		Outgoing:          false,
		EntitiesJson:      []byte("[]"),
		Silent:            n.Silent,
		QuoteEntitiesJson: []byte("[]"),
		Pts:               int32(pts),
		MediaJson:         mediaJSON,
		ReplyMarkupJson:   []byte("{}"),
		RichMessageJson:   []byte("{}"),
	})
	if err != nil {
		return domain.Message{}, false, fmt.Errorf("create contact sign up box: %w", err)
	}
	msg, err := messageFromBoxRow(boxRow)
	if err != nil {
		return domain.Message{}, false, err
	}
	if err := qtx.UpsertInboxDialog(ctx, sqlcgen.UpsertInboxDialogParams{
		UserID:         n.OwnerUserID,
		PeerType:       string(domain.PeerTypeUser),
		PeerID:         n.FromUserID,
		TopMessageID:   int32(msg.ID),
		TopMessageDate: int32(msg.Date),
	}); err != nil {
		return domain.Message{}, false, fmt.Errorf("upsert contact sign up dialog: %w", err)
	}
	if err := appendNewMessageEvent(ctx, qtx, msg); err != nil {
		return domain.Message{}, false, err
	}
	if err := enqueueDispatch(ctx, qtx, sqlcgen.EnqueueDispatchParams{
		TargetUserID: n.OwnerUserID,
		Pts:          int32(msg.Pts),
		EventType:    string(domain.UpdateEventNewMessage),
	}); err != nil {
		return domain.Message{}, false, fmt.Errorf("enqueue contact sign up dispatch: %w", err)
	}
	if tag, err := tx.Exec(ctx, `
UPDATE private_messages
SET recipient_box_id = $3, recipient_pts = $4
WHERE sender_user_id = $1 AND id = $2
  AND recipient_delivered
  AND recipient_box_id = 0 AND recipient_pts = 0`,
		n.FromUserID, pm.ID, msg.ID, msg.Pts); err != nil {
		return domain.Message{}, false, fmt.Errorf("save contact sign up private receipt: %w", err)
	} else if tag.RowsAffected() != 1 {
		return domain.Message{}, false, fmt.Errorf("save contact sign up private receipt: logical message disappeared")
	}
	if err := qtx.InsertContactSignUpNotice(ctx, sqlcgen.InsertContactSignUpNoticeParams{OwnerUserID: n.OwnerUserID, FromUserID: n.FromUserID, BoxID: int32(msg.ID)}); err != nil {
		return domain.Message{}, false, fmt.Errorf("record contact sign up notice: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Message{}, false, fmt.Errorf("commit contact sign up notice: %w", err)
	}
	committed = true
	return msg, true, nil
}
