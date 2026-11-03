package storetest

import (
	"context"
	"errors"
	"sync"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// DialogFolderLimitFixture 是一个空的文件夹集合：UserA、UserB 都还没有自定义文件夹。
type DialogFolderLimitFixture struct {
	Dialogs      store.DialogStore
	UserA, UserB int64
}

func limitTestFolder(id int, title string) domain.DialogFolder {
	return domain.DialogFolder{ID: id, Title: title, Contacts: true}
}

// DialogFolderLimitContract 是 UpsertFolderWithinLimit 的契约：新建按 limit 截停，
// 修改已有文件夹不受限（降级后超出的也能改），上限按用户计，删除后腾出名额，
// 同一用户的并发新建不会一起越过上限。
func DialogFolderLimitContract(t *testing.T, newFixture func(t *testing.T) DialogFolderLimitFixture) {
	t.Run("creation stops at the limit and edits are always allowed", func(t *testing.T) {
		f := newFixture(t)
		ctx := context.Background()
		for id := 2; id < 5; id++ {
			if err := f.Dialogs.UpsertFolderWithinLimit(ctx, f.UserA, limitTestFolder(id, "f"), 3); err != nil {
				t.Fatalf("create folder %d within limit: %v", id, err)
			}
		}
		if err := f.Dialogs.UpsertFolderWithinLimit(ctx, f.UserA, limitTestFolder(5, "over"), 3); !errors.Is(err, domain.ErrDialogFoldersTooMuch) {
			t.Fatalf("fourth folder at limit 3: err = %v, want ErrDialogFoldersTooMuch", err)
		}
		// 降级后上限变小：已有的文件夹仍可修改。
		if err := f.Dialogs.UpsertFolderWithinLimit(ctx, f.UserA, limitTestFolder(3, "renamed"), 1); err != nil {
			t.Fatalf("edit existing folder above a lower limit: %v", err)
		}
		list, err := f.Dialogs.ListFolders(ctx, f.UserA)
		if err != nil {
			t.Fatalf("ListFolders: %v", err)
		}
		if len(list.Folders) != 3 {
			t.Fatalf("folders = %d, want 3 (the rejected one not written)", len(list.Folders))
		}
		renamed := false
		for _, folder := range list.Folders {
			if folder.ID == 5 {
				t.Fatalf("rejected folder 5 was written: %+v", folder)
			}
			if folder.ID == 3 && folder.Title == "renamed" {
				renamed = true
			}
		}
		if !renamed {
			t.Fatalf("folder 3 edit not saved: %+v", list.Folders)
		}

		// 上限按用户计：另一个用户不受 A 的文件夹影响。
		if err := f.Dialogs.UpsertFolderWithinLimit(ctx, f.UserB, limitTestFolder(2, "b"), 1); err != nil {
			t.Fatalf("user B first folder: %v", err)
		}
		// 删掉一个就腾出名额。
		if err := f.Dialogs.DeleteFolder(ctx, f.UserA, 2); err != nil {
			t.Fatalf("DeleteFolder: %v", err)
		}
		if err := f.Dialogs.UpsertFolderWithinLimit(ctx, f.UserA, limitTestFolder(5, "freed"), 3); err != nil {
			t.Fatalf("create after delete: %v", err)
		}
	})

	t.Run("concurrent creations never pass the limit together", func(t *testing.T) {
		f := newFixture(t)
		ctx := context.Background()
		const limit, attempts = 3, 8
		var wg sync.WaitGroup
		errs := make([]error, attempts)
		for i := 0; i < attempts; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				errs[i] = f.Dialogs.UpsertFolderWithinLimit(ctx, f.UserA, limitTestFolder(2+i, "race"), limit)
			}(i)
		}
		wg.Wait()
		created := 0
		for i, err := range errs {
			switch {
			case err == nil:
				created++
			case !errors.Is(err, domain.ErrDialogFoldersTooMuch):
				t.Fatalf("attempt %d: unexpected error %v", i, err)
			}
		}
		list, err := f.Dialogs.ListFolders(ctx, f.UserA)
		if err != nil {
			t.Fatalf("ListFolders: %v", err)
		}
		if created != limit || len(list.Folders) != limit {
			t.Fatalf("created %d, stored %d, want exactly %d", created, len(list.Folders), limit)
		}
	})
}
