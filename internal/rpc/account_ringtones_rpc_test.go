package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	appaccount "telesrv/internal/app/account"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func ringtoneRouter(t *testing.T, files *fakeFiles) (*Router, *captureSessions) {
	t.Helper()
	passwords := memory.NewPasswordStore()
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Account:  appaccount.NewService(passwords, appaccount.WithSavedRingtones(passwords)),
		Files:    files,
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	return r, sessions
}

// 上传的铃声直接进已保存列表（客户端上传后不会再调 saveRingtone），其它设备收到
// updateSavedRingtones；取消保存后列表变空。
func TestUploadRingtoneJoinsSavedListAndUnsaveRemovesIt(t *testing.T) {
	files := &fakeFiles{}
	r, sessions := ringtoneRouter(t, files)
	ctx := WithUserID(context.Background(), 1000003)

	req := &tg.AccountUploadRingtoneRequest{File: &tg.InputFile{ID: 5, Parts: 1, Name: "ding.mp3"}, FileName: "ding.mp3", MimeType: "audio/mpeg"}
	got, err := r.onAccountUploadRingtone(ctx, req)
	if err != nil {
		t.Fatalf("uploadRingtone: %v", err)
	}
	doc, ok := got.(*tg.Document)
	if !ok || doc.ID == 0 {
		t.Fatalf("uploadRingtone = %#v, want a document", got)
	}
	updates, ok := sessions.snapshot().message.(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("pushed = %#v, want updateSavedRingtones", sessions.snapshot().message)
	}
	if _, ok := updates.Updates[0].(*tg.UpdateSavedRingtones); !ok {
		t.Fatalf("pushed update = %T, want *tg.UpdateSavedRingtones", updates.Updates[0])
	}

	list, err := r.onAccountGetSavedRingtones(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	saved, ok := list.(*tg.AccountSavedRingtones)
	if !ok || len(saved.Ringtones) != 1 || documentClassID(saved.Ringtones[0]) != doc.ID {
		t.Fatalf("saved ringtones = %#v, want the uploaded document", list)
	}
	if again, err := r.onAccountGetSavedRingtones(ctx, saved.Hash); err != nil {
		t.Fatal(err)
	} else if _, ok := again.(*tg.AccountSavedRingtonesNotModified); !ok {
		t.Fatalf("same hash = %T, want notModified", again)
	}

	res, err := r.onAccountSaveRingtone(ctx, &tg.AccountSaveRingtoneRequest{ID: &tg.InputDocument{ID: doc.ID, AccessHash: doc.AccessHash}, Unsave: true})
	if err != nil {
		t.Fatalf("unsave: %v", err)
	}
	if _, ok := res.(*tg.AccountSavedRingtone); !ok {
		t.Fatalf("unsave = %T, want *tg.AccountSavedRingtone", res)
	}
	list, err = r.onAccountGetSavedRingtones(ctx, saved.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if empty, ok := list.(*tg.AccountSavedRingtones); !ok || len(empty.Ringtones) != 0 {
		t.Fatalf("after unsave = %#v, want empty list", list)
	}
}

// 错误码是 TDesktop 认得的那几个：非音频上传 RINGTONE_MIME_INVALID 且不落文档；
// 把聊天里的视频存成铃声、access_hash 不对都返回对应错误。
func TestRingtoneErrorsUseTheCodesClientsRecognise(t *testing.T) {
	video := domain.Document{ID: 77, AccessHash: 7, MimeType: "video/mp4", Size: 10}
	files := &fakeFiles{docs: map[int64]domain.Document{video.ID: video}}
	r, _ := ringtoneRouter(t, files)
	ctx := WithUserID(context.Background(), 1000004)

	bad := &tg.AccountUploadRingtoneRequest{File: &tg.InputFile{ID: 6, Parts: 1, Name: "x.png"}, FileName: "x.png", MimeType: "image/png"}
	if _, err := r.onAccountUploadRingtone(ctx, bad); !tgerr.Is(err, "RINGTONE_MIME_INVALID") {
		t.Fatalf("upload image = %v, want RINGTONE_MIME_INVALID", err)
	}
	if files.createUploadCalls != 0 {
		t.Fatalf("rejected upload created %d documents, want 0", files.createUploadCalls)
	}
	if _, err := r.onAccountSaveRingtone(ctx, &tg.AccountSaveRingtoneRequest{ID: &tg.InputDocument{ID: video.ID, AccessHash: video.AccessHash}}); !tgerr.Is(err, "RINGTONE_MIME_INVALID") {
		t.Fatalf("save video = %v, want RINGTONE_MIME_INVALID", err)
	}
	if _, err := r.onAccountSaveRingtone(ctx, &tg.AccountSaveRingtoneRequest{ID: &tg.InputDocument{ID: video.ID, AccessHash: 999}}); !tgerr.Is(err, "RINGTONE_INVALID") {
		t.Fatalf("save with wrong access hash = %v, want RINGTONE_INVALID", err)
	}
}
