package storetest

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// SavedRingtoneContract：已保存铃声按保存先后最新在前；重复保存与重复删除报告无变化；
// 满 max 后新增返回 ErrRingtoneInvalid，已在列表里的再保存不受影响；用户之间隔离。
// user/other 是两个没有已保存铃声的用户，docs 是三个已存在的文档 id。
func SavedRingtoneContract(t *testing.T, s store.SavedRingtoneStore, user, other int64, docs [3]int64) {
	t.Helper()
	ctx := context.Background()
	add := func(owner, doc int64, max int) (bool, error) {
		t.Helper()
		return s.AddSavedRingtone(ctx, owner, doc, max)
	}
	list := func(owner int64, want []int64) {
		t.Helper()
		got, err := s.ListSavedRingtoneIDs(ctx, owner)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) == 0 && len(want) == 0 {
			return
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("user %d ringtones = %v, want %v", owner, got, want)
		}
	}

	list(user, nil)
	for _, doc := range docs[:2] {
		if added, err := add(user, doc, 2); err != nil || !added {
			t.Fatalf("add %d = %v %v, want added", doc, added, err)
		}
	}
	if added, err := add(user, docs[0], 2); err != nil || added {
		t.Fatalf("re-add at limit = %v %v, want unchanged", added, err)
	}
	if _, err := add(user, docs[2], 2); !errors.Is(err, domain.ErrRingtoneInvalid) {
		t.Fatalf("add over limit = %v, want ErrRingtoneInvalid", err)
	}
	list(user, []int64{docs[1], docs[0]})

	if added, err := add(other, docs[2], 2); err != nil || !added {
		t.Fatalf("other add = %v %v", added, err)
	}
	list(other, []int64{docs[2]})

	if removed, err := s.RemoveSavedRingtone(ctx, user, docs[1]); err != nil || !removed {
		t.Fatalf("remove = %v %v, want removed", removed, err)
	}
	if removed, err := s.RemoveSavedRingtone(ctx, user, docs[1]); err != nil || removed {
		t.Fatalf("remove again = %v %v, want unchanged", removed, err)
	}
	if added, err := add(user, docs[2], 2); err != nil || !added {
		t.Fatalf("add after remove = %v %v", added, err)
	}
	list(user, []int64{docs[2], docs[0]})
	list(other, []int64{docs[2]})
}
