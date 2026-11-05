package rpc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
	"telesrv/internal/updatecdn"
)

type fakeAppUpdateResolver struct {
	request updatecdn.ResolveRequest
	result  *updatecdn.ResolvedUpdate
	err     error
}

func (f *fakeAppUpdateResolver) Resolve(_ context.Context, req updatecdn.ResolveRequest) (*updatecdn.ResolvedUpdate, error) {
	f.request = req
	return f.result, f.err
}

func TestHelpGetAppUpdateUsesClientPlatformVersionSourceAndLanguage(t *testing.T) {
	resolver := &fakeAppUpdateResolver{result: &updatecdn.ResolvedUpdate{
		ID: 91, Version: "12.9.1", Text: "Новая версия", URL: "https://updates.example/app.apk", CanNotSkip: true,
	}}
	router := New(Config{PublicBaseURL: "https://telesrv.example"}, Deps{AppUpdates: resolver}, zap.NewNop(), clock.System)
	ctx := WithClientInfo(WithUserID(context.Background(), 1000000001), ClientInfo{
		Type: ClientTypeAndroid, AppVersion: "12.9.0 (500)", LangCode: "ru", SystemLangCode: "en",
	})
	result, err := router.onHelpGetAppUpdate(ctx, "com.example.store")
	if err != nil {
		t.Fatal(err)
	}
	update, ok := result.(*tg.HelpAppUpdate)
	if !ok {
		t.Fatalf("result = %T, want *tg.HelpAppUpdate", result)
	}
	if update.ID != 91 || update.Version != "12.9.1" || update.Text != "Новая версия" || !update.GetCanNotSkip() {
		t.Fatalf("update = %#v", update)
	}
	if got, ok := update.GetURL(); !ok || got != "https://updates.example/app.apk" {
		t.Fatalf("url = %q, %v", got, ok)
	}
	if resolver.request.Platform != "android" || resolver.request.Version != "12.9.0 (500)" ||
		resolver.request.Source != "com.example.store" || resolver.request.LangCode != "ru" {
		t.Fatalf("resolve request = %#v", resolver.request)
	}
}

type fakeAppUpdateDocuments struct {
	doc   domain.Document
	ready bool
	err   error
	asked []updatecdn.ResolvedFile
}

func (f *fakeAppUpdateDocuments) Document(_ context.Context, file updatecdn.ResolvedFile) (domain.Document, bool, error) {
	f.asked = append(f.asked, file)
	return f.doc, f.ready, f.err
}

// 带安装包的更新（Android）只在包已注册成文档后下发，并带上 document；还没就绪、查询出错、
// 没有接上注册器时一律回答 help.noAppUpdate，绝不先给一个只有 url 的结果。
func TestHelpGetAppUpdateCarriesDocumentOnlyWhenReady(t *testing.T) {
	file := &updatecdn.ResolvedFile{Name: "android-cc.example.app-1300.apk", SHA256: strings.Repeat("a", 64), Size: 8339, MimeType: updatecdn.AndroidPackageMimeType}
	resolver := &fakeAppUpdateResolver{result: &updatecdn.ResolvedUpdate{ID: 1300, Version: "12.3.0", Text: "Fixes", File: file}}
	ctx := WithClientInfo(WithUserID(context.Background(), 1000000001), ClientInfo{Type: ClientTypeAndroid, AppVersion: "12.2.10 (1234)"})
	doc := domain.Document{ID: 77, AccessHash: 5, FileReference: []byte{1}, MimeType: updatecdn.AndroidPackageMimeType, Size: 8339, DCID: 2,
		Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrFilename, FileName: file.Name}}}

	notReady := []struct {
		name string
		docs AppUpdateDocumentProvider
	}{
		{"no registry", nil},
		{"not ready", &fakeAppUpdateDocuments{}},
		{"lookup error", &fakeAppUpdateDocuments{err: errors.New("db down")}},
	}
	for _, tc := range notReady {
		router := New(Config{}, Deps{AppUpdates: resolver, AppUpdateDocuments: tc.docs}, zap.NewNop(), clock.System)
		result, err := router.onHelpGetAppUpdate(ctx, "")
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if _, ok := result.(*tg.HelpNoAppUpdate); !ok {
			t.Fatalf("%s: result = %T, want *tg.HelpNoAppUpdate", tc.name, result)
		}
	}

	docs := &fakeAppUpdateDocuments{doc: doc, ready: true}
	router := New(Config{}, Deps{AppUpdates: resolver, AppUpdateDocuments: docs}, zap.NewNop(), clock.System)
	result, err := router.onHelpGetAppUpdate(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	update, ok := result.(*tg.HelpAppUpdate)
	if !ok {
		t.Fatalf("result = %T, want *tg.HelpAppUpdate", result)
	}
	got, ok := update.GetDocument()
	if !ok {
		t.Fatal("help.appUpdate has no document")
	}
	tgDoc, ok := got.(*tg.Document)
	if !ok || tgDoc.ID != 77 || tgDoc.MimeType != updatecdn.AndroidPackageMimeType || tgDoc.Size != 8339 {
		t.Fatalf("document = %#v", got)
	}
	if len(docs.asked) != 1 || docs.asked[0] != *file {
		t.Fatalf("asked for %+v, want the resolved file", docs.asked)
	}
}

func TestHelpGetAppUpdateFailsClosedWithoutRPCError(t *testing.T) {
	resolver := &fakeAppUpdateResolver{err: errors.New("service unavailable")}
	router := New(Config{PublicBaseURL: "https://telesrv.example"}, Deps{AppUpdates: resolver}, zap.NewNop(), clock.System)
	ctx := WithClientInfo(WithUserID(context.Background(), 1000000001), ClientInfo{Type: ClientTypeIOS, AppVersion: "12.9.0"})
	result, err := router.onHelpGetAppUpdate(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.(*tg.HelpNoAppUpdate); !ok {
		t.Fatalf("result = %T, want *tg.HelpNoAppUpdate", result)
	}
}
