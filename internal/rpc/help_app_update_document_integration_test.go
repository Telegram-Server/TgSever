package rpc

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/app/appupdates"
	filesapp "telesrv/internal/app/files"
	"telesrv/internal/store/postgres"
	"telesrv/internal/updatecdn"
)

// 接缝测试：真实的进程内更新目录、安装包注册器与文件服务（Postgres 媒体存储 + 本地 blob）
// 接在一起，help.getAppUpdate 先回答没有更新（包还没入库），入库后带上 document，客户端
// 用 upload.getFile 按 document 读回的字节就是发布的 APK——安卓 Standalone 客户端的
// FileLoader.loadFile(update.document) 走的正是这条路。功能矩阵在 updatecdn 与 appupdates
// 的单元测试里。
func TestHelpGetAppUpdateDocumentDownloadsThePublishedAPK(t *testing.T) {
	pool := testPostgresPool(t)
	ctx := context.Background()
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.json")
	filesDir := filepath.Join(dir, "files")
	apkPath := filepath.Join("..", "apkinfo", "testdata", "a-v1300-v2v3.apk")
	if _, err := updatecdn.PublishAndroidPackage(manifest, filesDir, apkPath, updatecdn.AndroidReleaseMeta{
		Channel: "stable", Notes: map[string]string{"en": "Fixes"},
	}, updatecdn.PublishOptions{}); err != nil {
		t.Fatal(err)
	}
	catalog, err := updatecdn.NewStore(manifest, filesDir, updatecdn.CatalogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := filesapp.NewLocalFS(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	files := filesapp.NewService(postgres.NewMediaStore(pool), blobs, 2, filesapp.WithVideoThumbnailer(nil))
	registry := appupdates.NewRegistry(catalog, postgres.NewAppUpdateDocumentStore(pool), files, zap.NewNop())
	router := New(Config{}, Deps{AppUpdates: catalog, AppUpdateDocuments: registry, Files: files}, zap.NewNop(), clock.System)
	reqCtx := WithClientInfo(WithUserID(ctx, 1000000001), ClientInfo{Type: ClientTypeAndroid, AppVersion: "12.2.10 (1234)", LangCode: "en"})

	first, err := router.onHelpGetAppUpdate(reqCtx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := first.(*tg.HelpNoAppUpdate); !ok {
		t.Fatalf("before registration = %T, want help.noAppUpdate", first)
	}
	registry.Wait()

	second, err := router.onHelpGetAppUpdate(reqCtx, "")
	if err != nil {
		t.Fatal(err)
	}
	update, ok := second.(*tg.HelpAppUpdate)
	if !ok {
		t.Fatalf("after registration = %T, want help.appUpdate", second)
	}
	docClass, ok := update.GetDocument()
	if !ok {
		t.Fatal("help.appUpdate has no document")
	}
	doc := docClass.(*tg.Document)
	if update.ID != 1300 || update.Version != "12.3.0" || doc.MimeType != updatecdn.AndroidPackageMimeType {
		t.Fatalf("update = %+v document = %+v", update, doc)
	}
	var fileName string
	for _, attr := range doc.Attributes {
		if a, ok := attr.(*tg.DocumentAttributeFilename); ok {
			fileName = a.FileName
		}
	}
	if fileName != "android-cc.example.app-1300.apk" {
		t.Fatalf("file name attribute = %q", fileName)
	}

	want, err := os.ReadFile(apkPath)
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	const chunk = 4096
	for offset := int64(0); offset < doc.Size; offset += chunk {
		part, err := router.onUploadGetFile(reqCtx, &tg.UploadGetFileRequest{
			Location: &tg.InputDocumentFileLocation{ID: doc.ID, AccessHash: doc.AccessHash, FileReference: doc.FileReference},
			Offset:   offset,
			Limit:    chunk,
		})
		if err != nil {
			t.Fatalf("upload.getFile offset %d: %v", offset, err)
		}
		got.Write(part.(*tg.UploadFile).Bytes)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("downloaded %d bytes, want the %d-byte APK", got.Len(), len(want))
	}
}
