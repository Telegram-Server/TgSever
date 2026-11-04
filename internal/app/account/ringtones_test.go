package account

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func ringtoneDoc(id int64) domain.Document {
	return domain.Document{ID: id, AccessHash: id * 3, MimeType: "audio/mpeg", Size: 40 << 10}
}

func TestSavedRingtonesNewestFirstAndUnsave(t *testing.T) {
	ctx := context.Background()
	st := memory.NewPasswordStore()
	svc := NewService(st, WithSavedRingtones(st))
	const user = int64(42)

	for _, id := range []int64{1, 2, 3} {
		if changed, err := svc.SaveRingtone(ctx, user, ringtoneDoc(id), false); err != nil || !changed {
			t.Fatalf("save %d = changed %v err %v", id, changed, err)
		}
	}
	if changed, err := svc.SaveRingtone(ctx, user, ringtoneDoc(2), false); err != nil || changed {
		t.Fatalf("re-save 2 = changed %v err %v, want unchanged", changed, err)
	}
	if changed, err := svc.SaveRingtone(ctx, user, ringtoneDoc(1), true); err != nil || !changed {
		t.Fatalf("unsave 1 = changed %v err %v", changed, err)
	}
	if changed, err := svc.SaveRingtone(ctx, user, ringtoneDoc(1), true); err != nil || changed {
		t.Fatalf("unsave 1 again = changed %v err %v, want unchanged", changed, err)
	}
	ids, err := svc.ListSavedRingtoneIDs(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{3, 2}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	if other, err := svc.ListSavedRingtoneIDs(ctx, user+1); err != nil || len(other) != 0 {
		t.Fatalf("other user ids = %v err %v, want empty", other, err)
	}
}

func TestSaveRingtoneRejectsWhatClientsCannotPlay(t *testing.T) {
	ctx := context.Background()
	st := memory.NewPasswordStore()
	svc := NewService(st, WithSavedRingtones(st))
	const user = int64(42)

	big := ringtoneDoc(10)
	big.Size = domain.RingtoneSizeMax + 1
	video := ringtoneDoc(11)
	video.MimeType = "video/mp4"
	long := ringtoneDoc(12)
	long.Attributes = []domain.DocumentAttribute{{Kind: domain.DocAttrAudio, AudioDuration: domain.RingtoneDurationMax + 1}}
	voice := ringtoneDoc(13)
	voice.MimeType = "application/octet-stream"
	voice.Attributes = []domain.DocumentAttribute{{Kind: domain.DocAttrAudio, Voice: true, AudioDuration: 3}}

	cases := []struct {
		name string
		doc  domain.Document
		want error
	}{
		{"too big", big, domain.ErrRingtoneSizeTooBig},
		{"not audio", video, domain.ErrRingtoneMimeInvalid},
		{"too long", long, domain.ErrRingtoneDurationTooLong},
		{"voice note by attribute", voice, nil},
	}
	for _, tc := range cases {
		_, err := svc.SaveRingtone(ctx, user, tc.doc, false)
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	// 取消保存不校验文档本身：已经存下的旧铃声总能删掉。
	if _, err := svc.SaveRingtone(ctx, user, big, true); err != nil {
		t.Fatalf("unsave oversized = %v, want nil", err)
	}
}

func TestSaveRingtoneStopsAtTheSavedCountLimit(t *testing.T) {
	ctx := context.Background()
	st := memory.NewPasswordStore()
	svc := NewService(st, WithSavedRingtones(st))
	const user = int64(42)

	for id := int64(1); id <= domain.RingtoneSavedCountMax; id++ {
		if _, err := svc.SaveRingtone(ctx, user, ringtoneDoc(id), false); err != nil {
			t.Fatalf("save %d: %v", id, err)
		}
	}
	if _, err := svc.SaveRingtone(ctx, user, ringtoneDoc(domain.RingtoneSavedCountMax+1), false); !errors.Is(err, domain.ErrRingtoneInvalid) {
		t.Fatalf("save over limit = %v, want ErrRingtoneInvalid", err)
	}
	// 已在列表里的再保存不算新增，不受上限影响。
	if _, err := svc.SaveRingtone(ctx, user, ringtoneDoc(1), false); err != nil {
		t.Fatalf("re-save at limit = %v, want nil", err)
	}
}
