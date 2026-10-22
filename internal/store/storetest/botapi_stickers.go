package storetest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"reflect"
	"testing"

	"telesrv/internal/app/files"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// BotAPIStickerLifecycle exercises real bytes and mutations through files.Service,
// reopening the persistence adapter between stages rather than trusting app caches.
func BotAPIStickerLifecycle(t *testing.T, reopen func() store.MediaStore) {
	t.Helper()
	ctx := context.Background()
	blobs, err := files.NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fresh := func() *files.Service { return files.NewService(reopen(), blobs, 2) }
	svc := fresh()
	pngBytes := func(w, h int, c color.NRGBA) []byte {
		im := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := range h {
			for x := range w {
				im.SetNRGBA(x, y, c)
			}
		}
		var b bytes.Buffer
		if err := png.Encode(&b, im); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	input := func(c color.NRGBA) domain.BotAPIInputSticker {
		return domain.BotAPIInputSticker{File: domain.BotAPIFileInput{Bytes: pngBytes(512, 256, c)}, Format: "static", EmojiList: []string{"🙂", "😄"}, Keywords: []string{"one,two", "three"}}
	}
	const botID int64 = 71001
	const ownerID int64 = 71002
	set, err := svc.BotStickerCreate(ctx, botID, "fixture_bot", domain.BotAPICreateStickerSet{UserID: ownerID, Name: "lifecycle_by_fixture_bot", Title: "Lifecycle", Kind: domain.StickerSetKindStickers, Stickers: []domain.BotAPIInputSticker{input(color.NRGBA{R: 255, A: 255})}})
	if err != nil {
		t.Fatal(err)
	}
	oldID := set.DocumentIDs[0]
	svc = fresh()
	set, docs, err := svc.BotStickerOwnedSet(ctx, botID, ownerID, set.ShortName)
	if err != nil || set.CreatorBotID != botID || set.CreatorUserID != ownerID || len(docs) != 1 {
		t.Fatalf("durable create: %+v, %+v, %v", set, docs, err)
	}
	if len(set.Packs) != 2 || !reflect.DeepEqual(set.Keywords[0].Keywords, []string{"one,two", "three"}) {
		t.Fatalf("metadata lost: %+v", set)
	}
	if _, _, err := svc.BotStickerOwnedSet(ctx, botID+10, ownerID, set.ShortName); !errors.Is(err, domain.ErrStickerSetNotOwned) {
		t.Fatalf("other bot gained control: %v", err)
	}
	if _, _, err := svc.BotStickerOwnedSet(ctx, botID, ownerID+10, set.ShortName); !errors.Is(err, domain.ErrStickerSetNotOwned) {
		t.Fatalf("wrong proxy owner accepted: %v", err)
	}
	duplicate, err := svc.BotStickerAdd(ctx, botID, ownerID, set.ShortName, input(color.NRGBA{R: 255, A: 255}))
	if err != nil || !reflect.DeepEqual(duplicate.DocumentIDs, set.DocumentIDs) {
		t.Fatalf("byte-identical upload added twice: %+v %v", duplicate, err)
	}
	set, err = svc.BotStickerAdd(ctx, botID, ownerID, set.ShortName, input(color.NRGBA{G: 255, A: 255}))
	if err != nil {
		t.Fatal(err)
	}
	newID := set.DocumentIDs[1]
	set, err = svc.BotStickerMove(ctx, botID, newID, 0)
	if err != nil || !reflect.DeepEqual(set.DocumentIDs, []int64{newID, oldID}) {
		t.Fatalf("position: %+v %v", set, err)
	}
	if _, err = svc.BotStickerSetEmoji(ctx, botID, newID, []string{"🧡", "💚"}); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.BotStickerSetKeywords(ctx, botID, newID, []string{"new,keyword"}); err != nil {
		t.Fatal(err)
	}
	svc = fresh()
	set, docs, err = svc.BotStickerOwnedSet(ctx, botID, ownerID, set.ShortName)
	if err != nil {
		t.Fatal(err)
	}
	if docs[0].ID != newID {
		t.Fatalf("order lost: %+v", docs)
	}
	var foundKeyword bool
	for _, kw := range set.Keywords {
		if kw.DocumentID == newID {
			foundKeyword = reflect.DeepEqual(kw.Keywords, []string{"new,keyword"})
		}
	}
	if !foundKeyword {
		t.Fatalf("keyword edit lost: %+v", set.Keywords)
	}
	if _, err = svc.BotStickerSetMask(ctx, botID, newID, &domain.StickerMaskPosition{Point: "eyes", Scale: 1}); !errors.Is(err, domain.ErrStickerSetTypeInvalid) {
		t.Fatalf("regular mask edit: %v", err)
	}
	invalid := input(color.NRGBA{B: 255, A: 255})
	invalid.File.Bytes = []byte("not a png")
	if _, err = svc.BotStickerReplace(ctx, botID, ownerID, set.ShortName, newID, invalid); err == nil {
		t.Fatal("invalid replacement accepted")
	}
	svc = fresh()
	set, _, err = svc.BotStickerOwnedSet(ctx, botID, ownerID, set.ShortName)
	if err != nil || set.DocumentIDs[0] != newID {
		t.Fatalf("failed replacement removed old: %+v %v", set, err)
	}
	set, err = svc.BotStickerReplace(ctx, botID, ownerID, set.ShortName, newID, input(color.NRGBA{B: 255, A: 255}))
	if err != nil {
		t.Fatal(err)
	}
	replacementID := set.DocumentIDs[0]
	if replacementID == newID || set.DocumentIDs[1] != oldID {
		t.Fatalf("replacement position: %+v", set)
	}
	detached, err := reopen().GetDocuments(ctx, []int64{newID})
	if err != nil || len(detached) != 1 {
		t.Fatal(err)
	}
	if id, _, ok := detached[0].StickerSetRef(); ok || id != 0 {
		t.Fatal("replaced document still attached")
	}
	thumb := domain.BotAPIFileInput{Bytes: pngBytes(100, 100, color.NRGBA{R: 100, A: 255})}
	set, err = svc.BotStickerThumbnail(ctx, botID, ownerID, set.ShortName, "static", thumb, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	svc = fresh()
	set, _, err = svc.BotStickerOwnedSet(ctx, botID, ownerID, set.ShortName)
	if err != nil || set.ThumbDocumentID == 0 || len(set.Thumbs) == 0 {
		t.Fatalf("thumbnail lost: %+v %v", set, err)
	}
	blob, found, err := reopen().GetFileBlob(ctx, fmt.Sprintf("doc:%d:%s", set.ThumbDocumentID, set.Thumbs[0].Type))
	if err != nil || !found {
		t.Fatalf("thumbnail not downloadable: %+v %v", blob, err)
	}
	if _, err = svc.BotStickerTitle(ctx, botID, set.ShortName, "Renamed"); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.BotStickerRemove(ctx, botID, replacementID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.BotStickerRemove(ctx, botID, oldID); err != nil {
		t.Fatal(err)
	}
	svc = fresh()
	empty, _, err := svc.BotStickerOwnedSet(ctx, botID, ownerID, set.ShortName)
	if err != nil || empty.Count != 0 || len(empty.DocumentIDs) != 0 || empty.Title != "Renamed" {
		t.Fatalf("last removal or title lost: %+v %v", empty, err)
	}
	if _, err = svc.BotStickerDelete(ctx, botID, set.ShortName); err != nil {
		t.Fatal(err)
	}
	if _, _, err = fresh().BotStickerOwnedSet(ctx, botID, ownerID, set.ShortName); !errors.Is(err, domain.ErrStickerSetInvalid) {
		t.Fatalf("deleted set readable: %v", err)
	}

	mask := input(color.NRGBA{R: 80, A: 255})
	mask.Keywords = nil
	mask.MaskPosition = &domain.StickerMaskPosition{Point: "eyes", XShift: -0.5, YShift: 1, Scale: 2}
	set, err = svc.BotStickerCreate(ctx, botID, "fixture_bot", domain.BotAPICreateStickerSet{UserID: ownerID, Name: "masks_by_fixture_bot", Title: "Masks", Kind: domain.StickerSetKindMasks, Stickers: []domain.BotAPIInputSticker{mask}})
	if err != nil {
		t.Fatal(err)
	}
	svc = fresh()
	_, docs, err = svc.BotStickerOwnedSet(ctx, botID, ownerID, set.ShortName)
	if err != nil {
		t.Fatal(err)
	}
	var position *domain.StickerMaskPosition
	for _, a := range docs[0].Attributes {
		if a.Kind == domain.DocAttrSticker {
			position = a.MaskPosition
		}
	}
	if !reflect.DeepEqual(position, mask.MaskPosition) {
		t.Fatalf("mask position lost: %+v", position)
	}
	if _, err = svc.BotStickerSetEmoji(ctx, botID, set.DocumentIDs[0], []string{"🙂"}); !errors.Is(err, domain.ErrStickerSetTypeInvalid) {
		t.Fatalf("mask emoji edit accepted: %v", err)
	}
	if _, err = svc.BotStickerSetMask(ctx, botID, set.DocumentIDs[0], nil); err != nil {
		t.Fatal(err)
	}
	_, docs, err = fresh().BotStickerOwnedSet(ctx, botID, ownerID, set.ShortName)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range docs[0].Attributes {
		if a.MaskPosition != nil {
			t.Fatal("mask position not removed")
		}
	}
	emoji := domain.BotAPIInputSticker{File: domain.BotAPIFileInput{Bytes: pngBytes(100, 100, color.NRGBA{G: 80, A: 255})}, Format: "static", EmojiList: []string{"💚"}}
	set, err = svc.BotStickerCreate(ctx, botID, "fixture_bot", domain.BotAPICreateStickerSet{UserID: ownerID, Name: "emoji_by_fixture_bot", Title: "Emoji", Kind: domain.StickerSetKindEmoji, NeedsRepainting: true, Stickers: []domain.BotAPIInputSticker{emoji}})
	if err != nil {
		t.Fatal(err)
	}
	svc = fresh()
	set, docs, err = svc.BotStickerOwnedSet(ctx, botID, ownerID, set.ShortName)
	if err != nil || !set.TextColor || !docs[0].IsCustomEmoji() {
		t.Fatalf("custom emoji classification: %+v %+v %v", set, docs, err)
	}
	if _, err = svc.BotStickerThumbnail(ctx, botID, 0, set.ShortName, "", domain.BotAPIFileInput{}, set.DocumentIDs[0], true); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.BotStickerThumbnail(ctx, botID, 0, set.ShortName, "", domain.BotAPIFileInput{}, oldID, true); !errors.Is(err, domain.ErrStickerSetFileInvalid) {
		t.Fatalf("foreign emoji thumbnail accepted: %v", err)
	}
	concurrentName := "concurrent_by_fixture_bot"
	if _, err = svc.BotStickerCreate(ctx, botID, "fixture_bot", domain.BotAPICreateStickerSet{UserID: ownerID, Name: concurrentName, Title: "Concurrent", Kind: domain.StickerSetKindStickers, Stickers: []domain.BotAPIInputSticker{input(color.NRGBA{R: 50, A: 255})}}); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for _, item := range []domain.BotAPIInputSticker{input(color.NRGBA{R: 51, A: 255}), input(color.NRGBA{R: 52, A: 255})} {
		go func(item domain.BotAPIInputSticker) {
			_, err := svc.BotStickerAdd(ctx, botID, ownerID, concurrentName, item)
			results <- err
		}(item)
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	concurrent, _, err := fresh().BotStickerOwnedSet(ctx, botID, ownerID, concurrentName)
	if err != nil || concurrent.Count != 3 || concurrent.DocumentIDs[1] == concurrent.DocumentIDs[2] {
		t.Fatalf("concurrent add lost an item: %+v %v", concurrent, err)
	}
	for _, capacity := range []struct {
		kind                 domain.StickerSetKind
		count, width, height int
		name                 string
	}{
		{domain.StickerSetKindStickers, 120, 512, 1, "capacity_regular_by_fixture_bot"},
		{domain.StickerSetKindEmoji, 200, 100, 100, "capacity_emoji_by_fixture_bot"},
	} {
		makeInput := func(i int) domain.BotAPIInputSticker {
			return domain.BotAPIInputSticker{File: domain.BotAPIFileInput{Bytes: pngBytes(capacity.width, capacity.height, color.NRGBA{R: uint8(i), G: uint8(i >> 8), A: 255})}, Format: "static", EmojiList: []string{"🙂"}}
		}
		set, err := svc.BotStickerCreate(ctx, botID, "fixture_bot", domain.BotAPICreateStickerSet{UserID: ownerID, Name: capacity.name, Title: "Capacity", Kind: capacity.kind, Stickers: []domain.BotAPIInputSticker{makeInput(0)}})
		if err != nil {
			t.Fatal(err)
		}
		for i := 1; i < capacity.count; i++ {
			set, err = svc.BotStickerAdd(ctx, botID, ownerID, set.ShortName, makeInput(i))
			if err != nil {
				t.Fatalf("%s item %d: %v", capacity.kind, i, err)
			}
		}
		if _, err = svc.BotStickerAdd(ctx, botID, ownerID, set.ShortName, makeInput(capacity.count)); !errors.Is(err, domain.ErrStickerSetTooMuch) {
			t.Fatalf("%s overflow: %v", capacity.kind, err)
		}
		duplicate, err := svc.BotStickerAdd(ctx, botID, ownerID, set.ShortName, makeInput(0))
		if err != nil || !reflect.DeepEqual(duplicate.DocumentIDs, set.DocumentIDs) {
			t.Fatalf("full-set duplicate changed order: %v %v", duplicate.DocumentIDs, err)
		}
		durable, _, err := fresh().BotStickerOwnedSet(ctx, botID, ownerID, set.ShortName)
		if err != nil || durable.Count != capacity.count {
			t.Fatalf("%s durable capacity=%d error=%v", capacity.kind, durable.Count, err)
		}
	}
}
