package memory

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestMediaStoreSnapshotsAndTransactionRollback(t *testing.T) {
	ctx := context.Background()
	media := NewMediaStore()
	position := &domain.StickerMaskPosition{Point: "eyes", Scale: 1}
	doc := domain.Document{ID: 10, AccessHash: 20, Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker, Mask: true, MaskPosition: position}}}
	if err := media.PutDocument(ctx, doc); err != nil {
		t.Fatal(err)
	}
	position.Point = "mouth"
	loaded, found, err := media.GetDocument(ctx, 10)
	if err != nil || !found || loaded.Attributes[0].MaskPosition.Point != "eyes" {
		t.Fatalf("input alias changed durable document: %+v %v", loaded, err)
	}
	loaded.Attributes[0].MaskPosition.Point = "chin"
	loaded, _, err = media.GetDocument(ctx, 10)
	if err != nil || loaded.Attributes[0].MaskPosition.Point != "eyes" {
		t.Fatalf("read alias changed durable document: %+v %v", loaded, err)
	}
	rollback := errors.New("abort materialization")
	mutate := func(ctx context.Context, tx store.MediaStore) error {
		loaded.Attributes[0].Alt = "🙂"
		if err := tx.PutDocument(ctx, loaded); err != nil {
			return err
		}
		if err := tx.PutStickerSet(ctx, domain.StickerSet{ID: 30, AccessHash: 40, CreatorUserID: 50, CreatorBotID: 60, ShortName: "transaction_by_fixture_bot", DocumentIDs: []int64{10}, Count: 1}); err != nil {
			return err
		}
		return nil
	}
	if err := media.WithTx(ctx, func(ctx context.Context, tx store.MediaStore) error {
		if err := mutate(ctx, tx); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("rollback error: %v", err)
	}
	if _, found, err := media.GetStickerSetByID(ctx, 30); err != nil || found {
		t.Fatalf("aborted set became visible: found=%v err=%v", found, err)
	}
	loaded, _, err = media.GetDocument(ctx, 10)
	if err != nil || loaded.Attributes[0].Alt != "" {
		t.Fatalf("aborted document edit became visible: %+v %v", loaded, err)
	}
	if err := media.WithTx(ctx, mutate); err != nil {
		t.Fatal(err)
	}
	set, found, err := media.GetStickerSetByID(ctx, 30)
	if err != nil || !found || set.CreatorBotID != 60 || set.DocumentIDs[0] != 10 {
		t.Fatalf("committed set lost: %+v %v", set, err)
	}
	loaded, _, err = media.GetDocument(ctx, 10)
	if err != nil || loaded.Attributes[0].Alt != "🙂" {
		t.Fatalf("committed document lost: %+v %v", loaded, err)
	}
}
